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

function selectUpstreamServer(upstreamServers) {
  // 选择健康的上游服务器
  const healthyServers = upstreamServers.filter(server => server.healthy);
  if (healthyServers.length === 0) {
    throw new Error('没有可用的健康上游服务器');
  }

  // 根据权重选择服务器
  const totalWeight = healthyServers.reduce((sum, server) => sum + server.dynamicWeight, 0);
  let randomWeight = Math.random() * totalWeight;

  for (const server of healthyServers) {
    if (randomWeight < server.dynamicWeight) {
      return server;
    }
    randomWeight -= server.dynamicWeight;
  }

  return healthyServers[0]; // 作为后备，返回第一个健康服务器
}

async function checkServerHealth(server, testUrl, requestTimeout) {
  try {
    const start = Date.now();
    const response = await axios.get(`${server.url}${testUrl}`, {
      timeout: requestTimeout,
    });
    const responseTime = Date.now() - start;
    
    console.log(LOG_PREFIX.SUCCESS, `健康检查成功 - 服务器: ${server.url}, 响应时间: ${responseTime}ms`);
    return responseTime;
  } catch (error) {
    console.error(LOG_PREFIX.ERROR, `健康检查失败 - 服务器: ${server.url}, 错误: ${error.message}`);
    throw error;
  }
}

function addWeightUpdate(server, responseTime, weightUpdateQueue) {
  process.nextTick(() => {
    weightUpdateQueue.push({
      server,
      responseTime,
      timestamp: Date.now()
    });
  });
}

function calculateBaseWeight(responseTime, baseWeightMultiplier) {
  // 基于响应时间计算基础权重
  // 响应时间越短，权重越高
  return Math.min(
    100, // 设置最大权重上为100
    Math.max(1, Math.floor((1000 / responseTime) * baseWeightMultiplier))
  );
}

module.exports = { LRUCache, selectUpstreamServer, checkServerHealth, addWeightUpdate, calculateBaseWeight }; 