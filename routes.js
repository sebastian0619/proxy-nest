const { getCacheItem, setCacheItem } = require('./cache');
const { handleRequest } = require('./requestHandler');
const { LOG_PREFIX } = require('./utils');
const url = require('url');

function getCacheKey(req) {
  const parsedUrl = url.parse(req.originalUrl, true);
  return `${parsedUrl.pathname}?${new URLSearchParams(parsedUrl.query)}`;
}

async function handleRequest(req, res, next) {
  const cacheKey = getCacheKey(req);

  try {
    console.log(LOG_PREFIX.INFO, `处理请求: ${req.method} ${req.originalUrl}`);
    
    const cachedItem = await getCacheItem(cacheKey);
    if (cachedItem) {
      console.log(LOG_PREFIX.CACHE.HIT, `缓存命中: ${cacheKey}`);
      res.setHeader('Content-Type', cachedItem.contentType);
      return res.send(cachedItem.data);
    }

    console.log(LOG_PREFIX.CACHE.MISS, `缓存未命中: ${cacheKey}`);
    const response = await handleRequest(req);

    if (response && response.data) {
      await setCacheItem(cacheKey, response.data, response.contentType);
      console.log(LOG_PREFIX.CACHE.INFO, `缓存已更新: ${cacheKey}`);
    }

    res.setHeader('Content-Type', response.contentType);
    res.send(response.data);
  } catch (error) {
    console.error(LOG_PREFIX.ERROR, `请求处理错误: ${error.message}`);
    next(error);
  }
}

module.exports = { handleRequest };