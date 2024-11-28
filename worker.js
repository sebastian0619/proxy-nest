const { parentPort, workerData } = require('worker_threads');
const axios = require('axios');

// 初始化全局日志前缀
if (!workerData.logPrefix) {
  global.LOG_PREFIX = {
    INFO: '[ 信息 ]',
    ERROR: '[ 错误 ]',
    SUCCESS: '[ 成功 ]',
    WARN: '[ 警告 ]',
    CACHE: {
      HIT: '[ 缓存命中 ]',
      MISS: '[ 缓存未命中 ]'
    }
  };
} else {
  global.LOG_PREFIX = workerData.logPrefix;
}

// 验证LOG_PREFIX是否正确初始化
if (!global.LOG_PREFIX || !global.LOG_PREFIX.ERROR) {
  throw new Error('LOG_PREFIX 初始化失败');
}

const {
  validateResponse,
  tryRequestWithRetries,
  updateServerWeights,
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
  MAX_ERRORS_BEFORE_UNHEALTHY,
  UNHEALTHY_TIMEOUT
} = workerData;

// 解析上游服务器配置
let localUpstreamServersWithDefault = [];
try {
  // 直接使用传入的服务器数据
  const serverData = workerData.upstreamServers;
  localUpstreamServersWithDefault = initializeUpstreamServers(serverData);
  console.log(`成功加载 ${localUpstreamServersWithDefault.length} 个上游服务器配置`);
} catch (error) {
  console.error(`解析上游服务器配置失败: ${error.message}`);
  process.exit(1);
}

// 立即调用初始化函数
initializeWorker().catch(error => {
  console.error('[ 错误 ]', `工作线程初始化失败: ${error.message}`);
  process.exit(1);
});

// 初始化工作线程
async function initializeWorker() {
  try {
    if (localUpstreamServersWithDefault.length === 0) {
      throw new Error('未配置上游服务器');
    }

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
  const healthyServers = localUpstreamServersWithDefault.filter(server => {
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
    const weight = calculateCombinedWeight(server, {
      alpha: ALPHA_INITIAL
    });
    return sum + weight;
  }, 0);

  // 按权重随机选择
  const random = Math.random() * totalWeight;
  let weightSum = 0;

  for (const server of healthyServers) {
    const weight = calculateCombinedWeight(server, {
      alpha: ALPHA_INITIAL
    });
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
  const weight = calculateCombinedWeight(server, {
    alpha: ALPHA_INITIAL
  });
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
  const requestStartTime = Date.now();
  const selectedServer = selectUpstreamServer();
  
  // 创建初始请求Promise
  const initialRequest = tryRequestWithRetries(selectedServer, url, {
    timeout: INITIAL_TIMEOUT,
    upstreamType: UPSTREAM_TYPE
  }, global.LOG_PREFIX);

  try {
    // 设置8秒后自动触发并行请求的定时器
    const parallelTrigger = new Promise((_resolve, reject) => {
      setTimeout(() => reject(new Error('PARALLEL_TRIGGER')), INITIAL_TIMEOUT);
    });

    // 等待初始请求或触发并行
    const result = await Promise.race([initialRequest, parallelTrigger]);
    
    // 初始请求成功
    addWeightUpdate(selectedServer, result.responseTime);
    await validateAndUpdateServer(result, selectedServer);
    return formatResponse(result);

  } catch (error) {
    // 如果不是触发并行的信号，且已经超过总超时，则直接抛出
    if (error.message !== 'PARALLEL_TRIGGER' && 
        Date.now() - requestStartTime >= REQUEST_TIMEOUT) {
      throw error;
    }

    // 获取所有健康的其他服务器
    const healthyServers = getHealthyServers(selectedServer);
    if (healthyServers.length === 0) {
      throw new Error('没有其他健康的服务器可用于并行请求');
    }

    // 计算剩余可用时间
    const timeRemaining = REQUEST_TIMEOUT - (Date.now() - requestStartTime);
    
    try {
      // 并行请求所有健康服务器
      const parallelResult = await Promise.race([
        makeParallelRequests(healthyServers, url, timeRemaining),
        // 继续等待初始请求（果还没完成）
        initialRequest,
        // 总超时保护
        new Promise((_, reject) => 
          setTimeout(() => reject(new Error('请求超时')), timeRemaining)
        )
      ]);

      if (!parallelResult.success) {
        throw new Error('所有请求均失败');
      }

      // 更新成功服务器的权重
      addWeightUpdate(parallelResult.server, parallelResult.responseTime);
      await validateAndUpdateServer(parallelResult, parallelResult.server);
      return formatResponse(parallelResult);

    } catch (error) {
      handleServerError(selectedServer);
      throw new Error(`请求失败: ${error.message}`);
    }
  }
}

// 辅助函数：验证响应并更新服务器状态
async function validateAndUpdateServer(result, server) {
  try {
    const isValid = validateResponse(
      result.data,
      result.contentType,
      UPSTREAM_TYPE
    );
    
    if (!isValid) {
      throw new Error('Invalid response from upstream server');
    }
    
    // 重置错误计数
    server.errorCount = 0;
  } catch (error) {
    console.error(global.LOG_PREFIX.ERROR, `响应验证失败: ${error.message}`);
    handleServerError(server);
    throw error;
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
  // 使用统一的权重更新函数
  const weights = updateServerWeights(server, responseTime);
  
  // 发送权重更新消息到主线程
  parentPort.postMessage({
    type: 'weight_update',
    data: {
      server: server,
      responseTime,
      timestamp: Date.now()
    }
  });
}

// 在文件开头附近添加这个函数
function initializeUpstreamServers(serverData) {
  try {
    // 解析传入的JSON数据
    const servers = JSON.parse(serverData);
    
    if (!Array.isArray(servers)) {
      throw new Error('服务器配置必须是数组');
    }

    return servers.map(server => ({
      ...server, // 保持原有状态
      responseTimes: [], // 只初始化本地需要的新属性
      recoveryTime: 0
    }));
  } catch (error) {
    console.error(`解析服务器配置失败: ${error.message}`);
    throw error;
  }
}
