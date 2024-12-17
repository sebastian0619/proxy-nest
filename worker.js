const { parentPort, workerData } = require('worker_threads');
const axios = require('axios');
const {
  initializeLogPrefix,
  validateResponse,
  tryRequestWithRetries,
  initializeCache,
  getCacheKey
} = require('./utils');

// 健康状态枚举
const HealthStatus = {
  HEALTHY: 'healthy',
  UNHEALTHY: 'unhealthy'
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
  UNHEALTHY_TIMEOUT,
  MAX_ERRORS_BEFORE_UNHEALTHY
} = workerData;

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
      status: HealthStatus.HEALTHY,
      errorCount: 0,
      recoveryTime: 0,
      lastCheckTime: Date.now(),
      alpha: ALPHA_INITIAL,
      responseTimes: [],
      lastResponseTime: 0,
      lastEWMA: 0,
      baseWeight: 1,
      dynamicWeight: 1
    }));

    console.log(global.LOG_PREFIX.SUCCESS, `工作线程 ${workerId} 初始化完成`);
  } catch (error) {
    console.error(global.LOG_PREFIX?.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

// 监听来自主线程的消息
parentPort.on('message', (message) => {
  if (message.type === 'server_health_update') {
    // 更新本地服务器状态
    const server = localUpstreamServers.find(s => s.url === message.data.url);
    if (server) {
      server.status = message.data.status;
      server.baseWeight = message.data.baseWeight;
      server.dynamicWeight = message.data.dynamicWeight;
      server.lastResponseTime = message.data.lastResponseTime;
      server.lastCheckTime = message.data.lastCheckTime;
      server.errorCount = message.data.errorCount;
      server.recoveryTime = message.data.recoveryTime;
    }
  }
});

// 获取健康的服务器列表
function getHealthyServers() {
  return localUpstreamServers.filter(server => server.status === HealthStatus.HEALTHY);
}

// 检查服务器是否健康
function isServerHealthy(server) {
  return server.status === HealthStatus.HEALTHY;
}

function selectUpstreamServer() {
  const availableServers = localUpstreamServers.filter(server => {
    return server.status === HealthStatus.HEALTHY;
  });

  if (availableServers.length === 0) {
    throw new Error('没有可用的上游服务器');
  }

  // 计算总权重
  const totalWeight = availableServers.reduce((sum, server) => {
    return sum + server.dynamicWeight;  // 直接使用health_checker提供的动态权重
  }, 0);

  const random = Math.random() * totalWeight;
  let weightSum = 0;

  for (const server of availableServers) {
    weightSum += server.dynamicWeight;  // 使用动态权重进行选择
    
    if (weightSum > random) {
      console.log(global.LOG_PREFIX.SUCCESS, 
        `选择服务器 ${server.url} [状态=${server.status} ` +
        `基础权重=${server.baseWeight} ` +
        `动态权重=${server.dynamicWeight} ` +
        `综合权重=${server.dynamicWeight} ` +  // 使用动态权重作为综合权重
        `实际权重=${server.dynamicWeight} ` +
        `概率=${(server.dynamicWeight / totalWeight * 100).toFixed(1)}% ` +
        `最近响应=${server.lastResponseTime || 0}ms]`
      );
      return server;
    }
  }

  // 保底返回第一个服务器
  const server = availableServers[0];
  console.log(global.LOG_PREFIX.WARN, 
    `选择服务器 ${server.url} [状态=${server.status} ` +
    `基础权重=${server.baseWeight} ` +
    `动态权重=${server.dynamicWeight} ` +
    `综合权重=${server.dynamicWeight} ` +  // 使用动态权重作为综合权重
    `实际权重=${server.dynamicWeight} ` +
    `概率=${(server.dynamicWeight / totalWeight * 100).toFixed(1)}% ` +
    `最近响应=${server.lastResponseTime || 0}ms]`
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
      // 找到磁盘缓存，检查是否需要加载到内存
      const mimeCategory = cachedResponse.contentType.split(';')[0].trim().toLowerCase();
      const contentTypeConfig = global.cache.lruCache.getContentTypeConfig(mimeCategory);
      
      if (!contentTypeConfig || !contentTypeConfig.skip_memory) {
        global.cache.lruCache.set(cacheKey, cachedResponse, cachedResponse.contentType);
      }
      console.log(global.LOG_PREFIX.CACHE.HIT, `磁盘缓存命中: ${url}`);
      return cachedResponse;
    }
  } else {
    console.log(global.LOG_PREFIX.CACHE.HIT, `内存缓存命中: ${url}`);
    return cachedResponse;
  }

  // 如果没有缓存，发起请求
  try {
    const selectedServer = selectUpstreamServer();
    const result = await tryRequestWithRetries(selectedServer, url, {
      REQUEST_TIMEOUT,
      UPSTREAM_TYPE
    }, global.LOG_PREFIX);
    
    // 发送响应时间给health_checker
    parentPort.postMessage({
      type: 'response_time',
      data: {
        url: selectedServer.url,
        responseTime: result.responseTime,
        timestamp: Date.now()
      }
    });
    
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
      global.cache.lruCache.set(cacheKey, finalResult, result.contentType);
      await global.cache.diskCache.set(cacheKey, finalResult, result.contentType);
      return finalResult;
    }
    
    // 其他类型数据保持原样
    // 写入缓存
    global.cache.lruCache.set(cacheKey, result, result.contentType);
    await global.cache.diskCache.set(cacheKey, result, result.contentType);
    return result;
    
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, 
      `请求失败: ${error.message}`
    );
    throw error;
  }
}
