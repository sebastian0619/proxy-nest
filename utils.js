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
  
  if (!config.CACHE_CONFIG.CACHE_DIR) {
    throw new Error('CACHE_DIR 未在配置中定义');
  }

  const diskCache = new Map();
  const lruCache = new LRUCache(config.CACHE_CONFIG.MEMORY_CACHE_SIZE || 100);

  try {
    // 确保缓存目录存在
    await fs.mkdir(config.CACHE_CONFIG.CACHE_DIR, { recursive: true });
    const indexPath = path.join(config.CACHE_CONFIG.CACHE_DIR, config.CACHE_CONFIG.CACHE_INDEX_FILE);
    
    try {
      const indexData = await fs.readFile(indexPath);
      const index = JSON.parse(indexData);
      for (const [key, meta] of Object.entries(index)) {
        diskCache.set(key, meta);
      }
    } catch (error) {
      // 如果索引文件不存在或损坏，创建新的
      await fs.writeFile(indexPath, JSON.stringify({}, null, 2));
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
  // 确保参数有效
  const safeMultiplier = typeof BASE_WEIGHT_MULTIPLIER === 'number' && BASE_WEIGHT_MULTIPLIER > 0 
    ? BASE_WEIGHT_MULTIPLIER 
    : 20; // 默认值改为20
  
  // 确保响应时间至少为1ms，避免除零
  const weight = Math.floor((1000 / Math.max(responseTime, 1)) * (safeMultiplier / 100));
  return Math.min(Math.max(1, weight), 100);
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
 * @param {Buffer|string|object} response - 响应内容
 * @param {string} contentType - 内容类型
 * @param {string} upstreamType - 上游类型
 * @returns {boolean} - 验证结果
 */
function validateResponse(data, contentType, upstreamType) {
  if (!data || !contentType) {
    console.error(global.LOG_PREFIX.ERROR, '无效的响应: 缺少数据或Content-Type');
    return false;
  }

  // 通用的 MIME 类型检查
  const mimeCategory = contentType.split(';')[0].trim().toLowerCase();

  switch (upstreamType) {
    case 'tmdb-api':
      // API 响应验证
      if (!mimeCategory.includes('application/json')) {
        console.error(global.LOG_PREFIX.ERROR, `API响应类型错误: ${contentType}`);
        return false;
      }
      try {
        if (typeof data === 'object' && data !== null) {
          return true;
        }
        if (Buffer.isBuffer(data) || typeof data === 'string') {
          JSON.parse(typeof data === 'string' ? data : data.toString('utf-8'));
          return true;
        }
        return false;
      } catch (error) {
        console.error(global.LOG_PREFIX.ERROR, `JSON解析失败: ${error.message}`);
        return false;
      }

    case 'tmdb-image':
      // 图片响应验证 - 只验证基本类型和非空
      if (!mimeCategory.startsWith('image/')) {
        console.error(global.LOG_PREFIX.ERROR, `图片响应类型错误: ${contentType}`);
        return false;
      }
      // 接受 Buffer 或其他非空数据
      return data && (Buffer.isBuffer(data) || data.length > 0);

    default:
      // 默认验证 - 确保数据非空
      return data && (
        Buffer.isBuffer(data) || 
        typeof data === 'string' || 
        typeof data === 'object'
      );
  }
}

// 添加重试请求函数
async function tryRequestWithRetries(server, url, config, LOG_PREFIX) {
  let retryCount = 0;
  
  while (retryCount < 3) {
    try {
      const requestUrl = `${server.url}${url}`;
      console.log(LOG_PREFIX.INFO, `请求: ${requestUrl}`);
      
      const startTime = Date.now();
      const proxyRes = await axios.get(requestUrl, {
        timeout: config.REQUEST_TIMEOUT,
        responseType: 'arraybuffer'  // 默认使用二进制数据
      });
      
      const responseTime = Date.now() - startTime;

      // 根据不同的上游类型处理响应
      let responseData;
      let contentType;

      switch (config.UPSTREAM_TYPE) {
        case 'tmdb-api':
          // API 响应需要解析为 JSON
          responseData = JSON.parse(proxyRes.data.toString());
          contentType = 'application/json';
          break;

        case 'tmdb-image':
          // 图片数据直接返回 Buffer
          responseData = proxyRes.data;
          contentType = proxyRes.headers['content-type'];
          break;

        case 'custom':
          // 自定义类型根据 Content-Type 判断
          contentType = config.CUSTOM_CONTENT_TYPE || proxyRes.headers['content-type'];
          if (contentType.includes('json')) {
            responseData = JSON.parse(proxyRes.data.toString());
          } else {
            responseData = proxyRes.data;
          }
          break;

        default:
          throw new Error(`未知的上游类型: ${config.UPSTREAM_TYPE}`);
      }
      
      return {
        data: responseData,
        contentType,
        responseTime
      };
      
    } catch (error) {
      retryCount++;
      console.error(LOG_PREFIX.ERROR, 
        `请求失败 (${retryCount}/3) - ${server.url}: ${error.message}`
      );
      
      if (retryCount === 3) {
        throw error;
      }
      
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
  }
}

// 添加健康检查函数
async function startHealthCheck(servers, config, LOG_PREFIX) {
  const {
    BASE_WEIGHT_UPDATE_INTERVAL,
    REQUEST_TIMEOUT,
    UPSTREAM_TYPE,
    TMDB_API_KEY,
    TMDB_IMAGE_TEST_URL,
    BASE_WEIGHT_MULTIPLIER,
    DYNAMIC_WEIGHT_MULTIPLIER,
    ALPHA_ADJUSTMENT_STEP
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

        // 初始化 EWMA 和 alpha
        if (typeof server.lastEWMA === 'undefined') {
          server.lastEWMA = responseTime;
          server.alpha = 0.5;
        }

        // 使用 EWMA 计算平均响应时间
        const beta = 0.2;
        server.lastEWMA = beta * responseTime + (1 - beta) * server.lastEWMA;

        // 确保配置参数有效
        const safeBaseMultiplier = typeof BASE_WEIGHT_MULTIPLIER === 'number' && BASE_WEIGHT_MULTIPLIER > 0 
          ? BASE_WEIGHT_MULTIPLIER 
          : 0.02;
        const safeDynamicMultiplier = typeof DYNAMIC_WEIGHT_MULTIPLIER === 'number' && DYNAMIC_WEIGHT_MULTIPLIER > 0 
          ? DYNAMIC_WEIGHT_MULTIPLIER 
          : 0.02;
        const safeAlphaStep = typeof ALPHA_ADJUSTMENT_STEP === 'number' ? ALPHA_ADJUSTMENT_STEP : 0.1;

        // 使用函数计算权重
        server.baseWeight = calculateBaseWeight(server.lastEWMA, safeBaseMultiplier);
        server.dynamicWeight = calculateDynamicWeight(server, responseTime);

        // 确保 alpha 有效并调整
        server.alpha = typeof server.alpha === 'number' ? server.alpha : 0.5;
        
        if (responseTime < server.lastEWMA) {
          server.alpha = Math.min(1, server.alpha + safeAlphaStep);
        } else {
          server.alpha = Math.max(0.1, server.alpha - safeAlphaStep);
        }

        // 计算综合权重
        const combinedWeightRaw = server.alpha * server.dynamicWeight + (1 - server.alpha) * server.baseWeight;
        server.combinedWeight = Math.min(100, Math.max(1, Math.floor(combinedWeightRaw)));

        console.log(LOG_PREFIX.SUCCESS, 
          `服务器 ${server.url} 健康检查成功, ` +
          `响应时间: ${responseTime}ms, ` +
          `平均: ${server.lastEWMA.toFixed(2)}ms, ` +
          `基础权重: ${server.baseWeight}, ` +
          `动态权重: ${server.dynamicWeight}, ` +
          `综合权重: ${server.combinedWeight}, ` +
          `alpha: ${server.alpha.toFixed(2)}`
        );
      } catch (error) {
        server.healthy = false;
        server.lastCheck = Date.now();
        server.baseWeight = 0;
        server.dynamicWeight = 0;
        server.combinedWeight = 0;
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
function processWeightUpdateQueue(queue, servers, LOG_PREFIX, ALPHA_ADJUSTMENT_STEP, BASE_WEIGHT_MULTIPLIER, DYNAMIC_WEIGHT_MULTIPLIER) {
  while (queue.length > 0) {
    const update = queue.shift();
    const server = servers.find(s => s.url === update.server.url);
    
    if (server && server.healthy) {
      // 初始化 EWMA
      if (typeof server.lastEWMA === 'undefined') {
        server.lastEWMA = update.responseTime;
      }

      // 使用 EWMA 计算平均响应时间
      const beta = 0.2; // 衰减因子
      server.lastEWMA = beta * update.responseTime + (1 - beta) * server.lastEWMA;

      // 计算动态权重：响应时间越短，权重越大
      const weight = Math.floor(DYNAMIC_WEIGHT_MULTIPLIER * (1000 / Math.max(server.lastEWMA, 1)));
      
      // 确保权重在合理范围内
      server.dynamicWeight = Math.min(Math.max(1, weight), 100);

      // 更新基础权重
      server.baseWeight = calculateBaseWeight(server.lastEWMA, BASE_WEIGHT_MULTIPLIER);
      
      // 调整 alpha 值
      if (update.responseTime < server.lastEWMA) {
        server.alpha = Math.min(1, (server.alpha || 0.5) + ALPHA_ADJUSTMENT_STEP);
      } else {
        server.alpha = Math.max(0.1, (server.alpha || 0.5) - ALPHA_ADJUSTMENT_STEP);
      }

      // 计算综合权重
      const alpha = server.alpha || 0.5;
      server.combinedWeight = Math.max(1, Math.floor(
        alpha * server.dynamicWeight + (1 - alpha) * server.baseWeight
      ));

      // 记录权重更新
      console.log(LOG_PREFIX.WEIGHT,
        `服务器权重更新 - ${server.url}, ` +
        `响应时间: ${update.responseTime}ms, ` +
        `平均: ${server.lastEWMA.toFixed(2)}ms, ` +
        `基础权重: ${server.baseWeight}, ` +
        `动态权重: ${server.dynamicWeight}, ` +
        `综合权重: ${server.combinedWeight}, ` +
        `alpha: ${server.alpha.toFixed(2)}`
      );
    }
  }
}

function calculateCombinedWeight(server) {
  if (!server.healthy) return 0;
  
  const baseWeight = server.baseWeight || 1;
  const dynamicWeight = server.dynamicWeight || 1;
  const alpha = server.alpha || 0.5;
  
  // 使用加权平均计算综合权重
  const combinedWeight = Math.floor(alpha * dynamicWeight + (1 - alpha) * baseWeight);
  return Math.max(1, combinedWeight);
}

// 计算动态权重
function calculateDynamicWeight(server, responseTime) {
  // 确保参数有效
  const safeMultiplier = typeof DYNAMIC_WEIGHT_MULTIPLIER === 'number' && DYNAMIC_WEIGHT_MULTIPLIER > 0 
    ? DYNAMIC_WEIGHT_MULTIPLIER 
    : 50; // 默认值为50
    
  // 更新响应时间记录
  if (!server.responseTimes) {
    server.responseTimes = [];
  }
  server.responseTimes.push(responseTime);
  if (server.responseTimes.length > RECENT_REQUEST_LIMIT) {
    server.responseTimes.shift();
  }
  
  // 如果服务器不健康或者没有足够的响应时间记录
  if (!server.healthy || server.responseTimes.length === 0) {
    return 1;
  }
  
  // 计算加权平均响应时间
  const avgResponseTime = server.responseTimes.reduce((sum, time) => sum + time, 0) / server.responseTimes.length;
  
  // 使用EWMA更新lastEWMA
  if (!server.lastEWMA) {
    server.lastEWMA = avgResponseTime;
  } else {
    server.lastEWMA = EWMA_BETA * server.lastEWMA + (1 - EWMA_BETA) * avgResponseTime;
  }
  
  // 使用对数函数计算基础分数
  const baseScore = 1000 / Math.max(server.lastEWMA, 1);
  // 应用动态权重乘数并使用对数函数平滑
  const weight = Math.floor(Math.log10(baseScore + 1) * safeMultiplier);
  
  // 确保权重在1到100之间
  return Math.min(Math.max(1, weight), 100);
}

const PORT = process.env.PORT || 8080;
const BASE_WEIGHT_MULTIPLIER = parseInt(process.env.BASE_WEIGHT_MULTIPLIER || '20');
const DYNAMIC_WEIGHT_MULTIPLIER = parseInt(process.env.DYNAMIC_WEIGHT_MULTIPLIER || '50');
const REQUEST_TIMEOUT = 5000;
const RECENT_REQUEST_LIMIT = parseInt(process.env.RECENT_REQUEST_LIMIT || '10'); // 扩大记录数以更平滑动态权重
const ALPHA_INITIAL = 0.5; // 初始平滑因子 α
const ALPHA_ADJUSTMENT_STEP = 0.05; // 每次非缓存请求或健康检查调整的 α 增减值
const EWMA_BETA = parseFloat(process.env.EWMA_BETA || '0.8'); // EWMA平滑系数，默认0.8

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