const { checkServerHealth } = require('./utils');
const { HealthStatus } = require('./constants');
const { calculateBaseWeight, calculateDynamicWeight } = require('./weightCalculator');

class HealthChecker {
  constructor(config) {
    this.config = config;
    this.servers = new Map();
    this.responseTimeBuffer = new Map(); // 用于存储每个服务器的最近响应时间
  }

  initialize(serverUrls) {
    for (const url of serverUrls) {
      this.servers.set(url, {
        url,
        status: HealthStatus.WARMING_UP,
        errorCount: 0,
        recoveryTime: 0,
        warmupStartTime: Date.now(),
        warmupRequests: 0,
        lastCheckTime: 0,
        alpha: this.config.ALPHA_INITIAL,
        responseTimes: [],
        lastResponseTime: 0,
        lastEWMA: 0,
        baseWeight: 1,
        dynamicWeight: 1
      });
    }
  }

  async startHealthCheck() {
    console.log(this.config.LOG_PREFIX.INFO, '开始健康检查...');
    
    // 立即执行一次健康检查
    await this.checkAllServers();
    
    // 设置定期健康检查
    setInterval(() => this.checkAllServers(), this.config.HEALTH_CHECK_CONFIG.HEALTH_CHECK_INTERVAL);
  }

  async checkAllServers() {
    for (const [url, server] of this.servers) {
      if (server.status === HealthStatus.UNHEALTHY && Date.now() < server.recoveryTime) {
        continue;
      }

      try {
        const responseTime = await checkServerHealth(server, {
          UPSTREAM_TYPE: this.config.UPSTREAM_TYPE,
          TMDB_API_KEY: this.config.TMDB_API_KEY,
          TMDB_IMAGE_TEST_URL: this.config.TMDB_IMAGE_TEST_URL,
          REQUEST_TIMEOUT: this.config.REQUEST_TIMEOUT,
          LOG_PREFIX: this.config.LOG_PREFIX
        });

        this.updateServerStatus(url, {
          status: HealthStatus.HEALTHY,
          responseTime,
          error: null
        });
      } catch (error) {
        this.handleHealthCheckFailure(url, error);
      }
    }
  }

  updateServerStatus(url, { status, responseTime, error }) {
    const server = this.servers.get(url);
    if (!server) return;

    if (status === HealthStatus.HEALTHY) {
      server.status = status;
      server.errorCount = 0;
      server.lastResponseTime = responseTime;
      server.responseTimes.push(responseTime);
      
      // 保持最近100个响应时间记录
      if (server.responseTimes.length > 100) {
        server.responseTimes.shift();
      }

      // 更新EWMA
      server.lastEWMA = server.lastEWMA === 0 
        ? responseTime 
        : server.lastEWMA * (1 - server.alpha) + responseTime * server.alpha;

      // 更新权重
      server.baseWeight = calculateBaseWeight(responseTime, this.config.BASE_WEIGHT_MULTIPLIER);
      server.dynamicWeight = calculateDynamicWeight(server.lastEWMA, this.config.DYNAMIC_WEIGHT_MULTIPLIER);

      console.log(this.config.LOG_PREFIX.SUCCESS, 
        `服务器 ${url} 健康检查成功 [响应时间=${responseTime}ms, EWMA=${server.lastEWMA.toFixed(2)}ms, ` +
        `基础权重=${server.baseWeight.toFixed(2)}, 动态权重=${server.dynamicWeight.toFixed(2)}]`
      );
    }
  }

  handleHealthCheckFailure(url, error) {
    const server = this.servers.get(url);
    if (!server) return;

    server.errorCount++;
    console.error(this.config.LOG_PREFIX.ERROR, 
      `服务器 ${url} 健康检查失败 (${server.errorCount}/${this.config.HEALTH_CHECK_CONFIG.MAX_CONSECUTIVE_FAILURES}): ${error.message}`
    );

    if (server.errorCount >= this.config.HEALTH_CHECK_CONFIG.MAX_CONSECUTIVE_FAILURES) {
      this.markServerUnhealthy(url);
    }
  }

  markServerUnhealthy(url) {
    const server = this.servers.get(url);
    if (!server) return;

    server.status = HealthStatus.UNHEALTHY;
    server.recoveryTime = Date.now() + this.config.HEALTH_CHECK_CONFIG.UNHEALTHY_TIMEOUT;
    server.errorCount = 0;
    server.warmupAttempts = 0;

    console.log(this.config.LOG_PREFIX.WARN, 
      `服务器 ${url} 被标记为不健康状态，将在 ${new Date(server.recoveryTime).toLocaleTimeString()} 后尝试恢复`
    );
  }

  // 处理来自工作线程的响应时间更新
  handleResponseTime(url, responseTime) {
    const server = this.servers.get(url);
    if (!server) return;

    // 更新响应时间统计
    server.lastResponseTime = responseTime;
    server.responseTimes.push(responseTime);
    if (server.responseTimes.length > 100) {
      server.responseTimes.shift();
    }

    // 更新EWMA
    server.lastEWMA = server.lastEWMA === 0 
      ? responseTime 
      : server.lastEWMA * (1 - server.alpha) + responseTime * server.alpha;

    // 更新权重
    server.baseWeight = calculateBaseWeight(responseTime, this.config.BASE_WEIGHT_MULTIPLIER);
    server.dynamicWeight = calculateDynamicWeight(server.lastEWMA, this.config.DYNAMIC_WEIGHT_MULTIPLIER);
  }

  // 处理来自工作线程的错误报告
  handleServerError(url, error) {
    const server = this.servers.get(url);
    if (!server) return;

    server.errorCount++;
    if (server.errorCount >= this.config.HEALTH_CHECK_CONFIG.MAX_CONSECUTIVE_FAILURES) {
      this.markServerUnhealthy(url);
    }
  }

  // 获取服务器状态
  getServerStatus(url) {
    const server = this.servers.get(url);
    if (!server) return null;

    return {
      status: server.status,
      baseWeight: server.baseWeight,
      dynamicWeight: server.dynamicWeight
    };
  }

  // 获取所有服务器状态
  getAllServerStatus() {
    const status = {};
    for (const [url, server] of this.servers) {
      status[url] = {
        status: server.status,
        baseWeight: server.baseWeight,
        dynamicWeight: server.dynamicWeight,
        lastResponseTime: server.lastResponseTime,
        lastEWMA: server.lastEWMA,
        errorCount: server.errorCount
      };
    }
    return status;
  }
}

module.exports = HealthChecker; 