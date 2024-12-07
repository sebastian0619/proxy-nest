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
  MAX_SERVER_SWITCHES,
  CACHE_CONFIG,
  HEALTH_CHECK_CONFIG,
  RETRY_CONFIG,
  ERROR_PENALTY_FACTOR,
  ERROR_RECOVERY_FACTOR
} = require('./config');

const {
  CACHE_DIR,
  CACHE_INDEX_FILE,
  CACHE_TTL,
  MEMORY_CACHE_SIZE,
  CACHE_MAX_SIZE,
  CACHE_CLEANUP_INTERVAL,
  CACHE_FILE_EXT
} = CACHE_CONFIG;

// 全局变量

const workers = new Map();
const weightUpdateQueue = [];
let upstreamServers;

// 主服务器启动函数
async function startServer() {
  try {
    // 先初始化日志前缀
    global.LOG_PREFIX = await initializeLogPrefix();
    if (!global.LOG_PREFIX) {
      throw new Error('初始化日志前缀失败');
    }

    // 确保 LOG_PREFIX 已经初始化后再继续
    console.log(global.LOG_PREFIX.INFO, '日志系统初始化成功');
    
    // 初始化 Express 应用
    const app = express();
    app.use(morgan('combined'));

    // 初始化缓存
    const { diskCache, lruCache } = await initializeCache();

    // 初始化工作线程池
    await initializeWorkerPool({
      LOG_PREFIX: global.LOG_PREFIX,
      diskCache,
      lruCache
    });

    // 设置路由
    setupRoutes(app, diskCache, lruCache);

    // 启动服务器
    app.listen(PORT, () => {
      console.log(global.LOG_PREFIX.SUCCESS, `服务器启动在端口 ${PORT}`);
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
      BASE_WEIGHT_UPDATE_INTERVAL: 900000, // 5分钟
      REQUEST_TIMEOUT,
      UPSTREAM_TYPE,
      TMDB_API_KEY,
      TMDB_IMAGE_TEST_URL,
      BASE_WEIGHT_MULTIPLIER
    };

    startHealthCheck(upstreamServers, healthCheckConfig, global.LOG_PREFIX);

    // 启动权重更新处理
    setInterval(() => {
      processWeightUpdateQueue(
        weightUpdateQueue, 
        upstreamServers, 
        global.LOG_PREFIX, 
        ALPHA_ADJUSTMENT_STEP,
        BASE_WEIGHT_MULTIPLIER
      );
    }, 1000);

  } catch (error) {
    console.error(global.LOG_PREFIX?.ERROR || '[ 错误 ]', `服务器启动失败: ${error.message}`);
    process.exit(1);
  }
}

// 工作线程管理
async function initializeWorkerPool(config) {
  try {
    for (let i = 0; i < NUM_WORKERS; i++) {
      const worker = new Worker(path.join(__dirname, 'worker.js'), {
        workerData: {
          workerId: i,
          UPSTREAM_TYPE,
          TMDB_API_KEY,
          TMDB_IMAGE_TEST_URL,
          REQUEST_TIMEOUT,
          BASE_WEIGHT_MULTIPLIER,
          DYNAMIC_WEIGHT_MULTIPLIER,
          ALPHA_INITIAL,
          ALPHA_ADJUSTMENT_STEP,
          MAX_SERVER_SWITCHES,
          UPSTREAM_SERVERS: process.env.UPSTREAM_SERVERS,
          HEALTH_CHECK_CONFIG,
          RETRY_CONFIG,
          ERROR_PENALTY_FACTOR,
          ERROR_RECOVERY_FACTOR
        }
      });

      // 错误处理
      worker.on('error', (error) => {
        console.error(global.LOG_PREFIX.ERROR, `工作线程 ${i} 发生错误:`, error);
        handleWorkerError(worker, i);
      });

      // 退出处理
      worker.on('exit', (code) => {
        if (code !== 0) {
          console.error(global.LOG_PREFIX.ERROR, `工作线程 ${i} 异常退出，代码: ${code}`);
          handleWorkerExit(i);
        }
      });

      workers.set(i, worker);
    }

    console.log(global.LOG_PREFIX.SUCCESS, `初始化了 ${NUM_WORKERS} 个工作线程`);
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, '初始化工作线程池失败:', error);
    throw error;
  }
}

// 处理工作线程错误
async function handleWorkerError(worker, workerId) {
  try {
    // 终止出错的工作线程
    await worker.terminate();
    
    // 创建新的工作线程
    const newWorker = new Worker(path.join(__dirname, 'worker.js'), {
      workerData: {
        workerId,
        UPSTREAM_TYPE,
        TMDB_API_KEY,
        TMDB_IMAGE_TEST_URL,
        REQUEST_TIMEOUT,
        BASE_WEIGHT_MULTIPLIER,
        DYNAMIC_WEIGHT_MULTIPLIER,
        ALPHA_INITIAL,
        ALPHA_ADJUSTMENT_STEP,
        MAX_SERVER_SWITCHES,
        UPSTREAM_SERVERS: process.env.UPSTREAM_SERVERS,
        HEALTH_CHECK_CONFIG,
        RETRY_CONFIG,
        ERROR_PENALTY_FACTOR,
        ERROR_RECOVERY_FACTOR
      }
    });
    
    // 设置新工作线程的错误处理
    newWorker.on('error', (error) => {
      console.error(global.LOG_PREFIX.ERROR, `新工作线程 ${workerId} 发生错误:`, error);
      handleWorkerError(newWorker, workerId);
    });
    
    newWorker.on('exit', (code) => {
      if (code !== 0) {
        console.error(global.LOG_PREFIX.ERROR, `新工作线程 ${workerId} 异常退出，代码: ${code}`);
        handleWorkerExit(workerId);
      }
    });
    
    // 更新工作线程映射
    workers.set(workerId, newWorker);
    
    console.log(global.LOG_PREFIX.SUCCESS, `工作线程 ${workerId} 已重新创建`);
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, `重新创建工作线程 ${workerId} 失败:`, error);
    handleWorkerRecoveryFailure(workerId);
  }
}

// 处理工作线程退出
function handleWorkerExit(workerId) {
  // 从映射中移除工作线程
  workers.delete(workerId);
  
  // 尝试重新创建工作线程
  setTimeout(() => {
    try {
      const newWorker = new Worker(path.join(__dirname, 'worker.js'), {
        workerData: {
          workerId,
          UPSTREAM_TYPE,
          TMDB_API_KEY,
          TMDB_IMAGE_TEST_URL,
          REQUEST_TIMEOUT,
          BASE_WEIGHT_MULTIPLIER,
          DYNAMIC_WEIGHT_MULTIPLIER,
          ALPHA_INITIAL,
          ALPHA_ADJUSTMENT_STEP,
          MAX_SERVER_SWITCHES,
          UPSTREAM_SERVERS: process.env.UPSTREAM_SERVERS,
          HEALTH_CHECK_CONFIG,
          RETRY_CONFIG,
          ERROR_PENALTY_FACTOR,
          ERROR_RECOVERY_FACTOR
        }
      });
      
      workers.set(workerId, newWorker);
      console.log(global.LOG_PREFIX.SUCCESS, `工作线程 ${workerId} 已重新创建`);
    } catch (error) {
      console.error(global.LOG_PREFIX.ERROR, `重新创建工作线程 ${workerId} 失败:`, error);
      handleWorkerRecoveryFailure(workerId);
    }
  }, 5000); // 等待5秒后重试
}

// 处理工作线程恢复失败
function handleWorkerRecoveryFailure(workerId) {
  // 记录严重错误
  console.error(global.LOG_PREFIX.ERROR, `工作线程 ${workerId} 恢复失败，系统可能需要重启`);
  
  // 检查是否还有足够的工作线程
  if (workers.size < Math.ceil(NUM_WORKERS / 2)) {
    console.error(global.LOG_PREFIX.ERROR, '可用工作线程数量过低，准备重启服务器...');
    // 这里可以添加重启服务器的逻辑，或者发送告警
    process.exit(1);
  }
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
        const contentTypeConfig = lruCache.getContentTypeConfig(mimeCategory);
        
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

// 设置路由
function setupRoutes(app, diskCache, lruCache) {
  // 健康检查端点
  app.get('/health', (req, res) => {
    const health = {
      status: 'healthy',
      workers: workers.size,
      uptime: process.uptime(),
      memory: process.memoryUsage(),
      cache: {
        memory: lruCache.length,
        disk: diskCache.length
      }
    };
    
    // 检查工作线程数量
    if (workers.size < NUM_WORKERS) {
      health.status = 'degraded';
      health.message = `${NUM_WORKERS - workers.size} 个工作线程未运行`;
    }
    
    res.json(health);
  });

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
        console.log(global.LOG_PREFIX.CACHE.INFO, `缓存已保存: ${cacheKey}`);
      } catch (error) {
        console.error(global.LOG_PREFIX.ERROR, `缓存写入失败: ${error.message}`);
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

