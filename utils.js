// utils.js

const fs = require('fs').promises;
const path = require('path');
const axios = require('axios');
const url = require('url');
const config = require('./config');
const crypto = require('crypto');

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

// 添加 DiskCache 类
class DiskCache {
  constructor(config) {
    this.config = config;
    this.cacheDir = config.CACHE_DIR;
    this.indexPath = path.join(config.CACHE_DIR, config.CACHE_INDEX_FILE);
    this.index = new Map();
    this.lock = new Map(); // 用于并发控制
  }

  async init() {
    try {
      // 确保缓存目录存在
      await fs.mkdir(this.cacheDir, { recursive: true });
      
      // 加载索引文件
      try {
        const indexData = await fs.readFile(this.indexPath, 'utf8');
        const indexObj = JSON.parse(indexData);
        for (const [key, meta] of Object.entries(indexObj)) {
          this.index.set(key, meta);
        }
      } catch (error) {
        // 索引文件不存在或损坏，创建新的
        await this.saveIndex();
      }

      // 启动定期清理
      this.startCleanup();
    } catch (error) {
      throw new Error(`初始化磁盘缓存失败: ${error.message}`);
    }
  }

  async get(key) {
    const meta = this.index.get(key);
    if (!meta) return null;

    // 检查是否过期
    if (Date.now() > meta.expireAt) {
      await this.delete(key);
      return null;
    }

    try {
      const filePath = path.join(this.cacheDir, `${key}${this.config.CACHE_FILE_EXT}`);
      const data = await fs.readFile(filePath);
      return JSON.parse(data);
    } catch (error) {
      console.error(`读取缓存文件失败: ${error.message}`);
      await this.delete(key);
      return null;
    }
  }

  async set(key, value) {
    // 等待任何现有的写操作完成
    while (this.lock.has(key)) {
      await new Promise(resolve => setTimeout(resolve, 10));
    }
    
    this.lock.set(key, true);
    
    try {
      const filePath = path.join(this.cacheDir, `${key}${this.config.CACHE_FILE_EXT}`);
      const meta = {
        key,
        filePath,
        size: JSON.stringify(value).length,
        createdAt: Date.now(),
        expireAt: Date.now() + this.config.CACHE_TTL,
        lastAccessed: Date.now()
      };

      // 写入数据文件
      await fs.writeFile(filePath, JSON.stringify(value));
      
      // 更新索引
      this.index.set(key, meta);
      await this.saveIndex();
      
    } catch (error) {
      console.error(`写入缓存失败: ${error.message}`);
      await this.delete(key);
    } finally {
      this.lock.delete(key);
    }
  }

  async delete(key) {
    const meta = this.index.get(key);
    if (!meta) return;

    try {
      const filePath = path.join(this.cacheDir, `${key}${this.config.CACHE_FILE_EXT}`);
      await fs.unlink(filePath);
    } catch (error) {
      console.error(`删除缓存文件失败: ${error.message}`);
    }

    this.index.delete(key);
    await this.saveIndex();
  }

  async saveIndex() {
    try {
      const indexObj = Object.fromEntries(this.index);
      await fs.writeFile(this.indexPath, JSON.stringify(indexObj, null, 2));
    } catch (error) {
      console.error(`保存索引文件失败: ${error.message}`);
    }
  }

  async cleanup() {
    console.log('开始清理过期缓存...');
    const now = Date.now();
    
    for (const [key, meta] of this.index.entries()) {
      if (now > meta.expireAt) {
        await this.delete(key);
      }
    }

    // 检查缓存大小限制
    if (this.index.size > this.config.CACHE_MAX_SIZE) {
      // 按最后访问时间排序，删除最旧的
      const sortedEntries = [...this.index.entries()]
        .sort((a, b) => a[1].lastAccessed - b[1].lastAccessed);
      
      const entriesToDelete = sortedEntries
        .slice(0, this.index.size - this.config.CACHE_MAX_SIZE);
      
      for (const [key] of entriesToDelete) {
        await this.delete(key);
      }
    }
  }

  startCleanup() {
    setInterval(() => {
      this.cleanup().catch(error => {
        console.error(`缓存清理失败: ${error.message}`);
      });
    }, this.config.CACHE_CLEANUP_INTERVAL);
  }
}

// 修改 initializeCache 函数
async function initializeCache() {
  const diskCache = new DiskCache(config.CACHE_CONFIG);
  await diskCache.init();
  
  const lruCache = new LRUCache(config.CACHE_CONFIG.MEMORY_CACHE_SIZE);
  
  return { diskCache, lruCache };
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

// 初始化服务器状态
function initializeServerState(server) {
  if (!server.state) {
    server.state = {
      baseWeight: 1,
      dynamicWeight: 1,
      lastEWMA: 1000,
      alpha: config.ALPHA_INITIAL,
      responseTimes: [],
      healthy: true,
      lastUpdateTime: Date.now()
    };
  }
  return server.state;
}

// 计算基础权重
function calculateBaseWeight(responseTime, multiplier = 20) {
  try {
    if (!responseTime || isNaN(responseTime) || responseTime <= 0) {
      return 1;
    }
    
    // 基础权重计算：响应时间越短，权重越大
    const weight = Math.floor(multiplier * (1000 / Math.max(responseTime, 1)));
    
    // 确保权重在合理范围内 (1-100)
    return Math.min(Math.max(1, weight), 100);
  } catch (error) {
    console.error('[ 错误 ] 基础权重计算失败:', error);
    return 1;
  }
}

// 计算动态权重
function calculateDynamicWeight(avgResponseTime, multiplier = 50) {
  try {
    // 动态权重计算：响应时间越短，权重越大
    const weight = Math.floor(multiplier * (1000 / Math.max(avgResponseTime, 1)));
    
    // 确保权重在合理范围内 (1-100)
    return Math.min(Math.max(1, weight), 100);
  } catch (error) {
    console.error('[ 错误 ] 动态权重计算失败:', error);
    return 1;
  }
}

// 计算综合权重
function calculateCombinedWeight(server) {
  try {
    if (!server || !server.baseWeight || !server.dynamicWeight) {
      return 1;
    }

    // 使用加权平均计算综合权重
    const alpha = 0.7; // 基础权重的比重
    const combinedWeight = alpha * server.baseWeight + (1 - alpha) * server.dynamicWeight;
    
    // 确保权重在合理范围内
    return Math.min(Math.max(1, Math.floor(combinedWeight)), 100);
  } catch (error) {
    console.error('[ 错误 ] 综合权重计算失败:', error);
    return 1;
  }
}

// 更新服务器状态
function updateServerState(server, responseTime, healthy = true) {
  const state = initializeServerState(server);
  
  // 更新健康状态
  state.healthy = healthy;
  
  if (healthy && responseTime > 0) {
    // 更新响应时间记录，保持最近3次
    state.responseTimes.push(responseTime);
    if (state.responseTimes.length > 3) {
      state.responseTimes.shift();
    }
    
    // 计算最近3次请求的平均响应时间
    if (state.responseTimes.length === 3) {
      const avgResponseTime = state.responseTimes.reduce((a, b) => a + b, 0) / 3;
      // 只更新动态权重
      state.dynamicWeight = calculateDynamicWeight(avgResponseTime, config.DYNAMIC_WEIGHT_MULTIPLIER);
    }
  }
  
  // 更新时间戳
  state.lastUpdateTime = Date.now();
  
  return state;
}

// 生成缓存键
function getCacheKey(req) {
  try {
    const parsedUrl = url.parse(req.originalUrl, true);
    
    // 规范化查询参数
    const sortedParams = new URLSearchParams(parsedUrl.query);
    sortedParams.sort();
    
    // 创建缓存键
    const cacheKey = crypto
      .createHash('md5')
      .update(`${parsedUrl.pathname}?${sortedParams.toString()}`)
      .digest('hex');
      
    return cacheKey;
  } catch (error) {
    console.error(`生成缓存键失败: ${error.message}`);
    // 降级处理：使用原始URL
    return req.originalUrl.replace(/[^a-zA-Z0-9]/g, '_');
  }
}

// 记录错误
function logError(requestUrl, errorCode) {
  errorLogQueue.push({ requestUrl, errorCode, timestamp: Date.now() });
  if (errorLogQueue.length > config.MAX_ERROR_LOGS) {
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
    DYNAMIC_WEIGHT_MULTIPLIER
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

        // 获取最近三次响应时间的平均值
        const avgResponseTime = server.responseTimes && server.responseTimes.length === 3 
          ? server.responseTimes.reduce((a, b) => a + b, 0) / 3 
          : responseTime;

        // 使用平均响应时间计算 EWMA
        if (typeof server.lastEWMA === 'undefined') {
          server.lastEWMA = avgResponseTime;
        } else {
          const beta = 0.2; // EWMA 衰减因子
          server.lastEWMA = beta * avgResponseTime + (1 - beta) * server.lastEWMA;
        }

        // 计算基础权重
        server.baseWeight = calculateBaseWeight(server.lastEWMA, BASE_WEIGHT_MULTIPLIER);

        // 计算动态权重
        server.dynamicWeight = calculateDynamicWeight(avgResponseTime, DYNAMIC_WEIGHT_MULTIPLIER);

        // 计算综合权重
        const combinedWeight = calculateCombinedWeight(server);

        console.log(LOG_PREFIX.SUCCESS, 
          `服务器 ${server.url} 健康检查成功, ` +
          `响应时间: ${responseTime}ms, ` +
          `最近3次平均: ${avgResponseTime.toFixed(0)}ms, ` +
          `EWMA: ${server.lastEWMA.toFixed(0)}ms, ` +
          `基础权重: ${server.baseWeight}, ` +
          `动态权重: ${server.dynamicWeight}, ` +
          `综合权重: ${combinedWeight}`
        );
      } catch (error) {
        server.healthy = false;
        server.lastCheck = Date.now();
        server.baseWeight = 0;
        server.dynamicWeight = 0;
        console.error(LOG_PREFIX.ERROR, `服务器 ${server.url} 健康检查失败: ${error.message}`);
      }
    }
  };

  // 立即执行一次健康检查
  await healthCheck();

  // 每30分钟执行一次健康检查
  setInterval(healthCheck, BASE_WEIGHT_UPDATE_INTERVAL);
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
      if (!server.alpha) {
        server.alpha = ALPHA_INITIAL;
      }

      return server.dynamicWeight;
    }
  }
}

// 并行请求相关函数
async function makeRequest(server, url, timeout) {
  const axios = require('axios');
  const fullUrl = `${server.url}${url}`;
  
  try {
    const startTime = Date.now();
    const response = await axios({
      method: 'get',
      url: fullUrl,
      timeout: timeout,
      headers: {
        'User-Agent': 'tmdb-go-proxy'
      }
    });
    
    const responseTime = Date.now() - startTime;
    return {
      success: true,
      data: response.data,
      responseTime,
      server
    };
  } catch (error) {
    return {
      success: false,
      error,
      server
    };
  }
}

async function makeParallelRequests(servers, url, timeout) {
  const requests = servers.map(server => 
    makeRequest(server, url, timeout)
  );
  
  try {
    const responses = await Promise.all(requests);
    // 过滤出成功的响应并按响应时间排序
    const successfulResponses = responses
      .filter(r => r.success)
      .sort((a, b) => a.responseTime - b.responseTime);
      
    if (successfulResponses.length > 0) {
      return successfulResponses[0]; // 返回最快的成功响应
    }
    throw new Error('所有并行求都失败了');
  } catch (error) {
    throw error;
  }
}

// EWMA 和请求限制配置
const EWMA_BETA = parseFloat(process.env.EWMA_BETA || '0.8'); // EWMA平滑系数，默认0.8
const RECENT_REQUEST_LIMIT = parseInt(process.env.RECENT_REQUEST_LIMIT || '10'); // 扩大记录数以更平滑动态权重

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
  calculateCombinedWeight,
  calculateDynamicWeight,
  updateServerState,
  makeRequest,
  makeParallelRequests
};