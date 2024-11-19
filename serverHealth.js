const axios = require('axios');
const { calculateBaseWeight, addWeightUpdate } = require('./utils');

let upstreamServers = [];

// 加载上游服务器列表
function loadUpstreamServers() {
  const upstreamUrls = process.env.UPSTREAM_SERVERS ? process.env.UPSTREAM_SERVERS.split(',') : [];
  if (upstreamUrls.length === 0) {
    console.error('No upstream servers configured in environment variables');
    process.exit(1);
  }
  
  upstreamServers = upstreamUrls.map(url => ({
    url: url.trim(),
    healthy: true,
    baseWeight: 1,
    dynamicWeight: 1,
    alpha: 0.5,
    responseTimes: [],
    responseTime: Infinity,
  }));
  console.log('已加载上游服务器:', upstreamServers.map(s => s.url).join(', '));
}

// 启动健康检查
function startHealthCheck() {
  setInterval(() => {
    for (const server of upstreamServers) {
      if (!server.healthy) {
        checkServerHealth(server).catch(error => {
          console.error(`健康检查失败 - 服务器: ${server.url}, 错误: ${error.message}`);
        });
      }
    }
  }, 300000);
}

// 检查服务器健康状态
async function checkServerHealth(server) {
  let testUrl = '';
  
  switch (process.env.UPSTREAM_TYPE) {
    case 'tmdb-api':
      if (!process.env.TMDB_API_KEY) {
        console.error('TMDB_API_KEY 环境变量未设置');
        process.exit(1);
      }
      testUrl = `/3/configuration?api_key=${process.env.TMDB_API_KEY}`;
      break;
      
    case 'tmdb-image':
      if (!process.env.TMDB_IMAGE_TEST_URL) {
        console.error('TMDB_IMAGE_TEST_URL 环境变量未设置');
        process.exit(1);
      }
      testUrl = process.env.TMDB_IMAGE_TEST_URL;
      break;
      
    case 'custom':
      testUrl = '/';
      break;
      
    default:
      console.error(`未知的上游类型: ${process.env.UPSTREAM_TYPE}`);
      process.exit(1);
  }

  try {
    const start = Date.now();
    const response = await axios.get(`${server.url}${testUrl}`, {
      timeout: parseInt(process.env.REQUEST_TIMEOUT || '5000'),
    });
    const responseTime = Date.now() - start;
    
    server.healthy = true;
    server.responseTimes.push(responseTime);
    if (server.responseTimes.length > parseInt(process.env.RECENT_REQUEST_LIMIT || '10')) {
      server.responseTimes.shift();
    }
    server.responseTime = responseTime;
    server.baseWeight = calculateBaseWeight(responseTime);
    server.alpha = Math.min(1, server.alpha + parseFloat(process.env.ALPHA_ADJUSTMENT_STEP || '0.05'));
    
    console.log(`健康检查成功 - 服务器: ${server.url}, 响应时间: ${responseTime}ms`);
    return responseTime;
  } catch (error) {
    server.healthy = false;
    server.baseWeight = 0;
    server.alpha = Math.max(0, server.alpha - parseFloat(process.env.ALPHA_ADJUSTMENT_STEP || '0.05'));
    throw error;
  }
}

// 更新基础权重
async function updateBaseWeights() {
  console.log("开始更新服务器基础权重");

  for (const server of upstreamServers) {
    try {
      const responseTime = await checkServerHealth(server);
      addWeightUpdate(server, responseTime);
      console.log(`服务器: ${server.url}, 响应时间: ${responseTime}ms, 基础权重: ${server.baseWeight}, Alpha值: ${server.alpha.toFixed(2)}`);
    } catch (error) {
      console.error(`服务器 ${server.url} 出错: ${error.message}`);
    }
  }

  console.log('权重更新汇总:', upstreamServers.map(s => 
    `${s.url}(权重:${s.baseWeight}, 状态:${s.healthy ? '正常' : '异常'})`
  ).join(', '));
}

module.exports = { loadUpstreamServers, startHealthCheck, updateBaseWeights }; 