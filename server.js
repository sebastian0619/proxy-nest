const express = require('express');
const morgan = require('morgan');
const { Worker, isMainThread } = require('worker_threads');
const os = require('os');
const path = require('path');

// 从 utils 导入通用函数和常量
const {
  initializeLogPrefix,
  initializeCache,
  validateResponse,
  getCacheKey,
  processWeightUpdateQueue,
  startHealthCheck,
  LRUCache
} = require('./utils');

// 配置常量
const {
  PORT,
  NUM_WORKERS,
  MEMORY_CACHE_SIZE,
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

// 主服务器启动函数
async function startServer() {
  try {
    // 初始化日志
    LOG_PREFIX = await initializeLogPrefix();
    
    // 初始化 Express 应用
    const app = express();
    app.use(morgan('combined'));

    // 初始化缓存
    const { diskCache, lruCache } = await initializeCache();

    // 初始化工作线程池
    await initializeWorkerPool();

    // 设置路由
    setupRoutes(app, diskCache, lruCache);

    // 启动服务器
    app.listen(PORT, () => {
      console.log(LOG_PREFIX.SUCCESS, `服务器启动在端口 ${PORT}`);
    });

    // 启动健康检查
    startHealthCheck();

    // 启动权重更新处理
    setInterval(processWeightUpdateQueue, 1000);

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
    if (message.type === 'weight_update') {
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

// 设置路由
function setupRoutes(app, diskCache, lruCache) {
  app.use('/', async (req, res, next) => {
    const cacheKey = getCacheKey(req);
    
    try {
      // 检查缓存
      const cachedItem = await getCacheItem(cacheKey, diskCache, lruCache);
      if (cachedItem) {
        res.setHeader('Content-Type', cachedItem.contentType);
        return res.send(cachedItem.data);
      }

      // 选择工作线程处理请求
      const response = await handleRequestWithWorker(req, cacheKey);

      // 设置响应
      res.setHeader('Content-Type', response.contentType);
      res.send(response.data);

    } catch (error) {
      next(error);
    }
  });

  // 错误处理中间件
  app.use((err, req, res, next) => {
    console.error(LOG_PREFIX.ERROR, `请求处理错误: ${err.message}`);
    res.status(502).json({
      error: '代理请求失败',
      message: err.message
    });
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

