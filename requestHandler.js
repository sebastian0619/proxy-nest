const axios = require('axios');
const { selectUpstreamServer, addWeightUpdate } = require('./utils');
const { LOG_PREFIX } = require('./utils');
const { MAX_RETRY_ATTEMPTS, RETRY_DELAY, REQUEST_TIMEOUT } = require('./config');

async function handleRequestWithRetries(req) {
  let lastError = null;
  let serverSwitchCount = 0;

  while (serverSwitchCount < MAX_RETRY_ATTEMPTS) {
    const server = selectUpstreamServer();
    try {
      console.log(LOG_PREFIX.INFO, `尝试请求: ${server.url}${req.originalUrl}`);
      
      const result = await axios.get(`${server.url}${req.originalUrl}`, {
        timeout: REQUEST_TIMEOUT,
        responseType: 'arraybuffer',
      });

      addWeightUpdate(server, Date.now() - result.config.startTime);

      return {
        data: result.data,
        contentType: result.headers['content-type'],
      };
    } catch (error) {
      lastError = error;
      console.error(LOG_PREFIX.ERROR, `请求失败 - 服务器: ${server.url}, 错误: ${error.message}`);

      if (error.isTimeout || error.code === 'ECONNREFUSED') {
        server.healthy = false;
      }

      serverSwitchCount++;
      if (serverSwitchCount < MAX_RETRY_ATTEMPTS) {
        console.log(LOG_PREFIX.WARN, `切换到下一个服务器 ${serverSwitchCount}/${MAX_RETRY_ATTEMPTS}`);
        await delay(RETRY_DELAY);
        continue;
      }
    }
  }

  throw new Error(`所有服务器都失败 (${MAX_RETRY_ATTEMPTS} 次重试): ${lastError.message}`);
}

module.exports = { handleRequestWithRetries }; 