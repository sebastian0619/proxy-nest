const { parentPort, workerData } = require('worker_threads');
const axios = require('axios');
const {
  initializeLogPrefix,
  validateResponse,
  tryRequestWithRetries,
  calculateBaseWeight,
  calculateDynamicWeight,
  calculateCombinedWeight
} = require('./utils');
const { 
  INITIAL_TIMEOUT,
  REQUEST_TIMEOUT,
  UPSTREAM_TYPE
} = require('./config');

// 从 workerData 中解构需要的配置
const { 
  ALPHA_INITIAL,
  BASE_WEIGHT_MULTIPLIER,
  DYNAMIC_WEIGHT_MULTIPLIER,
  workerId,
  upstreamServers,
  MAX_ERRORS_BEFORE_UNHEALTHY,
  UNHEALTHY_TIMEOUT
} = workerData;

// 设置全局 LOG_PREFIX
global.LOG_PREFIX = workerData.LOG_PREFIX;

let localUpstreamServers = [];

// 立即调用初始化函数
initializeWorker().catch(error => {
  console.error('[ 错误 ]', `工作线程初始化失败: ${error.message}`);
  process.exit(1);
});

// 初始化工作线程
async function initializeWorker() {
  try {
    if (!upstreamServers) {
      throw new Error('未配置上游服务器');
    }

    localUpstreamServers = upstreamServers.split(',').map(url => ({
      url: url.trim(),
      healthy: true,
      baseWeight: 1,
      dynamicWeight: 1,
      alpha: ALPHA_INITIAL,
      responseTimes: [],
      lastResponseTime: 0,
      lastEWMA: 0,
      isHealthy: true,
      errorCount: 0,
      recoveryTime: 0
    }));

    console.log(global.LOG_PREFIX.INFO, `工作线程 ${workerId} 初始化完成`);
  } catch (error) {
    console.error(global.LOG_PREFIX?.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

// 服务器健康检查配置

function markServerUnhealthy(server) {
  server.isHealthy = false;
  server.recoveryTime = Date.now() + UNHEALTHY_TIMEOUT;
  server.errorCount = 0;
  console.log(`服务器 ${server.url} 被标记为不健康状态，将在 ${new Date(server.recoveryTime).toLocaleTimeString()} 后恢复`);
}

function checkServerHealth(server) {
  if (!server.isHealthy && Date.now() >= server.recoveryTime) {
    server.isHealthy = true;
    server.errorCount = 0;
    console.log(`服务器 ${server.url} 已恢复健康状态`);
  }
  return server.isHealthy;
}

function selectUpstreamServer() {
  const healthyServers = localUpstreamServers.filter(server => {
    if (!server.hasOwnProperty('isHealthy')) {
      server.isHealthy = true;
      server.errorCount = 0;
    }
    return checkServerHealth(server);
  });

  if (healthyServers.length === 0) {
    throw new Error('没有健康的上游服务器可用');
  }

  // 计算总权重
  const totalWeight = healthyServers.reduce((sum, server) => {
    // 确保服务器有基本的权重属性
    if (!server.baseWeight) server.baseWeight = 1;
    if (!server.dynamicWeight) server.dynamicWeight = 1;

    // 使用 calculateCombinedWeight 计算综合权重
    const weight = calculateCombinedWeight(server);
    return sum + weight;
  }, 0);

  // 按权重随机选择
  const random = Math.random() * totalWeight;
  let weightSum = 0;

  for (const server of healthyServers) {
    const weight = calculateCombinedWeight(server);
    weightSum += weight;
    
    if (weightSum > random) {
      // 计算平均响应时间
      const avgResponseTime = server.responseTimes && server.responseTimes.length === 3 
        ? (server.responseTimes.reduce((a, b) => a + b, 0) / 3).toFixed(0) 
        : '未知';
      
      console.log(global.LOG_PREFIX.SUCCESS, 
        `选择服务器 ${server.url} [基础=${server.baseWeight} 动态=${server.dynamicWeight} 综合=${weight} 概率=${(weight / totalWeight * 100).toFixed(1)}% 最近3次平均=${avgResponseTime}ms]`
      );
      return server;
    }
  }

  // 保底返回第一个服务器
  const server = healthyServers[0];
  const weight = calculateCombinedWeight(server);
  const avgResponseTime = server.responseTimes && server.responseTimes.length === 3 
    ? (server.responseTimes.reduce((a, b) => a + b, 0) / 3).toFixed(0) 
    : '未知';
    
  console.log(global.LOG_PREFIX.WARN, 
    `保底服务器 ${server.url} [基础=${server.baseWeight} 动态=${server.dynamicWeight} 综合=${weight} 概率=${(weight / totalWeight * 100).toFixed(1)}% 最近3次平均=${avgResponseTime}ms]`
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
  let requestStartTime = Date.now();

  // 第一阶段：使用权重选择的服务器
  const selectedServer = selectUpstreamServer();
  try {
    const result = await tryRequestWithRetries(selectedServer, url, {
      timeout: INITIAL_TIMEOUT,
      upstreamType: UPSTREAM_TYPE
    }, global.LOG_PREFIX);
    
    // 成功后更新服务器权重
    addWeightUpdate(selectedServer, result.responseTime);
    
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

    return formatResponse(result);
    
  } catch (error) {
    const timeElapsed = Date.now() - requestStartTime;
    const timeRemaining = REQUEST_TIMEOUT - timeElapsed;
    
    // 如果剩余时间不足，直接抛出错误
    if (timeRemaining < 2000) { // 至少留2秒用于并行请求
      throw new Error(`请求超时: ${error.message}`);
    }
    
    console.log(`初始服务器 ${selectedServer.url} 请求失败或超时，启动并行请求，剩余时间: ${timeRemaining}ms`);
    
    // 第二阶段：并行请求其他健康服务器
    const healthyServers = workerData.upstreamServers.filter(server => 
      server !== selectedServer && checkServerHealth(server)
    );

    if (healthyServers.length === 0) {
      throw new Error('没有其他健康的服务器可用于并行请求');
    }

    try {
      console.log(`开始并行请求，健康服务器数量: ${healthyServers.length}`);
      healthyServers.forEach(server => {
        console.log(`- 健康服务器: ${server.url}`);
      });

      const parallelResult = await Promise.race([
        utils.makeParallelRequests(healthyServers, url, timeRemaining),
        new Promise((_, reject) => 
          setTimeout(() => reject(new Error('并行请求超时')), timeRemaining)
        )
      ]);
      
      if (!parallelResult || !parallelResult.success) {
        throw new Error('并行请求失败或返回无效结果');
      }

      console.log(`并行请求成功，使用服务器: ${parallelResult.server.url}, 响应时间: ${parallelResult.responseTime}ms`);
      addWeightUpdate(parallelResult.server, parallelResult.responseTime);
      return formatResponse(parallelResult);
    } catch (error) {
      console.error(`并行请求失败: ${error.message}`);
      selectedServer.errorCount++;
      if (selectedServer.errorCount >= MAX_ERRORS_BEFORE_UNHEALTHY) {
        markServerUnhealthy(selectedServer);
      }
      throw error;
    }
  }
}

function formatResponse(result) {
  // 确保图片数据以 Buffer 形式返回
  if (result.contentType && result.contentType.startsWith('image/')) {
    return {
      data: Buffer.from(result.data),
      contentType: result.contentType,
      responseTime: result.responseTime
    };
  }
  return result;
}

function addWeightUpdate(server, responseTime) {
  // 更新响应时间队列
  if (!server.responseTimes) {
    server.responseTimes = [];
  }
  server.responseTimes.push(responseTime);
  if (server.responseTimes.length > 3) {
    server.responseTimes.shift();
  }

  // 计算平均响应时间
  const avgResponseTime = server.responseTimes.length === 3
    ? server.responseTimes.reduce((a, b) => a + b, 0) / 3
    : responseTime;

  // 更新权重
  server.lastResponseTime = responseTime;
  server.lastEWMA = avgResponseTime;
  server.baseWeight = calculateBaseWeight(avgResponseTime, BASE_WEIGHT_MULTIPLIER);
  server.dynamicWeight = calculateDynamicWeight(avgResponseTime, DYNAMIC_WEIGHT_MULTIPLIER);
  
  parentPort.postMessage({
    type: 'weight_update',
    data: {
      server,
      responseTime,
      timestamp: Date.now()
    }
  });
}
