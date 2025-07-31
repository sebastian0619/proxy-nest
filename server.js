const express = require('express');
const morgan = require('morgan');
const { Worker, isMainThread } = require('worker_threads');
const path = require('path');
const fs = require('fs/promises');
const LRUCache = require('lru-cache');
const crypto = require('crypto');

// 从 utils 导入实际需要的函数
const {
  initializeLogPrefix,
  initializeCache,
  getCacheKey,
  validateResponse
} = require('./utils');

// 配置常量
const {
  PORT,
  NUM_WORKERS,
  REQUEST_TIMEOUT,
  UPSTREAM_TYPE,
  TMDB_API_KEY,
  TMDB_IMAGE_TEST_URL,
  BASE_WEIGHT_MULTIPLIER,
  DYNAMIC_WEIGHT_MULTIPLIER,
  ALPHA_INITIAL,
  ALPHA_ADJUSTMENT_STEP,
  MAX_SERVER_SWITCHES,
  UNHEALTHY_TIMEOUT,
  MAX_ERRORS_BEFORE_UNHEALTHY
} = require('./config');

// 全局变量
const workers = new Map();
let healthCheckerWorker;

// 设置工作线程事件处理器
function setupWorkerEventHandlers(worker, workerId) {
  worker.on('message', (message) => {
    if (message.type === 'response_time') {
      // 转发响应时间给health_checker
      healthCheckerWorker.postMessage(message);
    }
  });

  worker.on('error', (error) => {
    console.error(global.LOG_PREFIX.ERROR, `工作线程 ${workerId} 错误:`, error);
  });

  worker.on('exit', (code) => {
    if (code !== 0) {
      console.error(global.LOG_PREFIX.ERROR, `工作线程 ${workerId} 异常退出，代码: ${code}`);
      setTimeout(() => initializeWorker(workerId), 1000);
    }
  });
}

// 设置健康检查线程的事件处理器
function setupHealthCheckerEventHandlers() {
  healthCheckerWorker.on('message', (message) => {
    if (message.type === 'health_status_update') {
      // 转发状态更新给所有worker
      for (const worker of workers.values()) {
        worker.postMessage({
          type: 'server_health_update',
          data: message.data
        });
      }
    }
  });

  healthCheckerWorker.on('error', (error) => {
    console.error(global.LOG_PREFIX.ERROR, `健康检查线程错误:`, error);
  });

  healthCheckerWorker.on('exit', (code) => {
    if (code !== 0) {
      console.error(global.LOG_PREFIX.ERROR, `健康检查线程异常退出，代码: ${code}`);
      setTimeout(initializeHealthChecker, 1000);
    }
  });
}

// 初始化健康检查线程
async function initializeHealthChecker() {
  healthCheckerWorker = new Worker('./health_checker.js', {
    workerData: {
      UPSTREAM_TYPE,
      TMDB_API_KEY,
      TMDB_IMAGE_TEST_URL,
      REQUEST_TIMEOUT,
      UNHEALTHY_TIMEOUT,
      MAX_ERRORS_BEFORE_UNHEALTHY,
      BASE_WEIGHT_MULTIPLIER,
      DYNAMIC_WEIGHT_MULTIPLIER,
      LOG_PREFIX: global.LOG_PREFIX
    }
  });

  // 设置事件处理器
  setupHealthCheckerEventHandlers();

  // 初始化健康检查
  healthCheckerWorker.postMessage({
    type: 'initialize',
    data: {
      upstreamServers: process.env.UPSTREAM_SERVERS
    }
  });
}

// 主服务器启动函数
async function startServer() {
  try {
    // 先初始化日志前缀
    global.LOG_PREFIX = await initializeLogPrefix();
    if (!global.LOG_PREFIX) {
      throw new Error('初始化日志前缀失败');
    }

    console.log(global.LOG_PREFIX.INFO, '日志系统初始化成功');
    
    // 初始化 Express 应用
    const app = express();
    app.use(morgan('combined'));

    // 初始化缓存
    const { diskCache, lruCache } = await initializeCache();

    // 初始化健康检查线程
    await initializeHealthChecker();

    // 初始化工作线程池
    await initializeWorkerPool({
      LOG_PREFIX: global.LOG_PREFIX
    });

    // 设置路由
    setupRoutes(app, diskCache, lruCache);

    // 启动服务器
    app.listen(PORT, () => {
      console.log(global.LOG_PREFIX.SUCCESS, `服务器启动在端口 ${PORT}`);
    });

  } catch (error) {
    console.error(global.LOG_PREFIX?.ERROR || '[ 错误 ]', `服务器启动失败: ${error.message}`);
    process.exit(1);
  }
}

// 初始化工作线程池
async function initializeWorkerPool(workerData) {
  if (!global.LOG_PREFIX) {
    throw new Error('LOG_PREFIX 未初始化');
  }
  
  console.log(global.LOG_PREFIX.INFO, `初始化 ${NUM_WORKERS} 个工作线程`);
  
  for (let i = 0; i < NUM_WORKERS; i++) {
    await initializeWorker(i, workerData);
  }
}

// 初始化单个工作线程
async function initializeWorker(workerId, workerData) {
  const worker = new Worker('./worker.js', {
    workerData: {
      ...workerData,
      workerId,
      upstreamServers: process.env.UPSTREAM_SERVERS,
      UPSTREAM_TYPE,
      TMDB_API_KEY,
      TMDB_IMAGE_TEST_URL,
      REQUEST_TIMEOUT,
      MAX_SERVER_SWITCHES,
      BASE_WEIGHT_MULTIPLIER,
      DYNAMIC_WEIGHT_MULTIPLIER,
      ALPHA_INITIAL,
      ALPHA_ADJUSTMENT_STEP,
      UNHEALTHY_TIMEOUT,
      MAX_ERRORS_BEFORE_UNHEALTHY
    }
  });

  setupWorkerEventHandlers(worker, workerId);
  workers.set(workerId, worker);
}

// 添加缺失的 getCacheItem 函数
async function getCacheItem(cacheKey, diskCache, lruCache) {
  // 先检查内存缓存
  let cachedItem = lruCache.get(cacheKey);
  if (cachedItem) {
    // 验证缓存内容
    try {
      const isValid = validateResponse(
        cachedItem.data,
        cachedItem.contentType,
        UPSTREAM_TYPE
      );
      if (isValid) {
        console.log(global.LOG_PREFIX.CACHE.HIT, `内存缓存命中: ${cacheKey}`);
        return cachedItem;
      }
    } catch (error) {
      console.error(global.LOG_PREFIX.ERROR, `缓存验证失败: ${error.message}`);
    }
  }

  // 检查磁盘缓存
  try {
    cachedItem = await diskCache.get(cacheKey);
    if (cachedItem) {
      // 验证缓存内容
      const isValid = validateResponse(
        cachedItem.data,
        cachedItem.contentType,
        UPSTREAM_TYPE
      );
      if (isValid) {
        console.log(global.LOG_PREFIX.CACHE.HIT, `磁盘缓存命中: ${cacheKey}`);
        // 检查是否应该加载到内存缓存
        const mimeCategory = cachedItem.contentType.split(';')[0].trim().toLowerCase();
        
        // 检查lruCache是否有getContentTypeConfig方法
        let contentTypeConfig = null;
        if (lruCache && typeof lruCache.getContentTypeConfig === 'function') {
          contentTypeConfig = lruCache.getContentTypeConfig(mimeCategory);
        }
        
        if (!contentTypeConfig || !contentTypeConfig.skip_memory) {
          lruCache.set(cacheKey, cachedItem, cachedItem.contentType);
        }
        return cachedItem;
      }
    }
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, `读取缓存失败: ${error.message}`);
  }

  console.log(global.LOG_PREFIX.CACHE.MISS, `缓存未命中: ${cacheKey}`);
  return null;
}

// 修改 setupRoutes 函数，添加缓存写入
function setupRoutes(app, diskCache, lruCache) {
  app.use('/', async (req, res, next) => {
    const cacheKey = getCacheKey(req);
    const cacheEnabled = require('./config').CACHE_CONFIG.CACHE_ENABLED;
    
    try {
      // 处理缓存响应（如果启用缓存）
      if (cacheEnabled) {
        const cachedItem = await getCacheItem(cacheKey, diskCache, lruCache);
        if (cachedItem) {
          res.setHeader('Content-Type', cachedItem.contentType);
          if (Buffer.isBuffer(cachedItem.data)) {
            return res.end(cachedItem.data);
          }
          return res.json(cachedItem.data);
        }
      }

      // 处理新请求
      const response = await handleRequestWithWorker(req, cacheKey);
      
      if (!response.contentType) {
        throw new Error('无效的Content-Type');
      }

      // 根据内容类型处理响应数据
      const mimeCategory = response.contentType.split(';')[0].trim().toLowerCase();
      let responseData;

      if (mimeCategory.includes('application/json')) {
        // JSON 数据处理
        responseData = typeof response.data === 'string' ? 
          JSON.parse(response.data) : response.data;
      } else if (mimeCategory.startsWith('image/')) {
        // 图片数据处理
        responseData = Buffer.isBuffer(response.data) ? 
          response.data : Buffer.from(response.data);
      } else if (mimeCategory.startsWith('text/')) {
        // 文本数据处理
        responseData = typeof response.data === 'string' ? 
          response.data : response.data.toString('utf-8');
      } else {
        // 其他类型数据处理
        responseData = Buffer.isBuffer(response.data) ? 
          response.data : Buffer.from(response.data);
      }

      // 验证响应
      const isValid = validateResponse(responseData, response.contentType, UPSTREAM_TYPE);
      if (!isValid) {
        throw new Error('响应验证失败');
      }

      // 保存缓存（如果启用缓存）
      if (cacheEnabled) {
        const cacheItem = {
          data: responseData,
          contentType: response.contentType,
          timestamp: Date.now()
        };
        
        try {
          await diskCache.set(cacheKey, cacheItem, response.contentType);
          lruCache.set(cacheKey, cacheItem, response.contentType);
          console.log(global.LOG_PREFIX.CACHE.INFO, `缓存已保存: ${cacheKey}`);
        } catch (error) {
          console.error(global.LOG_PREFIX.ERROR, `缓存写入失败: ${error.message}`);
        }
      }

      // 发送响应
      res.setHeader('Content-Type', response.contentType);
      if (Buffer.isBuffer(responseData)) {
        res.end(responseData);
      } else if (typeof responseData === 'string') {
        res.send(responseData);
      } else {
        res.json(responseData);
      }
      
    } catch (error) {
      console.error(global.LOG_PREFIX.ERROR, `请求处理错误: ${error.message}`);
      next(error);
    }
  });
}

// 使用工作线程处理请求
async function handleRequestWithWorker(req, cacheKey) {
  const workerId = Math.floor(Math.random() * NUM_WORKERS);
  const worker = workers.get(workerId);

  if (!worker) {
    throw new Error('没有可用的工作线程');
  }

  return new Promise((resolve, reject) => {
    const timeoutId = setTimeout(() => {
      reject(new Error('工作线程响应超时'));
    }, REQUEST_TIMEOUT);

    const messageHandler = (message) => {
      if (message.requestId === cacheKey) {
        clearTimeout(timeoutId);
        worker.removeListener('message', messageHandler);
        
        if (message.error) {
          reject(new Error(message.error));
        } else {
          resolve(message.response);
        }
      }
    };

    worker.on('message', messageHandler);
    
    // 提取重要的请求头
    const headers = {};
    if (req.headers.authorization) {
      headers.authorization = req.headers.authorization;
    }
    if (req.headers['content-type']) {
      headers['content-type'] = req.headers['content-type'];
    }
    if (req.headers.accept) {
      headers.accept = req.headers.accept;
    }
    if (req.headers['user-agent']) {
      headers['user-agent'] = req.headers['user-agent'];
    }
    
    worker.postMessage({
      type: 'request',
      requestId: cacheKey,
      url: req.originalUrl,
      headers: headers
    });
  });
}

// 只在主线程中启动服务器
if (isMainThread) {
  startServer().catch(error => {
    console.error('[ 错误 ]', `服务器启动失败: ${error.message}`);
    process.exit(1);
  });
}

module.exports = {
  startServer,
  handleRequestWithWorker
};

