const { Worker, parentPort, workerData } = require('worker_threads');
const { 
  initializeLogPrefix,
  initializeCache,
  getCacheKey,
  validateAndReturnCachedResponse,
  shouldStoreInMemoryCache,
  storeToCaches,
  tryRequestWithRetries,
  calculateCombinedWeight
} = require('./utils');
const { HealthStatus } = require('./constants');

// 从主线程接收配置
const { workerId, config } = workerData;

// 初始化全局变量
let localUpstreamServers = [];

// 确保在使用前初始化日志前缀
global.LOG_PREFIX = initializeLogPrefix();

// 初始化工作线程
async function initializeWorker() {
  try {
    if (!UPSTREAM_SERVERS) {
      throw new Error('未配置上游服务器');
    }

    console.log(global.LOG_PREFIX.INFO, `工作线程 ${workerId} 开始初始化...`);

    // 初始化缓存
    const { diskCache, lruCache } = await initializeCache();
    global.cache = { diskCache, lruCache };

    // 初始化服务器列表
    localUpstreamServers = UPSTREAM_SERVERS.split(',').map(url => ({
      url: url.trim(),
      status: HealthStatus.WARMING_UP,
      errorCount: 0,
      recoveryTime: 0,
      warmupStartTime: Date.now(),
      warmupRequests: 0,
      lastCheckTime: 0,
      alpha: ALPHA_INITIAL,
      responseTimes: [],
      lastResponseTime: 0,
      lastEWMA: 0,
      baseWeight: 1,
      dynamicWeight: 1
    }));

    console.log(global.LOG_PREFIX.SUCCESS, `工作线程 ${workerId} 初始化完成`);
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

// 监听来自主线程的消息
parentPort.on('message', async (message) => {
  if (message.type === 'request') {
    try {
      const result = await handleRequest(message.url);
      parentPort.postMessage({
        requestId: message.requestId,
        response: {
          data: result.data,
          contentType: result.contentType,
          responseTime: result.responseTime
        }
      });
    } catch (error) {
      console.error(global.LOG_PREFIX.ERROR, `请求处理失败: ${error.message}`);
      parentPort.postMessage({
        requestId: message.requestId,
        error: error.message
      });
    }
  } else if (message.type === 'server_status_update') {
    // 接收主线程发来的服务器状态更新
    const { url, status, baseWeight, dynamicWeight } = message.data;
    const server = localUpstreamServers.find(s => s.url === url);
    if (server) {
      server.status = status;
      server.baseWeight = baseWeight;
      server.dynamicWeight = dynamicWeight;
    }
  }
});

function selectUpstreamServer() {
  const availableServers = localUpstreamServers.filter(server => {
    if (!server || typeof server.url !== 'string') {
      return false;
    }
    
    if (typeof server.baseWeight !== 'number' || typeof server.dynamicWeight !== 'number') {
      return false;
    }

    return server.status === HealthStatus.HEALTHY || 
           (server.status === HealthStatus.WARMING_UP && server.warmupRequests < HEALTH_CHECK_CONFIG.WARMUP_REQUEST_COUNT);
  });

  if (availableServers.length === 0) {
    throw new Error('没有可用的上游服务器');
  }

  // 计算总权重
  const totalWeight = availableServers.reduce((sum, server) => {
    const combinedWeight = calculateCombinedWeight({ 
      baseWeight: server.baseWeight, 
      dynamicWeight: server.dynamicWeight 
    });
    return sum + (server.status === HealthStatus.WARMING_UP ? combinedWeight * 0.2 : combinedWeight);
  }, 0);

  const random = Math.random() * totalWeight;
  let weightSum = 0;

  for (const server of availableServers) {
    const combinedWeight = calculateCombinedWeight({ 
      baseWeight: server.baseWeight, 
      dynamicWeight: server.dynamicWeight 
    });
    const weight = server.status === HealthStatus.WARMING_UP ? combinedWeight * 0.2 : combinedWeight;
    weightSum += weight;
    
    if (weightSum > random) {
      return server;
    }
  }

  return availableServers[0];
}

async function handleRequest(url) {
  try {
    // 检查缓存
    const cacheKey = getCacheKey({ originalUrl: url });
    
    try {
      // 先检查内存缓存
      let cachedResponse = global.cache.lruCache.get(cacheKey);
      if (cachedResponse) {
        return validateAndReturnCachedResponse(cachedResponse, 'memory');
      }
      
      // 再检查磁盘缓存
      cachedResponse = await global.cache.diskCache.get(cacheKey);
      if (cachedResponse) {
        if (shouldStoreInMemoryCache(cachedResponse)) {
          global.cache.lruCache.set(cacheKey, cachedResponse, cachedResponse.contentType);
        }
        return validateAndReturnCachedResponse(cachedResponse, 'disk');
      }
    } catch (error) {
      console.error(global.LOG_PREFIX.CACHE.INFO || '[ 缓存信息 ]', `缓存检查失败: ${error.message}`);
    }

    // 没有缓存命中，处理实际请求
    const selectedServer = selectUpstreamServer();
    const startTime = Date.now();
    
    try {
      const result = await tryRequestWithRetries(selectedServer, url, {
        REQUEST_TIMEOUT: config.REQUEST_TIMEOUT,
        UPSTREAM_TYPE: config.UPSTREAM_TYPE,
        RETRY_CONFIG: config.RETRY_CONFIG,
        LOG_PREFIX: global.LOG_PREFIX
      });

      const responseTime = Date.now() - startTime;
      
      // 发送响应时间到主线程进行权重计算
      parentPort.postMessage({
        type: 'response_time',
        data: {
          url: selectedServer.url,
          responseTime,
          timestamp: Date.now()
        }
      });

      // 存储到缓存
      await storeToCaches(cacheKey, result);
      
      return result;
    } catch (error) {
      // 发送错误信息到主线程
      parentPort.postMessage({
        type: 'server_error',
        data: {
          url: selectedServer.url,
          error: error.message,
          timestamp: Date.now()
        }
      });
      throw error;
    }
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR || '[ 错误 ]', `请求处理错误: ${error.message}`);
    throw error;
  }
}

// 立即初始化工作线程
initializeWorker().catch(error => {
  console.error(global.LOG_PREFIX.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
  process.exit(1);
});

module.exports = {
  handleRequest
};

