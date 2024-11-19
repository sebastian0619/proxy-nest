const https = require('https');
const express = require('express');
const axios = require('axios');
const morgan = require('morgan');
const url = require('url'); // 用于处理 URL，提取路径和查询参数

const app = express();
const PORT = process.env.PORT || 8080;
const BASE_WEIGHT_MULTIPLIER = 30;
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
      MISS: chalk.hex('#FFA500')('[ 缓存未命中 ]')
    },
    PROXY: chalk.cyan('[ 代理 ]'),
    WEIGHT: chalk.magenta('[ 权重 ]')
  };
  
  startServer();
});

function startServer() {
  app.use(morgan('combined'));

  const cache = {}; // 缓存对象

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
  function updateServerEWMA(server) {
    if (!server.ewma) {
      // 首次计算，直接使用当前响应时间
      server.ewma = server.responseTime;
    } else {
      // 使用 EWMA 计算平均响应时间
      server.ewma = EWMA_BETA * server.ewma + (1 - EWMA_BETA) * server.responseTime;
    }

    // 基于 EWMA 计算动态权重
    if (server.ewma <= 0 || !server.healthy) {
      server.dynamicWeight = 0;
    } else {
      // 响应时间越短，权重越高
      const baseScore = 1000 / server.ewma;
      // 使用对数函数使权重分布更均匀
      const weight = Math.log10(baseScore + 1) * 20;
      server.dynamicWeight = Math.min(MAX_WEIGHT, Math.max(MIN_WEIGHT, Math.floor(weight)));
    }
  }

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

    // 选择服务器
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

    // 如果没有选中任何服务器返回权重最高的并记录日志
    const selected = healthyServers[0];
    console.log(
      LOG_PREFIX.SUCCESS,
      `已选择: ${selected.url} [基础:${selected.baseWeight.toFixed(0)} 动态:${selected.dynamicWeight.toFixed(0)} 综��:${selected.combinedWeight.toFixed(0)} 概率:${((selected.combinedWeight / totalWeight) * 100).toFixed(1)}%]`
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
   * 处理请求失败时的权重调整
   */
  async function handleRequestError(server, error) {
    // 异步更新权重数据
    setImmediate(() => {
      updateWeightDataAsync(server, Infinity).catch(err => {
        console.error(LOG_PREFIX.ERROR, `更新权重数据失败: ${err.message}`);
      });
    });

    // 标记服务器状态
    server.healthy = false;
    
    // 记录错误日志
    console.warn(
      LOG_PREFIX.WARN,
      `代理请求失败 - 服务器: ${server.url}, 错误: ${error.message}`
    );
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

  /**
   * 带重试的请求处理
   */
  async function handleRequestWithRetry(req, res, maxRetries = 9) {
    for (let retryCount = 0; retryCount < maxRetries; retryCount++) {
      const server = selectUpstreamServer();
      
      try {
        const data = await handleSingleRequest(server, req);
        return data;
      } catch (error) {
        if (retryCount === maxRetries - 1) {
          console.error(LOG_PREFIX.ERROR, `所有重试都失败了`);
          throw error;
        }
      }
    }
  }

  // 权重更新队列处理
  const weightUpdateQueue = [];

  // 添加常量定义
  const RECENT_RESPONSES_LIMIT = 3;    // 保留最近3条响应时间记录
  const EWMA_BETA = 0.8;              // EWMA平滑系数
  const MIN_WEIGHT = 1;               // 最小权重
  const MAX_WEIGHT = 100;             // 最大权重
  const REQUEST_TIMEOUT = 5000;       // 请求超时时间（毫秒）

  // 权重更新队列处理
  function processWeightUpdateQueue() {
    while (weightUpdateQueue.length > 0) {
      const { server, responseTime } = weightUpdateQueue.shift();
      if (!server || responseTime === undefined) continue;
      
      // 更新服务器权重
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
      
      // 计算动态权重
      if (cachedData.ewma === Infinity || !server.healthy) {
        cachedData.dynamicWeight = 0;
      } else {
        const baseScore = 1000 / cachedData.ewma;
        const weight = Math.log10(baseScore + 1) * 20;
        cachedData.dynamicWeight = Math.min(MAX_WEIGHT, Math.max(MIN_WEIGHT, Math.floor(weight)));
      }
      
      // 更新缓存
      weightCache.set(server.url, cachedData);
    }
  }

  // 定期处理权重更新队列
  setInterval(processWeightUpdateQueue, 1000);

  // 代理请求
  app.use(
    '/',
    async (req, res, next) => {
      const cacheKey = getCacheKey(req);

      if (cache[cacheKey]) {
        console.log(LOG_PREFIX.CACHE.HIT, `键值: ${cacheKey}`);
        return res.send(cache[cacheKey]);
      }
      console.log(LOG_PREFIX.CACHE.MISS, `键值: ${cacheKey}`);

      let targetServer = selectUpstreamServer();
      let retryCount = 0;

      while (targetServer && retryCount < upstreamServers.length) {
        try {
          req.targetServer = targetServer.url;

          const start = Date.now();
          const proxyRes = await axios.get(`${req.targetServer}${req.originalUrl}`, {
            timeout: REQUEST_TIMEOUT,
            responseType: 'arraybuffer', // 支持二进制数据
          });

          // 验证响应内容类型
          const contentType = proxyRes.headers['content-type'];
          let isValidResponse = false;

          switch (UPSTREAM_TYPE) {
            case 'tmdb-api':
              isValidResponse = contentType.includes('application/json');
              if (!isValidResponse) {
                console.error(chalk.red(`Invalid content type for TMDB API: ${contentType}`));
                throw new Error('Invalid content type for TMDB API');
              }
              break;

            case 'tmdb-image':
              isValidResponse = contentType.includes('image/');
              if (!isValidResponse) {
                console.error(chalk.red(`Invalid content type for TMDB Image: ${contentType}`));
                throw new Error('Invalid content type for TMDB Image');
              }
              break;

            case 'custom':
              if (!CUSTOM_CONTENT_TYPE) {
                console.error(chalk.red('CUSTOM_CONTENT_TYPE environment variable is required when UPSTREAM_TYPE is custom'));
                throw new Error('CUSTOM_CONTENT_TYPE not configured');
              }
              isValidResponse = contentType.includes(CUSTOM_CONTENT_TYPE);
              if (!isValidResponse) {
                console.error(chalk.red(`Invalid content type for Custom type: ${contentType}`));
                throw new Error('Invalid content type for Custom type');
              }
              break;

            default:
              console.error(chalk.red(`Unknown UPSTREAM_TYPE: ${UPSTREAM_TYPE}`));
              throw new Error('Unknown UPSTREAM_TYPE');
          }

          const responseTime = Date.now() - start;

          // 异步更新权重数据
          process.nextTick(() => {
            weightUpdateQueue.push({
              server: targetServer,
              responseTime,
              timestamp: Date.now()
            });
          });

          const buffer = Buffer.from(proxyRes.data); // 缓存响应数据
          // 使用之前声明的 contentType 变量,不再重复声明
          cache[cacheKey] = { data: buffer, contentType };

          console.log(LOG_PREFIX.SUCCESS, `代理请求成功 - 路径: ${req.originalUrl}, 服务器: ${targetServer.url}, 响应时间: ${responseTime}ms, 内容类型: ${contentType}`);

          res.setHeader('Content-Type', contentType);
          return res.send(buffer);

        } catch (error) {
          console.log(LOG_PREFIX.WARN, `代理请求失败 - 服务器: ${targetServer.url}, 错误: ${error.message}, 重试次数: ${retryCount + 1}/${upstreamServers.length}`);
          targetServer.healthy = false;
          targetServer = selectUpstreamServer();
          retryCount++;
        }
      }

      console.error(chalk.red('No healthy upstream servers available after retries.'));
      res.status(502).send('No healthy upstream servers available.');
    }
  );

  app.listen(PORT, () => {
    console.log(LOG_PREFIX.SUCCESS, `服务器已启动 - 端口: ${PORT}, 上游类型: ${UPSTREAM_TYPE}, 自定义内容类型: ${CUSTOM_CONTENT_TYPE || '无'}`);
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
