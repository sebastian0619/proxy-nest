const { Worker } = require('worker_threads');
const express = require('express');
const path = require('path');
const { initializeLogPrefix } = require('./utils');

// 初始化日志前缀
const LOG_PREFIX = initializeLogPrefix();
global.LOG_PREFIX = LOG_PREFIX;

// 加载配置
let config;
try {
  config = require('./config');
} catch (error) {
  console.error(LOG_PREFIX.ERROR, `加载配置文件失败: ${error.message}`);
  process.exit(1);
}

// 验证上游服务器配置
if (!config.UPSTREAM_SERVERS) {
  console.error(LOG_PREFIX.ERROR, '未配置上游服务器（UPSTREAM_SERVERS）');
  process.exit(1);
}

// 创建健康检查器实例
const HealthChecker = require('./healthCheck');
const healthChecker = new HealthChecker({
  ...config,
  LOG_PREFIX
});

// 初始化服务器列表
const UPSTREAM_SERVERS = config.UPSTREAM_SERVERS.split(',').map(url => url.trim());
healthChecker.initialize(UPSTREAM_SERVERS);

// 创建工作线程池
const workers = [];
const numWorkers = config.NUM_WORKERS || 4;

// 准备要传递给工作线程的配置
const workerConfig = {
  ...config,
  // 移除不可序列化的内容
  LOG_PREFIX: {
    INFO: '[ 信息 ]',
    ERROR: '[ 错误 ]',
    WARN: '[ 警告 ]',
    SUCCESS: '[ 成功 ]',
    CACHE: {
      HIT: '[ 缓存命中 ]',
      MISS: '[ 缓存未命中 ]',
      INFO: '[ 缓存信息 ]'
    }
  }
};

for (let i = 0; i < numWorkers; i++) {
  const worker = new Worker(path.join(__dirname, 'worker.js'), {
    workerData: {
      workerId: i,
      config: workerConfig
    }
  });

  worker.on('message', message => {
    if (message.type === 'response_time') {
      // 处理响应时间更新
      const { url, responseTime } = message.data;
      healthChecker.handleResponseTime(url, responseTime);
      
      // 向所有工作线程广播更新后的服务器状态
      const serverStatus = healthChecker.getServerStatus(url);
      if (serverStatus) {
        broadcastServerStatus(url, serverStatus);
      }
    } else if (message.type === 'server_error') {
      // 处理服务器错误
      const { url, error } = message.data;
      healthChecker.handleServerError(url, error);
      
      // 向所有工作线程广播更新后的服务器状态
      const serverStatus = healthChecker.getServerStatus(url);
      if (serverStatus) {
        broadcastServerStatus(url, serverStatus);
      }
    }
  });

  worker.on('error', (error) => {
    console.error(LOG_PREFIX.ERROR, `工作线程 ${i} 发生错误:`, error);
  });

  workers.push(worker);
}

// 广播服务器状态更新到所有工作线程
function broadcastServerStatus(url, status) {
  for (const worker of workers) {
    worker.postMessage({
      type: 'server_status_update',
      data: {
        url,
        ...status
      }
    });
  }
}

// 启动健康检查
healthChecker.startHealthCheck().catch(error => {
  console.error(LOG_PREFIX.ERROR, `健康检查启动失败: ${error.message}`);
  process.exit(1);
});

// 创建Express应用
const app = express();
let currentWorker = 0;

app.get('*', (req, res) => {
  // 简单的轮询分发请求到工作线程
  const worker = workers[currentWorker];
  currentWorker = (currentWorker + 1) % workers.length;

  const requestId = Date.now() + Math.random().toString(36).substring(2);
  
  const messageHandler = message => {
    if (message.requestId !== requestId) return;

    worker.off('message', messageHandler);
    
    if (message.error) {
      res.status(500).send(message.error);
    } else {
      res.setHeader('Content-Type', message.response.contentType);
      res.send(message.response.data);
    }
  };

  worker.on('message', messageHandler);
  
  worker.postMessage({
    type: 'request',
    requestId,
    url: req.url
  });
});

// 添加健康检查API端点
app.get('/health', (req, res) => {
  const status = healthChecker.getAllServerStatus();
  res.json(status);
});

// 启动服务器
const PORT = process.env.PORT || 3000;
app.listen(PORT, () => {
  console.log(LOG_PREFIX.SUCCESS, `服务器启动成功，监听端口 ${PORT}`);
});

