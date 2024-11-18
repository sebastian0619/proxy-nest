const https = require('https');
const express = require('express');
const { createProxyMiddleware } = require('http-proxy-middleware');
const axios = require('axios');
const morgan = require('morgan');
const url = require('url'); // 用于处理 URL，提取路径和查询参数

const app = express();
const PORT = process.env.PORT || 8080;
const BASE_WEIGHT_MULTIPLIER = 50;
const DYNAMIC_WEIGHT_MULTIPLIER = 50;
const REQUEST_TIMEOUT = 5000;
const RECENT_REQUEST_LIMIT = 10; // 扩大记录数以更平滑动态权重
const ALPHA_INITIAL = 0.5; // 初始平滑因子 α
const ALPHA_ADJUSTMENT_STEP = 0.05; // 每次非缓存请求或健康检查调整的 α 增减值
const UPSTREAM_TYPE = process.env.UPSTREAM_TYPE || 'tmdb-api';
const CUSTOM_CONTENT_TYPE = process.env.CUSTOM_CONTENT_TYPE;
const TMDB_API_KEY = process.env.TMDB_API_KEY || '';
const TMDB_IMAGE_TEST_URL = process.env.TMDB_IMAGE_TEST_URL || '';

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

  // 确保定时执行健康检查和权重更新
  setInterval(updateBaseWeights, WEIGHT_UPDATE_INTERVAL);

  // 初始启动时也执行一次
  updateBaseWeights().catch(error => {
    console.error(LOG_PREFIX.ERROR, '初始权重更新失败:', error.message);
  });

  // 动态权重的指数加权平均计算
  function adjustDynamicWeight(server) {
    const smoothingFactor = 0.3;
    const avgResponseTime = server.responseTimes.reduce((acc, time) => acc * (1 - smoothingFactor) + time * smoothingFactor, 0);
    server.dynamicWeight = Math.min(
      100,
      Math.max(1, Math.floor((1000 / avgResponseTime) * DYNAMIC_WEIGHT_MULTIPLIER))
    );

    console.log(LOG_PREFIX.WEIGHT, `服务器 ${server.url} 动态权重更新, 平均响应时间: ${avgResponseTime.toFixed(2)}ms, 动态权重: ${server.dynamicWeight}`);
  }

  // 综合权重计算使用 EWMA
  function calculateCombinedWeight(server) {
    return Math.floor(server.alpha * server.dynamicWeight + (1 - server.alpha) * server.baseWeight);
  }

  // 选择健康的上游服务器
  function getWeightedRandomServer() {
    const healthyServers = upstreamServers.filter(server => server.healthy);
    if (healthyServers.length === 0) return null;

    const weightedServers = [];
    healthyServers.forEach(server => {
      const combinedWeight = calculateCombinedWeight(server);
      for (let i = 0; i < combinedWeight; i++) {
        weightedServers.push(server);
      }
    });

    const selectedServer = weightedServers[Math.floor(Math.random() * weightedServers.length)];
    console.log(LOG_PREFIX.PROXY, `已选择上游服务器: ${selectedServer.url}, 综合权重: ${calculateCombinedWeight(selectedServer)}`);
    return selectedServer;
  }

  // 标准化缓存键
  function getCacheKey(req) {
    const parsedUrl = url.parse(req.originalUrl, true);
    return `${parsedUrl.pathname}?${new URLSearchParams(parsedUrl.query)}`;
  }

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

      let targetServer = getWeightedRandomServer();
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
          targetServer.responseTimes.push(responseTime);
          if (targetServer.responseTimes.length > RECENT_REQUEST_LIMIT) {
            targetServer.responseTimes.shift(); // 保持最近的记录
          }

          adjustDynamicWeight(targetServer);
          const buffer = Buffer.from(proxyRes.data); // 缓存响应数据
          // 使用之前声明的 contentType 变量,不再重复声明
          cache[cacheKey] = { data: buffer, contentType };

          console.log(LOG_PREFIX.SUCCESS, `代理请求成功 - 路径: ${req.originalUrl}, 服务器: ${targetServer.url}, 响应时间: ${responseTime}ms, 内容类型: ${contentType}`);

          res.setHeader('Content-Type', contentType);
          return res.send(buffer);

        } catch (error) {
          console.log(LOG_PREFIX.WARN, `代理请求失败 - 服务器: ${targetServer.url}, 错误: ${error.message}, 重试次数: ${retryCount + 1}/${upstreamServers.length}`);
          targetServer.healthy = false;
          targetServer = getWeightedRandomServer();
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
      testUrl = `/3/movie/configuration?api_key=${TMDB_API_KEY}`;
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
    100, // 设置最大权重上限为100
    Math.max(1, Math.floor((1000 / responseTime) * BASE_WEIGHT_MULTIPLIER))
  );
}
