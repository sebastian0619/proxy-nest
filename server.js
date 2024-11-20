const express = require('express');
const morgan = require('morgan');
const { Worker, isMainThread } = require('worker_threads');
const path = require('path');

// 从 utils 导入实际需要的函数
const {
  initializeLogPrefix,
  initializeCache,
  getCacheKey,
  processWeightUpdateQueue,
  startHealthCheck,
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
  MAX_SERVER_SWITCHES
} = require('./config');

// 全局变量
let LOG_PREFIX;
const workers = new Map();
const weightUpdateQueue = [];
let upstreamServers;

// 主服务器启动函数
async function startServer() {
  try {
    // 初始化日志前缀并立即赋值给全局变量
    global.LOG_PREFIX = await initializeLogPrefix();
    
    // 初始化 Express 应用
    const app = express();
    app.use(morgan('combined'));

    // 初始化缓存
    const { diskCache, lruCache } = await initializeCache();

    // 初始化工作线程池时传入 LOG_PREFIX
    await initializeWorkerPool({
      LOG_PREFIX: global.LOG_PREFIX
    });

    // 设置路由
    setupRoutes(app, diskCache, lruCache);

    // 启动服务器
    app.listen(PORT, () => {
      console.log(LOG_PREFIX.SUCCESS, `服务器启动在端口 ${PORT}`);
    });

    // 初始化上游服务器列表
    upstreamServers = process.env.UPSTREAM_SERVERS.split(',').map(url => ({
      url: url.trim(),
      healthy: true,
      baseWeight: 1,
      dynamicWeight: 1,
      alpha: ALPHA_INITIAL,
      responseTimes: [],
      responseTime: Infinity,
    }));

    // 启动健康检查
    const healthCheckConfig = {
      BASE_WEIGHT_UPDATE_INTERVAL: 300000, // 5分钟
      REQUEST_TIMEOUT,
      UPSTREAM_TYPE,
      TMDB_API_KEY,
      TMDB_IMAGE_TEST_URL,
      BASE_WEIGHT_MULTIPLIER
    };

    startHealthCheck(upstreamServers, healthCheckConfig, LOG_PREFIX);

    // 启动权重更新处理
    setInterval(() => {
      processWeightUpdateQueue(
        weightUpdateQueue, 
        upstreamServers, 
        LOG_PREFIX, 
        ALPHA_ADJUSTMENT_STEP,
        BASE_WEIGHT_MULTIPLIER
      );
    }, 1000);

  } catch (error) {
    console.error(LOG_PREFIX?.ERROR || '[ 错误 ]', `服务器启动失败: ${error.message}`);
    process.exit(1);
  }
}

// 初始化工作线程池
async function initializeWorkerPool() {
  console.log(LOG_PREFIX.INFO, `初始化 ${NUM_WORKERS} 个工作线程`);
  
  for (let i = 0; i < NUM_WORKERS; i++) {
    await initializeWorker(i);
  }
}

// 初始化单个工作线程
async function initializeWorker(workerId) {
  const worker = new Worker('./worker.js', {
    workerData: {
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
      ALPHA_ADJUSTMENT_STEP
    }
  });

  setupWorkerEventHandlers(worker, workerId);
  workers.set(workerId, worker);
}

// 设置工作线程事件处理器
function setupWorkerEventHandlers(worker, workerId) {
  worker.on('message', (message) => {
    if (message && message.type === 'weight_update' && message.data) {
      weightUpdateQueue.push(message.data);
    }
  });

  worker.on('error', (error) => {
    console.error(LOG_PREFIX.ERROR, `工作线程 ${workerId} 错误:`, error);
  });

  worker.on('exit', (code) => {
    if (code !== 0) {
      console.error(LOG_PREFIX.ERROR, `工作线程 ${workerId} 异常退出，代码: ${code}`);
      setTimeout(() => initializeWorker(workerId), 1000);
    }
  });
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
        console.log(LOG_PREFIX.CACHE.HIT, `内存缓存命中: ${cacheKey}`);
        return cachedItem;
      }
    } catch (error) {
      console.error(LOG_PREFIX.ERROR, `缓存验证失败: ${error.message}`);
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
        console.log(LOG_PREFIX.CACHE.HIT, `磁盘缓存命中: ${cacheKey}`);
        lruCache.set(cacheKey, cachedItem);
        return cachedItem;
      }
    }
  } catch (error) {
    console.error(LOG_PREFIX.ERROR, `读取缓存失败: ${error.message}`);
  }

  console.log(LOG_PREFIX.CACHE.MISS, `缓存未命中: ${cacheKey}`);
  return null;
}

// 修改 setupRoutes 函数，添加缓存写入
function setupRoutes(app, diskCache, lruCache) {
  app.use('/', async (req, res, next) => {
    const cacheKey = getCacheKey(req);
    
    try {
      // 处理缓存响应
      const cachedItem = await getCacheItem(cacheKey, diskCache, lruCache);
      if (cachedItem) {
        res.setHeader('Content-Type', cachedItem.contentType);
        if (Buffer.isBuffer(cachedItem.data)) {
          return res.end(cachedItem.data);
        }
        return res.json(cachedItem.data);
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

      // 保存缓存
      const cacheItem = {
        data: responseData,
        contentType: response.contentType,
        timestamp: Date.now()
      };
      
      try {
        await diskCache.set(cacheKey, cacheItem);
        lruCache.set(cacheKey, cacheItem);
        console.log(LOG_PREFIX.CACHE.INFO, `缓存已保存: ${cacheKey}`);
      } catch (error) {
        console.error(LOG_PREFIX.ERROR, `缓存写入失败: ${error.message}`);
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
      console.error(LOG_PREFIX.ERROR, `请求处理错误: ${error.message}`);
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
    worker.postMessage({
      type: 'request',
      requestId: cacheKey,
      url: req.originalUrl
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

