const { parentPort, workerData } = require('worker_threads');
const axios = require('axios');
const {
  initializeLogPrefix,
  validateResponse,
  tryRequestWithRetries,
  calculateBaseWeight,
  calculateDynamicWeight,
  calculateCombinedWeight,
  initializeCache,
  getCacheKey,
  checkServerHealth
} = require('./utils');

// 健康状态枚举
const HealthStatus = {
  HEALTHY: 'healthy',
  UNHEALTHY: 'unhealthy',
  WARMING_UP: 'warming_up'
};

// 从 workerData 中解构需要的配置
const { 
  UPSTREAM_TYPE,
  REQUEST_TIMEOUT,
  ALPHA_INITIAL,
  BASE_WEIGHT_MULTIPLIER,
  DYNAMIC_WEIGHT_MULTIPLIER,
  workerId,
  upstreamServers,
  TMDB_API_KEY,
  TMDB_IMAGE_TEST_URL
} = workerData;

let localUpstreamServers = [];

// 初始化日志前缀
async function initializeWorkerWithLogs() {
  try {
    // 先初始化日志前缀
    global.LOG_PREFIX = await initializeLogPrefix();
    
    // 然后执行其他初始化
    await initializeWorker();
  } catch (error) {
    console.error('[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

// 立即调用初始化函数
initializeWorkerWithLogs().catch(error => {
  console.error('[ 错误 ]', `工作线程初始化失败: ${error.message}`);
  process.exit(1);
});

// 初始化工作线程
async function initializeWorker() {
  try {
    if (!upstreamServers) {
      throw new Error('未配置上游服务器');
    }

    // 初始化缓存
    const { diskCache, lruCache } = await initializeCache();
    global.cache = { diskCache, lruCache };

    // 初始化服务器列表
    localUpstreamServers = upstreamServers.split(',').map(url => ({
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

    // 立即执行一次健康检查
    console.log(global.LOG_PREFIX.INFO, '执行初始健康检查...');
    for (const server of localUpstreamServers) {
      try {
        const startTime = Date.now();
        
        // 根据上游类型执行不同的健康检查
        if (UPSTREAM_TYPE === 'tmdb-api') {
          if (!TMDB_API_KEY) {
            throw new Error('TMDB_API_KEY is required for API servers');
          }
          await checkServerHealth(server, {
            UPSTREAM_TYPE,
            TMDB_API_KEY,
            REQUEST_TIMEOUT,
            LOG_PREFIX: global.LOG_PREFIX
          });
        } else if (UPSTREAM_TYPE === 'tmdb-image') {
          // 图片服务器使用简单的 HEAD 请求检查
          if (!TMDB_IMAGE_TEST_URL) {
            console.log(global.LOG_PREFIX.WARN, 'TMDB_IMAGE_TEST_URL not set, using default test image');
          }
          const testUrl = TMDB_IMAGE_TEST_URL || '/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png';
          const fullUrl = `${server.url}${testUrl}`;
          
          const response = await axios({
            method: 'head',
            url: fullUrl,
            timeout: REQUEST_TIMEOUT,
            validateStatus: status => status === 200
          });
        }
        
        // 计算初始权重
        const responseTime = Date.now() - startTime;
        server.responseTimes = [responseTime];
        server.lastResponseTime = responseTime;
        server.lastEWMA = responseTime;
        server.baseWeight = calculateBaseWeight(responseTime, BASE_WEIGHT_MULTIPLIER);
        server.dynamicWeight = calculateDynamicWeight(responseTime, DYNAMIC_WEIGHT_MULTIPLIER);
        server.status = HealthStatus.HEALTHY;

        console.log(global.LOG_PREFIX.SUCCESS, 
          `服务器 ${server.url} 初始化成功 [响应时间=${responseTime}ms, 基础权重=${server.baseWeight}, 动态权重=${server.dynamicWeight}]`
        );
      } catch (error) {
        console.error(global.LOG_PREFIX.ERROR, 
          `服务器 ${server.url} 初始化失败: ${error.message}`
        );
        server.status = HealthStatus.UNHEALTHY;
        server.recoveryTime = Date.now() + UNHEALTHY_TIMEOUT;
      }
    }

    // 启动定期健康检查
    startActiveHealthCheck();

    console.log(global.LOG_PREFIX.INFO, `工作线程 ${workerId} 初始化完成`);
  } catch (error) {
    console.error(global.LOG_PREFIX?.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

// 服务器健康检查配置
const UNHEALTHY_TIMEOUT = 30000; // 不健康状态持续30秒
const MAX_ERRORS_BEFORE_UNHEALTHY = 3; // 连续3次错误后标记为不健康

function markServerUnhealthy(server) {
  server.status = HealthStatus.UNHEALTHY;
  server.recoveryTime = Date.now() + UNHEALTHY_TIMEOUT;
  server.errorCount = 0;
  console.log(`服务器 ${server.url} 被标记为不健康状态，将在 ${new Date(server.recoveryTime).toLocaleTimeString()} 后恢复`);
}

// 修改函数名为 isServerHealthy
function isServerHealthy(server) {
  // 只返回当前状态，不做状态修改
  return server.status === HealthStatus.HEALTHY;
}

// 添加主动健康检查
async function startActiveHealthCheck() {
  const healthCheck = async () => {
    for (const server of localUpstreamServers) {
      try {
        if (server.status === HealthStatus.UNHEALTHY && 
            Date.now() >= server.recoveryTime) {
          
          console.log(global.LOG_PREFIX.INFO, `正在检查服务器健康状态: ${server.url}`);
          
          try {
            // 记录健康检查开始时间
            const startTime = Date.now();
            await checkServerHealth(server, {
              UPSTREAM_TYPE,
              TMDB_API_KEY,
              TMDB_IMAGE_TEST_URL,
              REQUEST_TIMEOUT,
              LOG_PREFIX: global.LOG_PREFIX
            });
            // 计算响应时间并更新服务器数据
            const responseTime = Date.now() - startTime;
            server.responseTimes.push(responseTime);
            if (server.responseTimes.length > 3) {
              server.responseTimes.shift();
            }
            server.lastResponseTime = responseTime;
            
            // 更新权重
            server.baseWeight = calculateBaseWeight(responseTime, BASE_WEIGHT_MULTIPLIER);
            server.dynamicWeight = calculateDynamicWeight(responseTime, DYNAMIC_WEIGHT_MULTIPLIER);
            
            // 健康检查成功，进入预热状态
            server.status = HealthStatus.WARMING_UP;
            server.warmupStartTime = Date.now();
            server.warmupRequests = 0;
            console.log(global.LOG_PREFIX.INFO, 
              `服务器 ${server.url} 健康检查通过 [响应时间=${responseTime}ms, 基础权重=${server.baseWeight}, 动态权重=${server.dynamicWeight}], 进入预热状态`
            );
          } catch (error) {
            console.error(global.LOG_PREFIX.ERROR, `服务器 ${server.url} 健康检查失败: ${error.message}`);
            server.recoveryTime = Date.now() + (UNHEALTHY_TIMEOUT / 2);
          }
        }
      } catch (error) {
        console.error(global.LOG_PREFIX.ERROR, `健康检查过程出错 ${server.url}: ${error.message}`);
      }
    }
  };

  setInterval(healthCheck, 30000);
  healthCheck().catch(error => {
    console.error(global.LOG_PREFIX.ERROR, `初始健康检查失败: ${error.message}`);
  });
}

function selectUpstreamServer() {
  const availableServers = localUpstreamServers.filter(server => {
    // 如果服务器还没有权重数据，先进行一次健康检查
    if (!server.baseWeight || !server.dynamicWeight) {
      return false;  // 暂时不参与选择
    }
    return isServerHealthy(server) || 
           (server.status === HealthStatus.WARMING_UP && 
            server.warmupRequests < 10);
  });

  if (availableServers.length === 0) {
    throw new Error('没有可用的上游服务器');
  }

  // 计算总权重
  const totalWeight = availableServers.reduce((sum, server) => {
    const baseWeight = server.baseWeight || 1;
    const dynamicWeight = server.dynamicWeight || 1;
    const combinedWeight = calculateCombinedWeight({ baseWeight, dynamicWeight });
    return sum + (server.status === HealthStatus.WARMING_UP ? combinedWeight * 0.2 : combinedWeight);
  }, 0);

  const random = Math.random() * totalWeight;
  let weightSum = 0;

  for (const server of availableServers) {
    const baseWeight = server.baseWeight || 1;
    const dynamicWeight = server.dynamicWeight || 1;
    const combinedWeight = calculateCombinedWeight({ baseWeight, dynamicWeight });
    const weight = server.status === HealthStatus.WARMING_UP ? combinedWeight * 0.2 : combinedWeight;
    weightSum += weight;
    
    if (weightSum > random) {
      // 使用已有的响应时间数据
      const avgResponseTime = server.responseTimes && server.responseTimes.length > 0
        ? (server.responseTimes.reduce((a, b) => a + b, 0) / server.responseTimes.length).toFixed(0)
        : server.lastResponseTime?.toFixed(0) || '未知';
      
      console.log(global.LOG_PREFIX.SUCCESS, 
        `选择服务器 ${server.url} [状态=${server.status} 基础权重=${baseWeight} 动态权重=${dynamicWeight} ` +
        `综合权重=${combinedWeight.toFixed(1)} 实际权重=${weight.toFixed(1)} 概率=${(weight / totalWeight * 100).toFixed(1)}% ` +
        `最近响应=${avgResponseTime}ms]`
      );
      return server;
    }
  }

  // 保底返回第一个服务器
  const server = availableServers[0];
  const baseWeight = server.baseWeight || 1;
  const dynamicWeight = server.dynamicWeight || 1;
  const combinedWeight = calculateCombinedWeight({ baseWeight, dynamicWeight });
  const weight = server.status === HealthStatus.WARMING_UP ? combinedWeight * 0.2 : combinedWeight;
  const avgResponseTime = server.responseTimes && server.responseTimes.length > 0
    ? (server.responseTimes.reduce((a, b) => a + b, 0) / server.responseTimes.length).toFixed(0)
    : server.lastResponseTime?.toFixed(0) || '未知';
    
  console.log(global.LOG_PREFIX.WARN, 
    `保底服务器 ${server.url} [状态=${server.status} 基础权重=${baseWeight} 动态权重=${dynamicWeight} ` +
    `综合权重=${combinedWeight.toFixed(1)} 实际权重=${weight.toFixed(1)} 概率=${(weight / totalWeight * 100).toFixed(1)}% ` +
    `最近响应=${avgResponseTime}ms]`
  );
  return server;
}

parentPort.on('message', async (message) => {
  if (message.type === 'request') {
    try {
      const result = await handleRequest(message.url);
      
      // 确保返回正确的消息格式
      parentPort.postMessage({
        requestId: message.requestId,
        response: {
          data: result.data,
          contentType: result.contentType,
          responseTime: result.responseTime
        }
      });
      
    } catch (error) {
      parentPort.postMessage({
        requestId: message.requestId,
        error: error.message
      });
    }
  }
});

async function handleRequest(url) {
  // 检查缓存
  const cacheKey = getCacheKey({ originalUrl: url });
  let cachedResponse = global.cache.lruCache.get(cacheKey);
  
  // 如果内存没有，检查磁盘缓存
  if (!cachedResponse) {
    cachedResponse = await global.cache.diskCache.get(cacheKey);
    if (cachedResponse) {
      // 找到磁盘缓存，加载到内存
      global.cache.lruCache.set(cacheKey, cachedResponse);
      console.log(global.LOG_PREFIX.CACHE.HIT, `磁盘缓存命中: ${url}`);
      return cachedResponse;
    }
  } else {
    console.log(global.LOG_PREFIX.CACHE.HIT, `内存缓存命中: ${url}`);
    return cachedResponse;
  }

  // 第一阶段：使用权重选择的服务器
  const selectedServer = selectUpstreamServer();
  try {
    const result = await tryRequestWithRetries(selectedServer, url, {
      REQUEST_TIMEOUT,
      UPSTREAM_TYPE
    }, global.LOG_PREFIX);
    
    // 成功后更新服务器权重
    addWeightUpdate(selectedServer, result.responseTime);
    
    // 更新预热状态
    if (selectedServer.status === HealthStatus.WARMING_UP) {
      selectedServer.warmupRequests++;
      if (selectedServer.warmupRequests >= 10) {
        selectedServer.status = HealthStatus.HEALTHY;
        console.log(global.LOG_PREFIX.SUCCESS, `服务器 ${selectedServer.url} 预热完成，恢复正常服务`);
      }
    }
    
    // 验证响应
    try {
      const isValid = validateResponse(
        result.data,
        result.contentType,
        UPSTREAM_TYPE
      );
      
      if (!isValid) {
        throw new Error('Invalid response from upstream server');
      }
    } catch (error) {
      console.error(global.LOG_PREFIX.ERROR, `响应验证失败: ${error.message}`);
      throw error;
    }

    // 确保图片数据以 Buffer 形式返回
    if (result.contentType.startsWith('image/')) {
      const finalResult = {
        data: Buffer.from(result.data),
        contentType: result.contentType,
        responseTime: result.responseTime
      };
      // 写入缓存
      global.cache.lruCache.set(cacheKey, finalResult);
      await global.cache.diskCache.set(cacheKey, finalResult);
      return finalResult;
    }
    
    // 其他类型数据保持原样
    // 写入缓存
    global.cache.lruCache.set(cacheKey, result);
    await global.cache.diskCache.set(cacheKey, result);
    return result;
    
  } catch (error) {
    selectedServer.errorCount++;
    if (selectedServer.errorCount >= MAX_ERRORS_BEFORE_UNHEALTHY) {
      markServerUnhealthy(selectedServer);
    }
    console.log(`初始服务器 ${selectedServer.url} 请求失败或超时，启动并行请求`);
    throw error;
  }
}

function addWeightUpdate(server, responseTime) {
  // 更新响应时间队列
  if (!server.responseTimes) {
    server.responseTimes = [];
  }
  server.responseTimes.push(responseTime);
  if (server.responseTimes.length > 3) {
    server.responseTimes.shift();
  }

  // 计算平均响应时间
  const avgResponseTime = server.responseTimes.length === 3
    ? server.responseTimes.reduce((a, b) => a + b, 0) / 3
    : responseTime;

  // 更新权重
  server.lastResponseTime = responseTime;
  server.lastEWMA = avgResponseTime;
  server.baseWeight = calculateBaseWeight(avgResponseTime, BASE_WEIGHT_MULTIPLIER);
  server.dynamicWeight = calculateDynamicWeight(avgResponseTime, DYNAMIC_WEIGHT_MULTIPLIER);
  
  parentPort.postMessage({
    type: 'weight_update',
    data: {
      server,
      responseTime,
      timestamp: Date.now()
    }
  });
}
