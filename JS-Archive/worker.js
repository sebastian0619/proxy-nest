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

// 设置主程序消息处理器
parentPort.on('message', (message) => {
  if (message.type === 'server_health_update') {
    // 更新服务器健康状态
    updateServerHealthStatus(message.data);
  } else if (message.type === 'cache_deleted') {
    // 处理缓存删除通知
    handleCacheDeleted(message.data);
  } else if (message.type === 'healthy_servers_update') {
    // 更新健康服务器列表
    updateHealthyServers(message.data);
  }
});

// 立即调用初始化函数
initializeWorkerWithLogs().catch(error => {
  console.error(global.LOG_PREFIX.ERROR, `工作线程初始化失败: ${error.message}`);
  process.exit(1);
});

// 处理缓存删除通知
function handleCacheDeleted(data) {
  const { cacheKey, deletedBy } = data;
  console.log(global.LOG_PREFIX.INFO, `收到缓存删除通知: ${cacheKey} (由worker ${deletedBy} 删除)`);
  
  // 从本地内存缓存中删除
  if (global.cache && global.cache.lruCache) {
    global.cache.lruCache.delete(cacheKey);
  }
}

// 更新服务器健康状态
function updateServerHealthStatus(healthData) {
  // 更新本地服务器状态
  for (const serverUrl in healthData) {
    const server = localUpstreamServers.find(s => s.url === serverUrl);
    if (server) {
      server.healthStatus = healthData[serverUrl].healthStatus;
      server.lastHealthCheck = healthData[serverUrl].lastHealthCheck;
    }
  }
}

// 更新健康服务器列表
function updateHealthyServers(healthyServers) {
  console.log(global.LOG_PREFIX.INFO, `收到健康服务器列表更新: ${healthyServers.length} 个服务器`);
  
  // 更新本地服务器列表
  localUpstreamServers = healthyServers.map(server => ({
    url: server.url,
    status: server.status || 'healthy',
    errorCount: server.errorCount || 0,
    recoveryTime: server.recoveryTime || 0,
    lastCheckTime: server.lastCheckTime || Date.now(),
    responseTimes: server.responseTimes || [],
    lastResponseTime: server.lastResponseTime || 0,
    baseWeight: server.baseWeight || 1,
    dynamicWeight: server.dynamicWeight || 1
  }));
  
  console.log(global.LOG_PREFIX.INFO, `本地服务器列表已更新`);
}

// 初始化工作线程  
async function initializeWorker() {
  try {
    // 如果没有配置上游服务器，使用默认值
    const servers = upstreamServers || process.env.UPSTREAM_SERVERS || 'https://api.themoviedb.org';
    console.log(global.LOG_PREFIX.INFO, `使用上游服务器: ${servers}`);
    
    if (!servers) {
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
    localUpstreamServers = servers.split(',').map(url => ({
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
parentPort.on('message', async (message) => {
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
  } else if (message.type === 'cache_deleted') {
    // 处理缓存删除通知
    handleCacheDeleted(message.data);
  } else if (message.type === 'request') {
    try {
      const result = await handleRequest(message.url, message.headers);
      
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



async function handleRequest(url, headers = {}) {
  // 获取缓存启用状态
  const cacheEnabled = require('./config').CACHE_CONFIG.CACHE_ENABLED;
  
  // 生成缓存键
  const cacheKey = getCacheKey({ originalUrl: url });
  let cachedResponse = null;
  
  // 如果启用缓存，尝试从缓存获取
  if (cacheEnabled) {
    // 检查内存缓存
    cachedResponse = global.cache.lruCache.get(cacheKey);
    
    // 如果内存没有，检查磁盘缓存
    if (!cachedResponse) {
      cachedResponse = await global.cache.diskCache.get(cacheKey);
      if (cachedResponse) {
        // 验证缓存内容
        try {
          const isValid = validateResponse(
            cachedResponse.data,
            cachedResponse.contentType,
            UPSTREAM_TYPE
          );
          
          if (!isValid) {
            console.error(global.LOG_PREFIX.ERROR, `磁盘缓存验证失败，请求删除缓存: ${url}`);
            // 发送删除请求给主程序，而不是直接删除
            parentPort.postMessage({
              type: 'cache_delete_request',
              data: cacheKey
            });
            cachedResponse = null;
          } else {
            // 找到磁盘缓存，检查是否需要加载到内存
            const mimeCategory = cachedResponse.contentType.split(';')[0].trim().toLowerCase();
            
            // 检查是否有getContentTypeConfig方法
            let contentTypeConfig = null;
            if (global.cache.lruCache && typeof global.cache.lruCache.getContentTypeConfig === 'function') {
              contentTypeConfig = global.cache.lruCache.getContentTypeConfig(mimeCategory);
            }
            
            if (!contentTypeConfig || !contentTypeConfig.skip_memory) {
              global.cache.lruCache.set(cacheKey, cachedResponse, cachedResponse.contentType);
            }
            console.log(global.LOG_PREFIX.CACHE.HIT, `磁盘缓存命中: ${url}`);
            return cachedResponse;
          }
        } catch (error) {
          console.error(global.LOG_PREFIX.ERROR, `缓存验证失败: ${error.message}`);
          // 发送删除请求给主程序，而不是直接删除
          parentPort.postMessage({
            type: 'cache_delete_request',
            data: cacheKey
          });
          cachedResponse = null;
        }
      }
    } else {
      // 验证内存缓存内容
      try {
        const isValid = validateResponse(
          cachedResponse.data,
          cachedResponse.contentType,
          UPSTREAM_TYPE
        );
        
        if (!isValid) {
          console.error(global.LOG_PREFIX.ERROR, `内存缓存验证失败，请求删除缓存: ${url}`);
          // 发送删除请求给主程序，而不是直接删除
          parentPort.postMessage({
            type: 'cache_delete_request',
            data: cacheKey
          });
          cachedResponse = null;
        } else {
          console.log(global.LOG_PREFIX.CACHE.HIT, `内存缓存命中: ${url}`);
          return cachedResponse;
        }
      } catch (error) {
        console.error(global.LOG_PREFIX.ERROR, `缓存验证失败: ${error.message}`);
        // 发送删除请求给主程序，而不是直接删除
        parentPort.postMessage({
          type: 'cache_delete_request',
          data: cacheKey
        });
        cachedResponse = null;
      }
    }
  }

  // 如果没有缓存或缓存验证失败，发起请求
  let selectedServer = null;
  try {
    selectedServer = selectUpstreamServer();
    const result = await tryRequestWithRetries(selectedServer, url, {
      REQUEST_TIMEOUT: UPSTREAM_TYPE === 'tmdb-image' ? (process.env.IMAGE_REQUEST_TIMEOUT || 90000) : REQUEST_TIMEOUT,
      UPSTREAM_TYPE
    }, global.LOG_PREFIX, headers, workerId);
    
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
    const finalResult = result.contentType.startsWith('image/') 
      ? {
          data: (() => {
            try {
              if (Buffer.isBuffer(result.data)) {
                return result.data;
              } else if (typeof result.data === 'string') {
                return Buffer.from(result.data);
              } else if (result.data && typeof result.data === 'object') {
                // 检查常见的图片数据属性
                if (result.data.data && Buffer.isBuffer(result.data.data)) {
                  return result.data.data;
                } else if (result.data.buffer && Buffer.isBuffer(result.data.buffer)) {
                  return result.data.buffer;
                } else if (result.data.toString && typeof result.data.toString === 'function') {
                  const str = result.data.toString();
                  if (str && str.length > 0) {
                    return Buffer.from(str);
                  }
                }
                // 尝试直接转换
                return Buffer.from(result.data);
              } else if (Array.isArray(result.data)) {
                return Buffer.from(result.data);
              } else {
                console.error(global.LOG_PREFIX.ERROR, `无法转换图片数据: ${typeof result.data}, 构造函数: ${result.data?.constructor?.name}`);
                throw new Error(`不支持的图片数据类型: ${typeof result.data}`);
              }
            } catch (error) {
              console.error(global.LOG_PREFIX.ERROR, `图片数据转换失败: ${error.message}`);
              throw error;
            }
          })(),
          contentType: result.contentType,
          responseTime: result.responseTime
        }
      : result;
    
    // 如果启用缓存，写入缓存
    if (cacheEnabled) {
      global.cache.lruCache.set(cacheKey, finalResult, finalResult.contentType);
      await global.cache.diskCache.set(cacheKey, finalResult, finalResult.contentType);
    }
    
    return finalResult;
    
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, 
      `请求失败: ${error.message}`
    );
    
    // 只有在成功选择了服务器的情况下才报告服务器不健康
    if (selectedServer) {
      parentPort.postMessage({
        type: 'unhealthy_server_report',
        data: {
          serverUrl: selectedServer.url,
          errorInfo: {
            message: error.message,
            code: error.code,
            status: error.response?.status,
            timestamp: Date.now()
          }
        }
      });
    }
    
    throw error;
  }
}
