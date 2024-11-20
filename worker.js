const { parentPort, workerData } = require('worker_threads');
const axios = require('axios');
const {
  initializeLogPrefix,
  validateResponse,
  tryRequestWithRetries
} = require('./utils');
// 从 workerData 中解构需要的配置
const { 
  MAX_SERVER_SWITCHES,
  UPSTREAM_TYPE,
  REQUEST_TIMEOUT,
  ALPHA_INITIAL,
  workerId,
  upstreamServers
} = workerData;

let LOG_PREFIX;
let localUpstreamServers = [];

// 立即调用初始化函数
initializeWorker().catch(error => {
  console.error('[ 错误 ]', `工作线程初始化失败: ${error.message}`);
  process.exit(1);
});

// 初始化工作线程
async function initializeWorker() {
  try {
    // 初始化 LOG_PREFIX
    LOG_PREFIX = await initializeLogPrefix();

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
      responseTime: Infinity,
    }));

    console.log(LOG_PREFIX.INFO, `工作线程 ${workerId} 初始化完成`);
  } catch (error) {
    console.error(LOG_PREFIX?.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

function selectUpstreamServer() {
  const healthyServers = localUpstreamServers.filter(server => server.healthy);

  if (healthyServers.length === 0) {
    throw new Error('没有健康的上游服务器可用');
  }

  const totalWeight = healthyServers.reduce((sum, server) => sum + server.baseWeight, 0);
  const random = Math.random() * totalWeight;
  let weightSum = 0;

  for (const server of healthyServers) {
    weightSum += server.baseWeight;
    if (weightSum > random) {
      return server;
    }
  }

  return healthyServers[0];
}

parentPort.on('message', async (message) => {
  if (message.type === 'request') {
    try {
      const result = await handleRequest(message.url);
      
      // 如果是JSON响应，确保数据已经是对象
      if (result.contentType.includes('application/json')) {
        const responseData = Buffer.isBuffer(result.data) ? 
          result.data.toString('utf-8') : 
          result.data;
          
        parentPort.postMessage({
          requestId: message.requestId,
          response: {
            data: typeof responseData === 'string' ? JSON.parse(responseData) : responseData,
            contentType: result.contentType
          }
        });
      } else {
        // 非JSON响应直接传递
        parentPort.postMessage({
          requestId: message.requestId,
          response: {
            data: result.data,
            contentType: result.contentType
          }
        });
      }
    } catch (error) {
      console.error(LOG_PREFIX.ERROR, `请求处理错误: ${error.message}`);
      parentPort.postMessage({
        requestId: message.requestId,
        error: error.message
      });
    }
  }
});

async function handleRequest(url) {
  let lastError = null;
  let serverSwitchCount = 0;

  while (serverSwitchCount < MAX_SERVER_SWITCHES) {
    const server = selectUpstreamServer();

    try {
      const result = await tryRequestWithRetries(server, url, {
        REQUEST_TIMEOUT,
        UPSTREAM_TYPE
      }, LOG_PREFIX);
      
      // 成功后更新服务器权重
      addWeightUpdate(server, result.responseTime);
      
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
        console.error(LOG_PREFIX.ERROR, `响应验证失败: ${error.message}`);
        throw error;
      }

      return result;
      
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

  // 所有尝试都失败了
  throw new Error(
    `所有服务器都失败 (${MAX_SERVER_SWITCHES} 次切换): ${lastError.message}`
  );
}

function addWeightUpdate(server, responseTime) {
  parentPort.postMessage({
    type: 'weight_update',
    data: {
      server,
      responseTime,
      timestamp: Date.now()
    }
  });
}
