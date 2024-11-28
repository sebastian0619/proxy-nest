const express = require('express');
const morgan = require('morgan');
const { Worker, isMainThread } = require('worker_threads');
const path = require('path');
const fs = require('fs/promises');
const LRUCache = require('lru-cache');
const crypto = require('crypto');

// 默认日志前缀
const DEFAULT_LOG_PREFIX = {
  INFO: '[ 信息 ]',
  ERROR: '[ 错误 ]',
  SUCCESS: '[ 成功 ]',
  WARN: '[ 警告 ]',
  CACHE: {
    HIT: '[ 缓存命中 ]',
    MISS: '[ 缓存未命中 ]'
  }
};

// 从 utils 导入需要的函数
const {
  initializeLogPrefix,
  getCacheKey,
  validateResponse,
  updateServerWeights,
  calculateCombinedWeight,
  initializeCache,
  processWeightUpdateQueue,
  startHealthCheck
} = require('./utils');

// 全局变量
const workers = new Map();
const weightUpdateQueue = [];
let upstreamServers;
let servers = [];

// 主启动函数
async function main() {
  try {
    // 初始化日志系统，使用默认值作为备份
    const logPrefix = initializeLogPrefix() || DEFAULT_LOG_PREFIX;
    global.LOG_PREFIX = {
      ...DEFAULT_LOG_PREFIX,
      ...logPrefix
    };
    
    console.log(global.LOG_PREFIX.INFO, '日志系统初始化成功');

    // 载入配置
    const config = require('./config');

    // 启动服务器
    await startServer(config);

  } catch (error) {
    const errorPrefix = (global.LOG_PREFIX && global.LOG_PREFIX.ERROR) || '[ 错误 ]';
    console.error(errorPrefix, `启动失败: ${error.message}`);
    process.exit(1);
  }
}

// 初始化上游服务器列表
function initializeUpstreamServers() {
  const upstreamUrls = (process.env.UPSTREAM_SERVERS || '').split(',').filter(url => url.trim());
  
  if (upstreamUrls.length === 0) {
    throw new Error('未配置上游服务器');
  }

  servers = upstreamUrls.map(url => ({
    url: url.trim(),
    healthy: true,
    baseWeight: 1,
    dynamicWeight: 1,
    lastEWMA: 0,
    lastCheck: 0,
    errorCount: 0
  }));

  console.log(global.LOG_PREFIX.INFO, `初始化了 ${servers.length} 个上游服务器`);
  return servers;
}

// 主服务器启动函数
async function startServer(config) {
  try {
    // 初始化服务器列表
    servers = initializeUpstreamServers();
    
    // 初始化缓存系统
    const cache = initializeCache(config.cacheConfig);
    global.cache = cache;
    
    // 启动健康检查
    await startHealthCheck(servers, config, global.LOG_PREFIX);
    
    // 启动权重更新队列处理
    setInterval(() => {
      processWeightUpdateQueue(weightUpdateQueue, servers);
    }, config.WEIGHT_UPDATE_INTERVAL);
    
    // 初始化工作线程
    await initializeWorkerPool({
      ...config,
      upstreamServers: process.env.UPSTREAM_SERVERS
    });
    
    console.log(global.LOG_PREFIX.SUCCESS, '服务器启动成功');
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, `服务器启动失败: ${error.message}`);
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
  const upstreamServers = (process.env.UPSTREAM_SERVERS || '').split(',')
    .filter(url => url.trim())
    .map(url => {
      // 找到对应的主服务器对象
      const mainServer = servers.find(s => s.url === url.trim());
      return {
        url: url.trim(),
        healthy: mainServer?.healthy ?? true,
        baseWeight: mainServer?.baseWeight ?? 1,
        dynamicWeight: mainServer?.dynamicWeight ?? 1,
        lastEWMA: mainServer?.lastEWMA ?? 0,
        lastCheck: mainServer?.lastCheck ?? 0,
        errorCount: mainServer?.errorCount ?? 0
      };
    });

  try {
    if (upstreamServers.length === 0) {
      throw new Error('未配置上游服务器');
    }

    const worker = new Worker('./worker.js', {
      workerData: {
        ...workerData,
        workerId,
        upstreamServers: JSON.stringify(upstreamServers) // 传递完整对象
      }
    });

    return worker;
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, `初始化工作线程失败: ${error.message}`);
    throw error;
  }
}

// 设置工作线程事件处理器
function setupWorkerEventHandlers(worker, workerId) {
  worker.on('message', (message) => {
    if (message.type === 'weight_update') {
      const { server, responseTime } = message.data;
      const targetServer = upstreamServers.find(s => s.url === server.url);
      
      if (targetServer && targetServer.healthy) {
        // 使用统一的权重更新函数
        const weights = updateServerWeights(targetServer, responseTime);
        
        console.log(LOG_PREFIX.INFO, 
          `更新服务器 ${targetServer.url} 权重: ` +
          `响应时间=${responseTime}ms, ` +
          `EWMA=${weights.lastEWMA.toFixed(0)}ms, ` +
          `基础权重=${weights.baseWeight}, ` +
          `动态权重=${weights.dynamicWeight}, ` +
          `综合权重=${calculateCombinedWeight(targetServer)}`
        );
      }
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
        lruCache.set(cacheKey, cachedItem);
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
