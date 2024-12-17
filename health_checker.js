const { parentPort, workerData } = require('worker_threads');
const axios = require('axios');
const { Worker } = require('worker_threads');
const {
  calculateBaseWeight,
  calculateDynamicWeight,
  calculateCombinedWeight
} = require('./utils');

const {
  UPSTREAM_TYPE,
  TMDB_API_KEY,
  TMDB_IMAGE_TEST_URL,
  REQUEST_TIMEOUT,
  UNHEALTHY_TIMEOUT,
  MAX_ERRORS_BEFORE_UNHEALTHY,
  BASE_WEIGHT_MULTIPLIER,
  DYNAMIC_WEIGHT_MULTIPLIER,
  LOG_PREFIX
} = workerData;

// 健康状态枚举
const HealthStatus = {
  HEALTHY: 'healthy',
  UNHEALTHY: 'unhealthy'
};

// 存储所有上游服务器的状态
let upstreamServers = new Map();

// 初始化服务器状态
function initializeServers(serverUrls) {
  serverUrls.split(',').forEach(url => {
    upstreamServers.set(url.trim(), {
      url: url.trim(),
      status: HealthStatus.HEALTHY,
      errorCount: 0,
      recoveryTime: 0,
      lastCheckTime: Date.now(),
      responseTimes: [],
      lastResponseTime: 0,
      baseWeight: 1,
      dynamicWeight: 1
    });
  });
  console.log(LOG_PREFIX.INFO, `已初始化 ${upstreamServers.size} 个上游服务器`);
}

// 检查单个服务器健康状态
async function checkServerHealth(server) {
  const startTime = Date.now();
  try {
    if (UPSTREAM_TYPE === 'tmdb-api') {
      const response = await axios({
        method: 'get',
        url: `${server.url}/3/configuration`,
        params: { api_key: TMDB_API_KEY },
        timeout: REQUEST_TIMEOUT,
        validateStatus: status => status === 200
      });
    } else if (UPSTREAM_TYPE === 'tmdb-image') {
      const testUrl = TMDB_IMAGE_TEST_URL || '/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png';
      const response = await axios({
        method: 'head',
        url: `${server.url}${testUrl}`,
        timeout: REQUEST_TIMEOUT,
        validateStatus: status => status === 200
      });
    }

    const responseTime = Date.now() - startTime;
    return { success: true, responseTime };
  } catch (error) {
    console.error(LOG_PREFIX.ERROR, `健康检查失败 - ${server.url}: ${error.message}`);
    return { success: false, error: error.message };
  }
}

// 更新服务器状态
function updateServerState(server, checkResult) {
  const serverState = upstreamServers.get(server.url);
  
  if (checkResult.success) {
    if (serverState.status === HealthStatus.UNHEALTHY) {
      console.log(LOG_PREFIX.SUCCESS, `服务器 ${server.url} 已恢复健康状态`);
    }
    
    serverState.status = HealthStatus.HEALTHY;
    serverState.errorCount = 0;
    serverState.recoveryTime = 0;
    serverState.lastResponseTime = checkResult.responseTime;
    
    // 更新响应时间历史
    serverState.responseTimes.push(checkResult.responseTime);
    if (serverState.responseTimes.length > 10) {
      serverState.responseTimes.shift();
    }
    
    // 更新权重
    serverState.baseWeight = Math.max(1, BASE_WEIGHT_MULTIPLIER / checkResult.responseTime);
    serverState.dynamicWeight = Math.max(1, DYNAMIC_WEIGHT_MULTIPLIER / checkResult.responseTime);
  } else {
    serverState.errorCount++;
    if (serverState.errorCount >= MAX_ERRORS_BEFORE_UNHEALTHY) {
      if (serverState.status === HealthStatus.HEALTHY) {
        console.log(LOG_PREFIX.WARN, `服务器 ${server.url} 已标记为不健康状态`);
      }
      serverState.status = HealthStatus.UNHEALTHY;
      serverState.recoveryTime = Date.now() + UNHEALTHY_TIMEOUT;
    }
  }
  
  serverState.lastCheckTime = Date.now();
  upstreamServers.set(server.url, serverState);
  
  // 通知主线程状态更新
  parentPort.postMessage({
    type: 'health_status_update',
    data: {
      url: server.url,
      status: serverState.status,
      baseWeight: serverState.baseWeight,
      dynamicWeight: serverState.dynamicWeight,
      lastResponseTime: serverState.lastResponseTime,
      lastCheckTime: serverState.lastCheckTime,
      errorCount: serverState.errorCount,
      recoveryTime: serverState.recoveryTime
    }
  });
}

// 执行健康检查
async function performHealthCheck() {
  console.log(LOG_PREFIX.INFO, '开始执行健康检查...');
  for (const [url, server] of upstreamServers) {
    // 检查是否需要进行恢复检查
    if (server.status === HealthStatus.UNHEALTHY && Date.now() < server.recoveryTime) {
      continue;
    }
    
    const checkResult = await checkServerHealth(server);
    updateServerState(server, checkResult);
  }
  console.log(LOG_PREFIX.INFO, '健康检查完成');
}

// 启动健康检查循环
function startHealthCheck() {
  // 每30秒执行一次健康检查
  setInterval(performHealthCheck, 30000);
  
  // 立即执行第一次检查
  performHealthCheck();
}

// 监听来自主线程的消息
parentPort.on('message', (message) => {
  if (message.type === 'initialize') {
    initializeServers(message.data.upstreamServers);
    startHealthCheck();
  }
});

// 处理worker发送的响应时间消息
function handleResponseTime(server, responseTime, timestamp) {
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

  // 更新服务器状态
  server.lastResponseTime = responseTime;
  server.lastEWMA = avgResponseTime;
  server.baseWeight = calculateBaseWeight(avgResponseTime, BASE_WEIGHT_MULTIPLIER);
  server.dynamicWeight = calculateDynamicWeight(avgResponseTime, DYNAMIC_WEIGHT_MULTIPLIER);
  server.combinedWeight = calculateCombinedWeight({
    baseWeight: server.baseWeight,
    dynamicWeight: server.dynamicWeight
  });

  // 发送更新后的状态给所有worker
  broadcastServerStatus(server);
}

// 监听worker消息
function setupWorkerMessageHandling(worker) {
  worker.on('message', (message) => {
    if (message.type === 'response_time') {
      const { url, responseTime, timestamp } = message.data;
      const server = upstreamServers.find(s => s.url === url);
      if (server) {
        handleResponseTime(server, responseTime, timestamp);
      }
    }
    // ... existing message handling code ...
  });
}

// 广播服务器状态给所有worker
function broadcastServerStatus(server) {
  workers.forEach(worker => {
    worker.postMessage({
      type: 'server_health_update',
      data: {
        url: server.url,
        status: server.status,
        baseWeight: server.baseWeight,
        dynamicWeight: server.dynamicWeight,
        combinedWeight: server.combinedWeight,
        lastResponseTime: server.lastResponseTime,
        lastCheckTime: server.lastCheckTime,
        errorCount: server.errorCount,
        recoveryTime: server.recoveryTime
      }
    });
  });
} 