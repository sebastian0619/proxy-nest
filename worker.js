const { parentPort, workerData } = require('worker_threads');
const axios = require('axios');
const {
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
  TMDB_IMAGE_TEST_URL,
  HEALTH_CHECK_CONFIG,
  RETRY_CONFIG,
  ERROR_PENALTY_FACTOR,
  ERROR_RECOVERY_FACTOR
} = workerData;

// 使用配置中的常量
const {
  HEALTH_CHECK_INTERVAL,
  MIN_HEALTH_CHECK_INTERVAL,
  MAX_CONSECUTIVE_FAILURES,
  WARMUP_REQUEST_COUNT,
  MAX_WARMUP_ATTEMPTS
} = HEALTH_CHECK_CONFIG;

const {
  MAX_RETRIES,
  RETRY_DELAY_BASE,
  RETRY_DELAY_MAX,
  RETRY_JITTER
} = RETRY_CONFIG;

let localUpstreamServers = [];

// 初始化日志前缀
async function initializeWorkerWithLogs() {
  try {
    // 设置默认的日志前缀
    global.LOG_PREFIX = {
      INFO: '[ 信息 ]',
      ERROR: '[ 错误 ]',
      WARN: '[ 警告 ]',
      SUCCESS: '[ 成功 ]',
      CACHE: {
        HIT: '[ 缓存命中 ]',
        MISS: '[ 缓存未命中 ]',
        INFO: '[ 缓存信息 ]'
      }
    };
    
    // 尝试初始化带颜色的日志前缀
    try {
      const chalkModule = await import('chalk');
      const chalk = chalkModule.default;
      chalk.level = 3;
      
      global.LOG_PREFIX = {
        INFO: chalk.blue('[ 信息 ]'),
        ERROR: chalk.red('[ 错误 ]'),
        WARN: chalk.yellow('[ 警告 ]'),
        SUCCESS: chalk.green('[ 成功 ]'),
        CACHE: {
          HIT: chalk.green('[ 缓存命中 ]'),
          MISS: chalk.hex('#FFA500')('[ 缓存未命中 ]'),
          INFO: chalk.cyan('[ 缓存信息 ]')
        }
      };
    } catch (error) {
      console.log(global.LOG_PREFIX.WARN, '无法加载 chalk 模块，使用默认日志前缀');
    }
    
    // 确保 LOG_PREFIX 已经初始化
    if (!global.LOG_PREFIX || !global.LOG_PREFIX.ERROR) {
      throw new Error('LOG_PREFIX 初始化失败');
    }
    
    // 然后执行其他初始化
    await initializeWorker();
  } catch (error) {
    // 使用基本的错误前缀，确保错误信息能够输出
    console.error('[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

// 立即调用初始化函数
initializeWorkerWithLogs().catch(error => {
  console.error(global.LOG_PREFIX.ERROR, `工作线程初始化失败: ${error.message}`);
  process.exit(1);
});

// 初始化工作线程
async function initializeWorker() {
  try {
    if (!upstreamServers) {
      throw new Error('未配置上游服务器');
    }

    // 确保 LOG_PREFIX 已经初始化
    if (!global.LOG_PREFIX || !global.LOG_PREFIX.ERROR) {
      throw new Error('LOG_PREFIX 未初始化');
    }

    console.log(global.LOG_PREFIX.INFO, `工作线程 ${workerId} 开始初始化...`);

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

    console.log(global.LOG_PREFIX.SUCCESS, `工作线程 ${workerId} 初始化完成`);

    // 等待一段时间再开始健康检查，确保其他组件都已初始化
    await new Promise(resolve => setTimeout(resolve, 2000));

    // 立即执行一次健康检查
    console.log(global.LOG_PREFIX.INFO, '执行初始健康检查...');
    for (const server of localUpstreamServers) {
      try {
        const responseTime = await checkServerHealth(server, {
          UPSTREAM_TYPE,
          TMDB_API_KEY,
          TMDB_IMAGE_TEST_URL,
          REQUEST_TIMEOUT,
          LOG_PREFIX: global.LOG_PREFIX
        });

        // 更新服务器状态
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
        console.error(global.LOG_PREFIX.ERROR, `初始健康检查失败: ${error.message}`);
        server.status = HealthStatus.UNHEALTHY;
        server.recoveryTime = Date.now() + UNHEALTHY_TIMEOUT;
      }
    }
  } catch (error) {
    console.error(global.LOG_PREFIX?.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

// 服务器健康检查配置
const UNHEALTHY_TIMEOUT = HEALTH_CHECK_CONFIG.UNHEALTHY_TIMEOUT || 900000; // 使用统一的15分钟超时时间
const MAX_ERRORS_BEFORE_UNHEALTHY = HEALTH_CHECK_CONFIG.MAX_CONSECUTIVE_FAILURES; // 使用配置中的值

function markServerUnhealthy(server) {
  server.status = HealthStatus.UNHEALTHY;
  server.recoveryTime = Date.now() + UNHEALTHY_TIMEOUT;
  server.errorCount = 0;
  server.warmupAttempts = 0;
  console.log(global.LOG_PREFIX.WARN, 
    `服务器 ${server.url} 被标记为不健康状态，将在 ${new Date(server.recoveryTime).toLocaleTimeString()} 后尝试恢复`
  );
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
        // 只检查不健康的服务器
        if (server.status === HealthStatus.UNHEALTHY && 
            Date.now() >= server.recoveryTime) {
          
          console.log(global.LOG_PREFIX.INFO, `正在检查服务器健康状态: ${server.url}`);
          
          try {
            const responseTime = await checkServerHealth(server, {
              UPSTREAM_TYPE,
              TMDB_API_KEY,
              TMDB_IMAGE_TEST_URL,
              REQUEST_TIMEOUT,
              LOG_PREFIX: global.LOG_PREFIX
            });
            
            // 更新服务器状态
            server.lastResponseTime = responseTime;
            server.lastEWMA = responseTime;
            server.baseWeight = calculateBaseWeight(responseTime, BASE_WEIGHT_MULTIPLIER);
            server.dynamicWeight = calculateDynamicWeight(responseTime, DYNAMIC_WEIGHT_MULTIPLIER);
            server.status = HealthStatus.WARMING_UP;
            server.warmupStartTime = Date.now();
            server.warmupRequests = 0;
            server.warmupAttempts = 0;
            server.errorCount = 0;
            
            console.log(global.LOG_PREFIX.SUCCESS, 
              `服务器 ${server.url} 健康检查成功，进入预热阶段 [响应时间=${responseTime}ms, 基础权重=${server.baseWeight}, 动态权重=${server.dynamicWeight}]`
            );
          } catch (error) {
            server.warmupAttempts = (server.warmupAttempts || 0) + 1;
            if (server.warmupAttempts >= HEALTH_CHECK_CONFIG.MAX_WARMUP_ATTEMPTS) {
              console.error(global.LOG_PREFIX.ERROR, 
                `服务器 ${server.url} 预热失败次数过多，将继续保持不健康状态: ${error.message}`
              );
              server.recoveryTime = Date.now() + UNHEALTHY_TIMEOUT;
              server.warmupAttempts = 0;
            } else {
              console.error(global.LOG_PREFIX.ERROR, 
                `服务器 ${server.url} 健康检查失败，这是第 ${server.warmupAttempts} 次尝试: ${error.message}`
              );
              server.recoveryTime = Date.now() + (UNHEALTHY_TIMEOUT / 2);
            }
          }
        }
      } catch (error) {
        console.error(global.LOG_PREFIX.ERROR, `健康检查过程出错 ${server.url}: ${error.message}`);
      }
    }
  };

  // 每30秒执行一次健康检查
  setInterval(healthCheck, HEALTH_CHECK_CONFIG.HEALTH_CHECK_INTERVAL);
  
  // 立即执行一次健康检查
  healthCheck().catch(error => {
    console.error(global.LOG_PREFIX.ERROR, `初始健康检查失败: ${error.message}`);
  });
}

function selectUpstreamServer() {
  const availableServers = localUpstreamServers.filter(server => {
    // 检查服务器是否有基本的必要属性
    if (!server || typeof server.url !== 'string') {
      return false;
    }
    
    // 如果服务器还没有权重数据，不参与选择
    if (typeof server.baseWeight !== 'number' || typeof server.dynamicWeight !== 'number') {
      return false;
    }

    // 检查服务器状态
    return isServerHealthy(server) || 
           (server.status === HealthStatus.WARMING_UP && 
            server.warmupRequests < 10);
  });

  if (availableServers.length === 0) {
    throw new Error('没有可用的上游服务器');
  }

  // 计算权重
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
      // 使用已有的响应时��数据
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
  
  try {
    // 先检查内存缓存
    let cachedResponse = global.cache.lruCache.get(cacheKey);
    
    if (cachedResponse) {
      console.log(global.LOG_PREFIX.CACHE.HIT, `内存缓存命中: ${url}`);
      return validateAndReturnCachedResponse(cachedResponse, 'memory');
    }
    
    // 再检查磁盘缓存
    cachedResponse = await global.cache.diskCache.get(cacheKey);
    if (cachedResponse) {
      console.log(global.LOG_PREFIX.CACHE.HIT, `磁盘缓存命中: ${url}`);
      
      // 检查是否需要加载到内存缓存
      const shouldCacheInMemory = shouldStoreInMemoryCache(cachedResponse);
      if (shouldCacheInMemory) {
        global.cache.lruCache.set(cacheKey, cachedResponse, cachedResponse.contentType);
      }
      
      return validateAndReturnCachedResponse(cachedResponse, 'disk');
    }
  } catch (error) {
    console.error(global.LOG_PREFIX.CACHE.INFO, `缓存检查失败: ${error.message}`);
    // 缓存错误不影响主流程，继续处理请求
  }

  // 没有缓存命中，处理实际请求
  const selectedServer = selectUpstreamServer();
  
  try {
    const result = await tryRequestWithRetries(selectedServer, url, {
      REQUEST_TIMEOUT,
      UPSTREAM_TYPE,
      RETRY_CONFIG
    });
    
    // 验证响应
    if (!validateResponse(result.data, result.contentType, UPSTREAM_TYPE)) {
      throw new Error('Invalid response from upstream server');
    }
    
    // 更新预热状态
    if (selectedServer.status === HealthStatus.WARMING_UP) {
      selectedServer.warmupRequests++;
      if (selectedServer.warmupRequests >= HEALTH_CHECK_CONFIG.WARMUP_REQUEST_COUNT) {
        selectedServer.status = HealthStatus.HEALTHY;
        selectedServer.errorCount = 0;
        selectedServer.warmupAttempts = 0;
        console.log(global.LOG_PREFIX.SUCCESS, `服务器 ${selectedServer.url} 预热完成，恢复正常服务`);
      }
    }
    
    // 处理响应数据
    const finalResult = await processResponse(result);
    
    // 存储到缓存
    await storeToCaches(cacheKey, finalResult);
    
    return finalResult;
  } catch (error) {
    // 更新服务器错误计数
    selectedServer.errorCount = (selectedServer.errorCount || 0) + 1;
    
    // 检查是否需要标记为不健康
    if (selectedServer.errorCount >= HEALTH_CHECK_CONFIG.MAX_CONSECUTIVE_FAILURES) {
      markServerUnhealthy(selectedServer);
    }
    
    // 记录错误并重新抛出
    console.error(global.LOG_PREFIX.ERROR, 
      `服务器 ${selectedServer.url} 请求失败: ${error.message}`
    );
    throw error;
  }
}

function validateAndReturnCachedResponse(cachedResponse, source) {
  try {
    // 验证缓存的响应是否完整
    if (!cachedResponse || !cachedResponse.data || !cachedResponse.contentType) {
      throw new Error(`无效的缓存响应 (${source})`);
    }
    
    // 对于图片类型，确保数据是Buffer
    if (cachedResponse.contentType.startsWith('image/') && !Buffer.isBuffer(cachedResponse.data)) {
      cachedResponse.data = Buffer.from(cachedResponse.data);
    }
    
    return cachedResponse;
  } catch (error) {
    console.error(global.LOG_PREFIX.CACHE.INFO, `缓存验证失败 (${source}): ${error.message}`);
    throw error;
  }
}

function shouldStoreInMemoryCache(response) {
  const mimeCategory = response.contentType.split(';')[0].trim().toLowerCase();
  const contentTypeConfig = global.cache.lruCache.getContentTypeConfig(mimeCategory);
  return !contentTypeConfig || !contentTypeConfig.skip_memory;
}

async function processResponse(result) {
  // 确保图片数据以Buffer形式返回
  if (result.contentType.startsWith('image/')) {
    return {
      data: Buffer.from(result.data),
      contentType: result.contentType,
      responseTime: result.responseTime
    };
  }
  
  // 其他类型数据保持原样
  return result;
}

async function storeToCaches(cacheKey, result) {
  try {
    // 存储到内存缓存
    if (shouldStoreInMemoryCache(result)) {
      global.cache.lruCache.set(cacheKey, result, result.contentType);
    }
    
    // 存储到磁盘缓存
    await global.cache.diskCache.set(cacheKey, result, result.contentType);
  } catch (error) {
    console.error(global.LOG_PREFIX.CACHE.INFO, `缓存存储失败: ${error.message}`);
    // 缓存错误不影响主流程
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

// 健康检查相关配置
// const HEALTH_CHECK_INTERVAL = 30000; // 30秒
// const MIN_HEALTH_CHECK_INTERVAL = 5000; // 最小健康检查间隔
// const MAX_CONSECUTIVE_FAILURES = 3; // 连续失败次数阈值

function updateServerMetrics(server, responseTime) {
  // 更新响应时间历史
  if (!Array.isArray(server.responseTimes)) {
    server.responseTimes = [];
  }
  server.responseTimes.push(responseTime);
  if (server.responseTimes.length > 10) {
    server.responseTimes.shift();
  }

  // 更新EWMA
  if (typeof server.lastEWMA === 'undefined') {
    server.lastEWMA = responseTime;
  } else {
    const alpha = 0.2; // EWMA衰减因子
    server.lastEWMA = alpha * responseTime + (1 - alpha) * server.lastEWMA;
  }

  // 更新权重
  server.baseWeight = calculateBaseWeight(server.lastEWMA, BASE_WEIGHT_MULTIPLIER);
  server.dynamicWeight = calculateDynamicWeight(responseTime, DYNAMIC_WEIGHT_MULTIPLIER);
}

function handleHealthCheckFailure(server, error) {
  server.consecutiveFailures = (server.consecutiveFailures || 0) + 1;
  
  if (server.consecutiveFailures >= HEALTH_CHECK_CONFIG.MAX_CONSECUTIVE_FAILURES) {
    markServerUnhealthy(server);
  }
  
  console.error(global.LOG_PREFIX.ERROR,
    `服务器 ${server.url} 健康检查失败 (${server.consecutiveFailures}/${HEALTH_CHECK_CONFIG.MAX_CONSECUTIVE_FAILURES}): ${error.message}`
  );
}

module.exports = {
  startActiveHealthCheck,
  handleRequest
};

