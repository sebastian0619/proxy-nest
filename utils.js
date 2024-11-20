// utils.js

const fs = require('fs').promises;
const path = require('path');
const axios = require('axios');
const crypto = require('crypto');
const url = require('url');

// 添加 LRU 缓存类
class LRUCache {
  constructor(capacity) {
    this.capacity = capacity;
    this.cache = new Map();
  }

  get(key) {
    if (!this.cache.has(key)) return null;
    const value = this.cache.get(key);
    this.cache.delete(key);
    this.cache.set(key, value);
    return value;
  }

  set(key, value) {
    if (this.cache.has(key)) {
      this.cache.delete(key);
    } else if (this.cache.size >= this.capacity) {
      const firstKey = this.cache.keys().next().value;
      this.cache.delete(firstKey);
    }
    this.cache.set(key, value);
  }
}

// 初始化缓存
async function initializeCache() {
  const config = require('./config');
  
  if (!config.CACHE_DIR) {
    throw new Error('CACHE_DIR 未在配置中定义');
  }

  const diskCache = new Map();
  const lruCache = new LRUCache(config.MEMORY_CACHE_SIZE || 100);

  try {
    // 确保缓存目录存在
    await fs.mkdir(config.CACHE_DIR, { recursive: true });
    
    const indexPath = path.join(config.CACHE_DIR, 'cache_index.json');
    
    try {
      const indexData = await fs.readFile(indexPath);
      const index = JSON.parse(indexData);
      
      for (const [key, meta] of Object.entries(index)) {
        diskCache.set(key, meta);
      }
      
    } catch (error) {
      // 如果索引文件不存在或损坏，创建新的
      await fs.writeFile(
        indexPath,
        JSON.stringify({}, null, 2)
      );
    }

    return { diskCache, lruCache };
    
  } catch (error) {
    throw new Error(`初始化缓存失败: ${error.message}`);
  }
}

// 初始化日志前缀
async function initializeLogPrefix() {
  const chalkModule = await import('chalk');
  const chalk = chalkModule.default;
  chalk.level = 3;
  
  return {
    INFO: chalk.blue('[ 信息 ]'),
    ERROR: chalk.red('[ 错误 ]'),
    WARN: chalk.yellow('[ 警告 ]'),
    SUCCESS: chalk.green('[ 成功 ]'),
    CACHE: {
      HIT: chalk.green('[ 缓存命中 ]'),
      MISS: chalk.hex('#FFA500')('[ 缓存未命中 ]'),
      INFO: chalk.cyan('[ 缓存信息 ]')
    },
    PROXY: chalk.cyan('[ 代理 ]'),
    WEIGHT: chalk.magenta('[ 权重 ]')
  };
}

// 计算基础权重
function calculateBaseWeight(responseTime, BASE_WEIGHT_MULTIPLIER) {
  return Math.min(
    100, // 最大权重上限为100
    Math.max(1, Math.floor((1000 / responseTime) * BASE_WEIGHT_MULTIPLIER))
  );
}

// 生成缓存键
function getCacheKey(req) {
  const parsedUrl = url.parse(req.originalUrl, true);
  return `${parsedUrl.pathname}?${new URLSearchParams(parsedUrl.query)}`;
}

// 记录错误
function logError(requestUrl, errorCode) {
  errorLogQueue.push({ requestUrl, errorCode, timestamp: Date.now() });
  if (errorLogQueue.length > MAX_ERROR_LOGS) {
    errorLogQueue.shift(); // 保持队列长度
  }
}

// 检查错误是否集中在某些请求上
function isServerError(errorCode) {
  const recentErrors = errorLogQueue.filter(log => log.errorCode === errorCode);
  const uniqueRequests = new Set(recentErrors.map(log => log.requestUrl));
  return uniqueRequests.size > 1; // 如果错误分布在多个请求上，可能服务器问题
}

// 延时函数
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

// 检查服务器健康状态
async function checkServerHealth(server, UPSTREAM_TYPE, TMDB_API_KEY, TMDB_IMAGE_TEST_URL, REQUEST_TIMEOUT, LOG_PREFIX) {
  let testUrl = '';
  
  switch (UPSTREAM_TYPE) {
    case 'tmdb-api':
      if (!TMDB_API_KEY) {
        console.error(LOG_PREFIX.ERROR, 'TMDB_API_KEY 环境变量未设置');
        process.exit(1);
      }
      testUrl = `/3/configuration?api_key=${TMDB_API_KEY}`;
      break;
      
    case 'tmdb-image':
      if (!TMDB_IMAGE_TEST_URL) {
        console.error(LOG_PREFIX.ERROR, 'TMDB_IMAGE_TEST_URL 环境变量未设置');
        process.exit(1);
      }
      testUrl = TMDB_IMAGE_TEST_URL;
      break;
      
    case 'custom':
      testUrl = '/';
      break;
      
    default:
      console.error(LOG_PREFIX.ERROR, `未知的上游类型: ${UPSTREAM_TYPE}`);
      process.exit(1);
  }

  try {
    const start = Date.now();
    const response = await axios.get(`${server.url}${testUrl}`, {
      timeout: REQUEST_TIMEOUT,
    });
    const responseTime = Date.now() - start;
    
    console.log(LOG_PREFIX.SUCCESS, `健康检查成功 - 服务器: ${server.url}, 响应时间: ${responseTime}ms`);
    return responseTime;
  } catch (error) {
    console.error(LOG_PREFIX.ERROR, `健康检查失败 - 服务器: ${server.url}, 错误: ${error.message}`);
    throw error;
  }
}

/**
 * 验证响应内容
 * @param {Buffer|string} response - 响应内容
 * @param {string} contentType - 内容类型
 * @param {string} upstreamType - 上游类型
 * @returns {boolean} - 验证结果
 */
function validateResponse(data, contentType, upstreamType) {
  if (!data || !contentType) {
    return false;
  }

  switch (upstreamType) {
    case 'tmdb-api':
      if (!contentType.includes('application/json')) {
        return false;
      }
      // 验证 JSON 数据结构
      try {
        // 如果已经是对象，直接返回true
        if (typeof data === 'object' && data !== null) {
          return true;
        }
        // 如果是Buffer，先转换为字符串
        if (Buffer.isBuffer(data)) {
          data = data.toString('utf-8');
        }
        // 尝试解析JSON字符串
        JSON.parse(data);
        return true;
      } catch (error) {
        console.error(LOG_PREFIX.ERROR, `Invalid JSON response: ${error.message}`);
        return false;
      }

    case 'tmdb-image':
      return contentType.includes('image/');

    case 'custom':
      return true;

    default:
      console.warn(LOG_PREFIX.WARN, `未知的上游类型: ${upstreamType}`);
      return data.length > 0;
  }
}

// 添加重试请求函数
async function tryRequestWithRetries(server, url, config, LOG_PREFIX) {
  const start = Date.now();
  
  try {
    const response = await axios.get(`${server.url}${url}`, {
      timeout: config.REQUEST_TIMEOUT,
      responseType: 'json'  // 确保返回 JSON 数据
    });
    
    return {
      data: response.data,
      contentType: response.headers['content-type'],
      responseTime: Date.now() - start,
      server
    };
    
  } catch (error) {
    console.error(LOG_PREFIX.ERROR, `请求失败: ${error.message}`);
    throw error;
  }
}

// 添加健康检查函数
async function startHealthCheck(servers, config, LOG_PREFIX) {
  const {
    BASE_WEIGHT_UPDATE_INTERVAL,
    REQUEST_TIMEOUT,
    UPSTREAM_TYPE,
    TMDB_API_KEY,
    TMDB_IMAGE_TEST_URL
  } = config;

  console.log(LOG_PREFIX.INFO, '启动健康检查服务');

  const healthCheck = async () => {
    console.log(LOG_PREFIX.INFO, '执行健康检查...');
    
    for (const server of servers) {
      try {
        let testUrl = '';
        switch (UPSTREAM_TYPE) {
          case 'tmdb-api':
            testUrl = `/3/configuration?api_key=${TMDB_API_KEY}`;
            break;
          case 'tmdb-image':
            testUrl = TMDB_IMAGE_TEST_URL;
            break;
          default:
            testUrl = '/';
        }

        const start = Date.now();
        const response = await axios.get(`${server.url}${testUrl}`, {
          timeout: REQUEST_TIMEOUT
        });
        const responseTime = Date.now() - start;

        server.healthy = true;
        server.lastCheck = Date.now();
        server.lastResponseTime = responseTime;
        server.baseWeight = Math.min(
          100,
          Math.max(1, Math.floor((1000 / responseTime) * config.BASE_WEIGHT_MULTIPLIER))
        );

        console.log(LOG_PREFIX.SUCCESS, 
          `服务器 ${server.url} 健康检查成功, ` +
          `响应时间: ${responseTime}ms, ` +
          `基础权重: ${server.baseWeight}`
        );
      } catch (error) {
        server.healthy = false;
        server.lastCheck = Date.now();
        server.baseWeight = 0;
        console.error(LOG_PREFIX.ERROR, 
          `服务器 ${server.url} 健康检查失败: ${error.message}`
        );
      }
    }
  };

  // 立即执行一次健康检查
  await healthCheck();
  
  // 设置定时执行
  return setInterval(healthCheck, BASE_WEIGHT_UPDATE_INTERVAL);
}

// 处理权重更新队列
function processWeightUpdateQueue(queue, servers, LOG_PREFIX, ALPHA_ADJUSTMENT_STEP, BASE_WEIGHT_MULTIPLIER) {
  while (queue.length > 0) {
    const update = queue.shift();
    const server = servers.find(s => s.url === update.server.url);
    
    if (server) {
      // 更新响应时间记录
      server.responseTimes.push(update.responseTime);
      if (server.responseTimes.length > 10) {
        server.responseTimes.shift();
      }
      
      // 计算平均响应时间
      const avgResponseTime = server.responseTimes.reduce((a, b) => a + b, 0) / server.responseTimes.length;
      server.responseTime = avgResponseTime;
      
      // 更新基础权重
      const newBaseWeight = calculateBaseWeight(avgResponseTime, BASE_WEIGHT_MULTIPLIER);
      server.baseWeight = Math.floor(
        server.baseWeight * (1 - server.alpha) + newBaseWeight * server.alpha
      );
      
      // 调整 alpha 值
      if (update.responseTime < avgResponseTime) {
        server.alpha = Math.min(1, server.alpha + ALPHA_ADJUSTMENT_STEP);
      } else {
        server.alpha = Math.max(0.1, server.alpha - ALPHA_ADJUSTMENT_STEP);
      }
      
      console.log(LOG_PREFIX.INFO, 
        `服务器权重更新 - ${server.url}, ` +
        `响应时间: ${update.responseTime}ms, ` +
        `平均: ${avgResponseTime.toFixed(2)}ms, ` +
        `权重: ${server.baseWeight}, ` +
        `alpha: ${server.alpha.toFixed(2)}`
      );
    }
  }
}

function calculateCombinedWeight(server) {
  if (!server.healthy) return 0;
  
  const baseWeight = server.baseWeight || 0;
  const dynamicWeight = server.dynamicWeight || 0;
  const alpha = server.alpha || ALPHA_INITIAL;
  
  return (alpha * dynamicWeight + (1 - alpha) * baseWeight);
}

module.exports = {
  initializeLogPrefix,
  initializeCache,
  calculateBaseWeight,
  getCacheKey,
  logError,
  isServerError,
  delay,
  checkServerHealth,
  validateResponse,
  tryRequestWithRetries,
  startHealthCheck,
  processWeightUpdateQueue,
  calculateCombinedWeight
};