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
      responseTime: Infinity,
    }));

    console.log(global.LOG_PREFIX.INFO, `工作线程 ${workerId} 初始化完成`);
  } catch (error) {
    console.error(global.LOG_PREFIX?.ERROR || '[ 错误 ]', `工作线程初始化失败: ${error.message}`);
    process.exit(1);
  }
}

function selectUpstreamServer() {
  const healthyServers = localUpstreamServers.filter(server => server.healthy);

  if (healthyServers.length === 0) {
    throw new Error('没有健康的上游服务器可用');
  }

  // 计算总权重
  const totalWeight = healthyServers.reduce((sum, server) => {
    const weight = server.baseWeight * server.dynamicWeight;
    return sum + weight;
  }, 0);

  // 计算每个服务器的选择概率
  const serverProbabilities = healthyServers.map(server => {
    const weight = server.baseWeight * server.dynamicWeight;
    const probability = (weight / totalWeight * 100).toFixed(2);
    return {
      server,
      weight,
      probability
    };
  });

  // 记录每个服务器的权重和概率
  console.log(global.LOG_PREFIX.WEIGHT, '服务器权重分布:');
  serverProbabilities.forEach(({ server, weight, probability }) => {
    console.log(
      `${server.url}: ` +
      `基础权重=${server.baseWeight}, ` +
      `动态权重=${server.dynamicWeight}, ` +
      `综合权重=${weight}, ` +
      `选择概率=${probability}%`
    );
  });

  // 按权重随机选择
  const random = Math.random() * totalWeight;
  let weightSum = 0;

  for (const { server, weight, probability } of serverProbabilities) {
    weightSum += weight;
    if (weightSum > random) {
      console.log(global.LOG_PREFIX.SUCCESS, 
        `选中服务器: ${server.url}, ` +
        `综合权重=${weight}, ` +
        `选择概率=${probability}%`
      );
      return server;
    }
  }

  // 保底返回第一个服务器
  const fallback = serverProbabilities[0];
  console.log(global.LOG_PREFIX.WARN, 
    `使用保底服务器: ${fallback.server.url}, ` +
    `综合权重=${fallback.weight}, ` +
    `选择概率=${fallback.probability}%`
  );
  return fallback.server;
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
  let lastError = null;
  let serverSwitchCount = 0;

  while (serverSwitchCount < MAX_SERVER_SWITCHES) {
    const server = selectUpstreamServer();

    try {
      const result = await tryRequestWithRetries(server, url, {
        REQUEST_TIMEOUT,
        UPSTREAM_TYPE
      }, global.LOG_PREFIX);
      
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
        console.error(global.LOG_PREFIX.ERROR, `响应验证失败: ${error.message}`);
        throw error;
      }

      // 确保图片数据以 Buffer 形式返回
      if (result.contentType.startsWith('image/')) {
        return {
          data: Buffer.from(result.data),  // 确保是 Buffer
          contentType: result.contentType,
          responseTime: result.responseTime
        };
      }
      
      // 其他类型数据保持原样
      return result;
      
    } catch (error) {
      lastError = error;
      console.error(global.LOG_PREFIX.ERROR, 
        `请求失败 - 服务器: ${server.url}, 错误: ${error.message}`
      );

      // 处理服务器失败
      if (error.isTimeout || error.code === 'ECONNREFUSED') {
        server.healthy = false;
      }

      serverSwitchCount++;
      
      if (serverSwitchCount < MAX_SERVER_SWITCHES) {
        console.log(global.LOG_PREFIX.WARN, 
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

