const express = require('express');
const morgan = require('morgan');
const { initializeCache } = require('./cache');
const { loadUpstreamServers, startHealthCheck } = require('./serverHealth');
const { handleRequest } = require('./routes');
const { LOG_PREFIX } = require('./utils');

const app = express();
const PORT = process.env.PORT || 8080;

app.use(morgan('combined'));
app.use('/', handleRequest);

async function startServer() {
  try {
    console.log(LOG_PREFIX.INFO, '初始化缓存...');
    await initializeCache();

    console.log(LOG_PREFIX.INFO, '加载上游服务器...');
    loadUpstreamServers();

    console.log(LOG_PREFIX.INFO, '启动健康检查...');
    startHealthCheck();

    app.listen(PORT, () => {
      console.log(LOG_PREFIX.SUCCESS, `服务器已启动 - 端口: ${PORT}`);
    });
  } catch (error) {
    console.error(LOG_PREFIX.ERROR, `初始化失败: ${error.message}`);
    process.exit(1);
  }
}

startServer();