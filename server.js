const express = require('express');
const axios = require('axios');
const morgan = require('morgan');
const url = require('url'); // 用于处理 URL，提取路径和查询参数
const fs = require('fs').promises;
const path = require('path');

const app = express();
const PORT = process.env.PORT || 8080;
const BASE_WEIGHT_MULTIPLIER = parseInt(process.env.BASE_WEIGHT_MULTIPLIER || '20');
const DYNAMIC_WEIGHT_MULTIPLIER = parseInt(process.env.DYNAMIC_WEIGHT_MULTIPLIER || '50');
const REQUEST_TIMEOUT = 5000;
const RECENT_REQUEST_LIMIT = 10; // 扩大记录数以更平滑动态权重
const ALPHA_INITIAL = 0.5; // 初始平滑因子 α
const ALPHA_ADJUSTMENT_STEP = 0.05; // 每次非缓存请求或健康检查调整的 α 增减值

const UPSTREAM_TYPE = process.env.UPSTREAM_TYPE || 'tmdb-api';
const CUSTOM_CONTENT_TYPE = process.env.CUSTOM_CONTENT_TYPE;
const TMDB_API_KEY = process.env.TMDB_API_KEY || '';
const TMDB_IMAGE_TEST_URL = process.env.TMDB_IMAGE_TEST_URL || '';

// 基础权重更新间隔（每5分钟）
const BASE_WEIGHT_UPDATE_INTERVAL = parseInt(process.env.BASE_WEIGHT_UPDATE_INTERVAL, 10) || 300000;

// 添加 EWMA 相关常量

let chalk;
let LOG_PREFIX; // 声明 LOG_PREFIX 变量

import('chalk').then((module) => {
  chalk = module.default;
  chalk.level = 3; // 支持 16M 色彩输出
  
  // 将 LOG_PREFIX 的定义移到这里
  LOG_PREFIX = {
    INFO: chalk.blue('[ 信息 ]'),
    ERROR: chalk.red('[ 错误 ]'),
    WARN: chalk.yellow('[ 警告 ]'),
    SUCCESS: chalk.green('[ 成功 ]'),
    CACHE: {
      HIT: chalk.green('[ 缓存命中 ]'),
      MISS: chalk.hex('#FFA500')('[ 缓存未命中 ]'),
      INFO: chalk.cyan('[ 缓存信息 ]')  // 添加缓存信息前缀
    },
    PROXY: chalk.cyan('[ 代理 ]'),
    WEIGHT: chalk.magenta('[ 权重 ]')
  };
  
  startServer();
});

// 1. 将 weightUpdateQueue 移到全局作用域
const weightUpdateQueue = [];

// 2. 将所有权重更新相关的常量也移到全局作用域
const RECENT_RESPONSES_LIMIT = 3;    // 保留最近3条响应时间记录
const EWMA_BETA = 0.8;              // EWMA平滑系数
const MIN_WEIGHT = 1;               // 最小权重
const MAX_WEIGHT = 100;             // 最大权重

// 1. 添加缓存相关常量
const CACHE_MAX_SIZE = parseInt(process.env.CACHE_MAX_SIZE || '1000');  // 最大缓存条目
const CACHE_TTL = parseInt(process.env.CACHE_TTL || '3600000');        // 缓存过期时间(ms)
const CACHE_CLEANUP_INTERVAL = 300000;                                 // 清理间隔(5分钟)

// 添加内存缓存相关常量
const MEMORY_CACHE_SIZE = parseInt(process.env.MEMORY_CACHE_SIZE || '100');  // 内存中保留的热点缓存数量
const MEMORY_CACHE_TTL = parseInt(process.env.MEMORY_CACHE_TTL || '300000'); // 内存缓存过期时间(5分钟)

// 添加缓存目录相关常量
const CACHE_DIR = process.env.CACHE_DIR || path.join(process.cwd(), 'cache');  // 默认在当前目录下的 cache 文件夹
const CACHE_FILE_EXT = '.cache';
const CACHE_INDEX_FILE = 'cache_index.json';

// 错误记录队列
const errorLogQueue = [];

// 最大错误记录数
const MAX_ERROR_LOGS = 10;

// 添加错误记录
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
  return uniqueRequests.size > 1; // 如果错误分布在多个请求上，可能是服务器问题
}

function startServer() {
  app.use(morgan('combined'));

  // 1. 先定义 LRU 缓存类
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

  // 2. 然后创建缓存实例
  const diskCache = new Map();    // 磁盘缓存索引
  const lruCache = new LRUCache(MEMORY_CACHE_SIZE);  // 内存缓存（LRU）

  // 2. 将 saveIndex 函数移到这里
  async function saveIndex() {
    try {
      const index = {};
      for (const [key, value] of diskCache.entries()) {
        index[key] = {
          filename: value.filename,
          contentType: value.contentType,
          timestamp: value.timestamp
        };
      }
      
      await fs.writeFile(
        path.join(CACHE_DIR, CACHE_INDEX_FILE),
        JSON.stringify(index, null, 2)
      );
      
      console.log(LOG_PREFIX.CACHE.INFO, `缓存索引已保存，共 ${diskCache.size} 项`);
    } catch (error) {
      console.error(LOG_PREFIX.ERROR, `保存缓存索引失败: ${error.message}`);
      throw error;
    }
  }


  // 4. 修改缓存读取函数
  async function getCacheItem(key) {
    // 首先检查 LRU 缓存
    const memoryItem = lruCache.get(key);
    if (memoryItem && Date.now() - memoryItem.timestamp <= MEMORY_CACHE_TTL) {
      console.log(LOG_PREFIX.CACHE.HIT, `内存缓存命中: ${key}`);
      return memoryItem;
    }

    // 检查磁盘缓存
    const diskItem = diskCache.get(key);
    if (!diskItem || Date.now() - diskItem.timestamp > CACHE_TTL) {
      console.log(LOG_PREFIX.CACHE.MISS, `缓存未命中: ${key}`);
      return null;
    }

    try {
      // 从磁盘读取数据
      const data = await fs.readFile(path.join(CACHE_DIR, diskItem.filename));
      
      // 构建缓存项
      const cacheItem = {
        data,
        contentType: diskItem.contentType,
        timestamp: Date.now()
      };

      // 放入 LRU 缓存
      lruCache.set(key, cacheItem);
      console.log(LOG_PREFIX.CACHE.HIT, `磁盘缓存命中: ${key}`);
      
      return cacheItem;
    } catch (error) {
      console.error(LOG_PREFIX.ERROR, `缓存读取失败: ${error.message}`);
      await deleteCacheItem(key);
      console.log(LOG_PREFIX.CACHE.MISS, `缓存读取失败，视为未命中: ${key}`);
      return null;
    }
  }

  // 5. 修改缓存写入函数
  async function setCacheItem(key, data, contentType) {
    const timestamp = Date.now();
    const filename = Buffer.from(key).toString('base64') + CACHE_FILE_EXT;

    try {
      // 写入磁盘
      await fs.writeFile(path.join(CACHE_DIR, filename), data);
      
      // 更新磁盘缓存索引
      diskCache.set(key, {
        filename,
        contentType,
        timestamp
      });

      // 写入 LRU 缓存
      lruCache.set(key, {
        data,
        contentType,
        timestamp
      });

      await saveIndex();
      
      // 添加日志
      console.log(LOG_PREFIX.CACHE.INFO, 
        `缓存写入成功 - 键值: ${key}, ` +
        `内存缓存数: ${lruCache.cache.size}, ` +
        `磁盘缓存数: ${diskCache.size}`
      );
    } catch (error) {
      console.error(LOG_PREFIX.ERROR, `缓存写入失败: ${error.message}`);
    }
  }

  // 缓存统计
  setInterval(() => {
    console.log(LOG_PREFIX.CACHE.INFO, 
      `缓存统计 - 内存: ${lruCache.cache.size}/${MEMORY_CACHE_SIZE}, ` +
      `磁盘: ${diskCache.size}/${CACHE_MAX_SIZE}`
    );
  }, 60000);

  let upstreamServers = [];
  function loadUpstreamServers() {
    // 从环境变量获取上游服务器列表，多个服务器用逗号分隔
    const upstreamUrls = process.env.UPSTREAM_SERVERS ? process.env.UPSTREAM_SERVERS.split(',') : [];
    
    if (upstreamUrls.length === 0) {
      console.error(chalk.red('No upstream servers configured in environment variables'));
      process.exit(1);
    }

    upstreamServers = upstreamUrls.map((url) => ({
      url: url.trim(),
      healthy: true,
      baseWeight: 1,
      dynamicWeight: 1,
      alpha: ALPHA_INITIAL,
      responseTimes: [],
      responseTime: Infinity,
    }));
  }

  loadUpstreamServers();
  console.log(LOG_PREFIX.INFO, '已加载上游服务器:', upstreamServers.map(s => s.url).join(', '));

  // 更新基础权重
  async function updateBaseWeights() {
    console.log(LOG_PREFIX.WEIGHT, "开始更新服务器基础权重");

    for (const server of upstreamServers) {
      try {
        const responseTime = await checkServerHealth(server);
        
        // 更新服务器状态
        server.healthy = true;
        server.responseTimes.push(responseTime);
        if (server.responseTimes.length > RECENT_REQUEST_LIMIT) {
          server.responseTimes.shift();
        }
        server.responseTime = responseTime;

        // 计算新的基础权重
        server.baseWeight = calculateBaseWeight(responseTime);
        // 健康检查成功时增加 alpha 值
        server.alpha = Math.min(1, server.alpha + ALPHA_ADJUSTMENT_STEP);

        console.log(LOG_PREFIX.SUCCESS, `服务器: ${server.url}, 响应时间: ${responseTime}ms, 基础权重: ${server.baseWeight}, Alpha值: ${server.alpha.toFixed(2)}`);
      } catch (error) {
        server.healthy = false;
        server.baseWeight = 0;
        // 健康检查失败时减少 alpha 值
        server.alpha = Math.max(0, server.alpha - ALPHA_ADJUSTMENT_STEP);
        console.error(LOG_PREFIX.ERROR, `服务器 ${server.url} 出错: ${error.message}`);
      }
    }

    console.log(LOG_PREFIX.WEIGHT, '权重更新汇总:', upstreamServers.map(s => 
      `${s.url}(权重:${s.baseWeight}, 状态:${s.healthy ? '正常' : '异常'})`
    ).join(', '));
  }

  // 分别设置两种权重的更新定时器
  setInterval(updateBaseWeights, BASE_WEIGHT_UPDATE_INTERVAL);

  // 初始启动时也执行一次
  updateBaseWeights().catch(error => {
    console.error(LOG_PREFIX.ERROR, '初始权重更新失败:', error.message);
  });

  /**
   * 使用 EWMA 计算动态权重
   * @param {Object} server 服务器对象
   * @param {number} newResponseTime 新的响应时间
   */


  /**
   * 存储上一次计算的权重数据
   * @type {Map<string, Object>}
   */
  const weightCache = new Map();

  /**
   * 选择上游服务器（使用缓存的权重数据）
   */
  function selectUpstreamServer() {
    const healthyServers = upstreamServers
      .filter(server => server.healthy)
      .map(server => {
        const cachedWeight = weightCache.get(server.url) || {
          dynamicWeight: 0,
          baseWeight: server.baseWeight || 0,
          ewma: 0
        };
        
        return {
          ...server,
          dynamicWeight: cachedWeight.dynamicWeight,
          combinedWeight: calculateCombinedWeight(server, cachedWeight.dynamicWeight)
        };
      })
      .sort((a, b) => b.combinedWeight - a.combinedWeight);

    if (healthyServers.length === 0) {
      throw new Error('没有健康的上游服务器可用');
    }

    const totalWeight = healthyServers.reduce((sum, server) => sum + server.combinedWeight, 0);
    const random = Math.random() * totalWeight;
    let weightSum = 0;

    for (const server of healthyServers) {
      weightSum += server.combinedWeight;
      if (weightSum > random) {
        console.log(
          LOG_PREFIX.SUCCESS,
          `已选择: ${server.url} [基础:${server.baseWeight.toFixed(0)} 动态:${server.dynamicWeight.toFixed(0)} 综合:${server.combinedWeight.toFixed(0)} 概率:${((server.combinedWeight / totalWeight) * 100).toFixed(1)}%]`
        );
        return server;
      }
    }

    const selected = healthyServers[0];
    console.log(
      LOG_PREFIX.SUCCESS,
      `已选择: ${selected.url} [基础:${selected.baseWeight.toFixed(0)} 动态:${selected.dynamicWeight.toFixed(0)} 综合:${selected.combinedWeight.toFixed(0)} 概率:${((selected.combinedWeight / totalWeight) * 100).toFixed(1)}%]`
    );
    return selected;
  }

  /**
   * 计算服务器的综合权重
   * @param {Object} server 服务器对象
   * @returns {number} 综合权重
   */
  function calculateCombinedWeight(server) {
    if (!server.healthy) return 0;
    
    const baseWeight = server.baseWeight || 0;
    const dynamicWeight = server.dynamicWeight || 0;
    const alpha = server.alpha || ALPHA_INITIAL;
    
    // 综合权重 = alpha * 动态权重 + (1 - alpha) * 基础权重
    return (alpha * dynamicWeight + (1 - alpha) * baseWeight);
  }

  // 标准化缓存键
  function getCacheKey(req) {
    const parsedUrl = url.parse(req.originalUrl, true);
    return `${parsedUrl.pathname}?${new URLSearchParams(parsedUrl.query)}`;
  }
  /**
   * 处理单个请求
   */
  async function handleSingleRequest(server, req) {
    const startTime = Date.now();
    
    try {
      const response = await axios.get(`${server.url}${req.path}`, {
        timeout: REQUEST_TIMEOUT
      });
      
      // 成功后异步更新权重
      process.nextTick(() => {
        const responseTime = Date.now() - startTime;
        weightUpdateQueue.push({ server, responseTime });
      });
      
      return response.data;
    } catch (error) {
      // 失败后异步更新权重
      process.nextTick(() => {
        weightUpdateQueue.push({ server, responseTime: Infinity });
      });
      
      throw error;
    }
  }

  // 3. 保留权重更新队列处理逻辑在 startServer 中
  function processWeightUpdateQueue() {
    while (weightUpdateQueue.length > 0) {
      const { server, responseTime } = weightUpdateQueue.shift();
      if (!server || responseTime === undefined) continue;
      
      const cachedData = weightCache.get(server.url) || {
        responseTimes: [],
        dynamicWeight: 0,
        ewma: 0
      };
      
      // 更新响应时间记录
      cachedData.responseTimes.push(responseTime);
      if (cachedData.responseTimes.length > RECENT_RESPONSES_LIMIT) {
        cachedData.responseTimes.shift();
      }
      
      // 计算新的 EWMA
      const avgTime = cachedData.responseTimes.reduce((a, b) => a + b, 0) / 
                     cachedData.responseTimes.length;
      
      cachedData.ewma = cachedData.ewma ? 
        EWMA_BETA * cachedData.ewma + (1 - EWMA_BETA) * avgTime : 
        avgTime;
      
      // 使用环境变量 DYNAMIC_WEIGHT_MULTIPLIER
      if (cachedData.ewma === Infinity || !server.healthy) {
        cachedData.dynamicWeight = 0;
      } else {
        const baseScore = 1000 / cachedData.ewma;
        const weight = Math.log10(baseScore + 1) * DYNAMIC_WEIGHT_MULTIPLIER;  // 使用环境变量
        cachedData.dynamicWeight = Math.min(MAX_WEIGHT, Math.max(MIN_WEIGHT, Math.floor(weight)));
      }
      
      weightCache.set(server.url, cachedData);
    }
  }

  // 4. 设置定时器处理权重更新队列
  setInterval(processWeightUpdateQueue, 1000);

  // 添加重试相关常量
  const MAX_RETRY_ATTEMPTS = 3;        // 单个服务器最大重试次数
  const MAX_SERVER_SWITCHES = 3;       // 最大切换服务器次数
  const RETRY_DELAY = 1000;            // 重试间隔时间(ms)

  // 添加延时函数
  const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

  // 修改主路由中的请求处理逻辑
  app.use('/', async (req, res, next) => {
    const cacheKey = getCacheKey(req);
    
    try {
      const cachedItem = await getCacheItem(cacheKey);
      if (cachedItem) {
        res.setHeader('Content-Type', cachedItem.contentType);
        return res.send(cachedItem.data);
      }

      // 缓存未命中，处理请求
      const response = await handleRequest(req);
      
      // 缓存响应
      if (response && response.data) {
        await setCacheItem(cacheKey, response.data, response.contentType);
        console.log(LOG_PREFIX.CACHE.INFO, `已缓存响应: ${cacheKey}`);
      }

      res.setHeader('Content-Type', response.contentType);
      res.send(response.data);
    } catch (error) {
      next(error);
    }
  });

  // 修改内容类型验证函数， JSON 验证
  async function validateResponse(response, contentType) {
    let isValidResponse = false;
    
    switch (UPSTREAM_TYPE) {
      case 'tmdb-api':
        // 首先验证 content-type
        if (!contentType.includes('application/json')) {
          throw new Error('Invalid content type: expected application/json');
        }
        
        // 验证 JSON 响应的合法性
        try {
          // 如果响应是 Buffer，先转换为字符串
          const jsonStr = response instanceof Buffer ? 
            response.toString('utf-8') : response;
          
          // 尝试解析 JSON
          const jsonData = JSON.parse(jsonStr);
          
          // 验证 TMDB API 的特定结构
          // 检查是否包含必要的字段，或者是否有错误信息
          if (jsonData.success === false || jsonData.status_code) {
            throw new Error(`TMDB API Error: ${jsonData.status_message || 'Unknown error'}`);
          }
          
          isValidResponse = true;
        } catch (error) {
          throw new Error(`Invalid JSON response: ${error.message}`);
        }
        break;
        
      case 'tmdb-image':
        isValidResponse = contentType.includes('image/');
        break;
        
      case 'custom':
        isValidResponse = CUSTOM_CONTENT_TYPE ? 
          contentType.includes(CUSTOM_CONTENT_TYPE) : true;
        break;
        
      default:
        throw new Error(`Unknown UPSTREAM_TYPE: ${UPSTREAM_TYPE}`);
    }
    
    if (!isValidResponse) {
      throw new Error(`Invalid response for ${UPSTREAM_TYPE}`);
    }
  }

  // 修改请求重试函数
  async function tryRequestWithRetries(server, req) {
    let retryCount = 0;
    
    while (retryCount < MAX_RETRY_ATTEMPTS) {
      try {
        const requestUrl = `${server.url}${req.originalUrl}`;
        console.log(LOG_PREFIX.INFO, `重试请求: ${requestUrl}`);
        
        const proxyRes = await axios.get(requestUrl, {
          timeout: REQUEST_TIMEOUT,
          responseType: 'arraybuffer',
        });
        
        server.healthy = true;
        console.log(LOG_PREFIX.SUCCESS, `服务器 ${server.url} 恢复健康`);
        return {
          buffer: Buffer.from(proxyRes.data),
          contentType: proxyRes.headers['content-type'],
          responseTime: Date.now()
        };
        
      } catch (error) {
        retryCount++;
        logError(req.originalUrl, error.response ? error.response.status : 'UNKNOWN');
        
        if (retryCount === MAX_RETRY_ATTEMPTS) {
          if (isServerError(error.response ? error.response.status : 'UNKNOWN')) {
            server.healthy = false; // 只有在确认是服务器问题时才标记为不健康
            console.error(LOG_PREFIX.ERROR, `服务器 ${server.url} 标记为不健康`);
          }
          throw error;
        }
        
        await delay(RETRY_DELAY);
        console.log(LOG_PREFIX.WARN, `重试请求 ${retryCount}/${MAX_RETRY_ATTEMPTS} - 服务器: ${server.url}, 错误: ${error.message}`);
      }
    }
  }


  // 在独立线程中运行重试逻辑
  function startHealthCheck() {
    setInterval(() => {
      for (const server of upstreamServers) {
        if (!server.healthy) {
          tryRequestWithRetries(server);
        }
      }
    }, BASE_WEIGHT_UPDATE_INTERVAL);
  }

  // 修改 handleRequest 函数以并发请求所有健康的上游服务器
  async function handleRequest(req) {
    const healthyServers = upstreamServers.filter(server => server.healthy);
    if (healthyServers.length === 0) {
      throw new Error('没有健康的上游服务器可用');
    }

    const requests = healthyServers.map(server => 
      axios.get(`${server.url}${req.originalUrl}`, {
        timeout: REQUEST_TIMEOUT,
        responseType: 'arraybuffer',
      }).then(response => ({
        server,
        data: response.data,
        contentType: response.headers['content-type'],
        responseTime: Date.now()
      }))
    );

    try {
      const result = await Promise.any(requests);
      console.log(LOG_PREFIX.SUCCESS, `最快响应来自: ${result.server.url}`);
      return {
        data: result.data,
        contentType: result.contentType
      };
    } catch (error) {
      throw new Error('所有请求都失败了');
    }
  }

  // 启动健康检查
  startHealthCheck();

  // 4. 初始化缓存目录和启动服务器
  async function initialize() {
    try {
      // 初始化缓存目录
      await fs.mkdir(CACHE_DIR, { recursive: true });
      console.log(LOG_PREFIX.INFO, `缓存目录初始化成功: ${CACHE_DIR}`);
      
      // 加载缓存索引
      try {
        const indexData = await fs.readFile(path.join(CACHE_DIR, CACHE_INDEX_FILE));
        const index = JSON.parse(indexData);
        for (const [key, meta] of Object.entries(index)) {
          if (Date.now() - meta.timestamp <= CACHE_TTL) {
            diskCache.set(key, meta);
          }
        }
        console.log(LOG_PREFIX.CACHE.INFO, `已加载 ${diskCache.size} 个缓存项`);
      } catch (error) {
        await saveIndex();
      }

      // 动 HTTP 服务器（只在这里启动一次）
      app.listen(PORT, () => {
        console.log(LOG_PREFIX.SUCCESS, 
          `服务器已启动 - 端口: ${PORT}, 上游类型: ${UPSTREAM_TYPE}, ` +
          `自定义内容类型: ${CUSTOM_CONTENT_TYPE || '无'}`
        );
        
        // 启动缓存统计
        setInterval(() => {
          console.log(LOG_PREFIX.CACHE.INFO, 
            `缓存统计 - 内存: ${lruCache.cache.size}/${MEMORY_CACHE_SIZE}, ` +
            `磁盘: ${diskCache.size}/${CACHE_MAX_SIZE}`
          );
        }, 60000);
      });

    } catch (error) {
      console.error(LOG_PREFIX.ERROR, `初始化失败: ${error.message}`);
      process.exit(1);
    }
  }

  // 启动初始化
  initialize().catch(error => {
    console.error(LOG_PREFIX.ERROR, `初始化失败: ${error.message}`);
    process.exit(1);
  });

  // 添加主要的请求处理函数
  async function handleRequest(req) {
    let lastError = null;
    let serverSwitchCount = 0;

    while (serverSwitchCount < MAX_SERVER_SWITCHES) {
      const server = selectUpstreamServer();
      
      try {
        const result = await tryRequestWithRetries(server, req);
        
        // 成功后更新服务器权重
        addWeightUpdate(server, result.responseTime);
        
        return {
          data: result.buffer,
          contentType: result.contentType
        };
        
      } catch (error) {
        lastError = error;
        console.error(LOG_PREFIX.ERROR, 
          `请求失败 - 服务器: ${server.url}, 错误: ${error.message}`
        );

        // 处理服务器失败
        if (error.isTimeout || error.code === 'ECONNREFUSED') {
          server.healthy = false;
        }

        serverSwitchCount++;
        
        if (serverSwitchCount < MAX_SERVER_SWITCHES) {
          console.log(LOG_PREFIX.WARN, 
            `切换到下一个服务器 ${serverSwitchCount}/${MAX_SERVER_SWITCHES}`
          );
          continue;
        }
      }
    }

    // 所有重试都失败了
    throw new Error(
      `所有服务器都失败 (${MAX_SERVER_SWITCHES} 次切换): ${lastError.message}`
    );
  }

  // 添加错误处理中间件
  app.use((err, req, res, next) => {
    console.error(LOG_PREFIX.ERROR, `请求处理错误: ${err.message}`);
    res.status(502).json({
      error: '代理请求失败',
      message: err.message
    });
  });
}
async function checkServerHealth(server) {
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
      // 自定义类型可以在这里添加其他健康检查逻辑
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

function calculateBaseWeight(responseTime) {
  // 基于响应时间计算基础权重
  // 响应时间越短，权重越高
  return Math.min(
    100, // 设置最大权重上为100
    Math.max(1, Math.floor((1000 / responseTime) * BASE_WEIGHT_MULTIPLIER))
  );
}

// 5. 修改 addWeightUpdate 函数以使用全局 weightUpdateQueue
function addWeightUpdate(server, responseTime) {
  process.nextTick(() => {
    weightUpdateQueue.push({
      server,
      responseTime,
      timestamp: Date.now()
    });
  });
}

// 修改 deleteCacheItem 函数，确保调用 saveIndex
async function deleteCacheItem(key) {
  const item = diskCache.get(key);
  if (item) {
    try {
      // 删除缓存文件
      await fs.unlink(path.join(CACHE_DIR, item.filename));
      diskCache.delete(key);
      lruCache.cache.delete(key);
      
      // 更新索引
      await saveIndex();
    } catch (error) {
      console.error(LOG_PREFIX.ERROR, `删除缓存失败: ${error.message}`);
    }
  }
}

