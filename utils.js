// utils.js

const fs = require('fs').promises;
const path = require('path');
const axios = require('axios');
const crypto = require('crypto');

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
    100,
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
function validateResponse(response, contentType, upstreamType) {
    let isValidResponse = false;
    
    switch (upstreamType) {
      case 'tmdb-api':
        // 验证 content-type
        if (!contentType.includes('application/json')) {
          throw new Error('Invalid content type: expected application/json');
        }
        
        try {
          const jsonStr = response instanceof Buffer ? 
            response.toString('utf-8') : response;
          const jsonData = JSON.parse(jsonStr);
          
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
        isValidResponse = true; // 自定义类型不做特殊验证
        break;
        
      default:
        throw new Error(`Unknown UPSTREAM_TYPE: ${upstreamType}`);
    }
    
    return isValidResponse;
}

module.exports = {
  initializeLogPrefix,
  calculateBaseWeight,
  getCacheKey,
  logError,
  isServerError,
  delay,
  checkServerHealth,
  validateResponse
};