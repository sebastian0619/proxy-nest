const os = require('os');
const path = require('path');

module.exports = {
  // 服务器配置
  PORT: process.env.PORT || 6635,
  NUM_WORKERS: Math.max(1, os.cpus().length - 1),
  REQUEST_TIMEOUT: parseInt(process.env.REQUEST_TIMEOUT || '5000'),
  
  // TMDB 相关配置
  UPSTREAM_TYPE: process.env.UPSTREAM_TYPE || 'tmdb-api',
  TMDB_API_KEY: process.env.TMDB_API_KEY,
  TMDB_IMAGE_TEST_URL: process.env.TMDB_IMAGE_TEST_URL,
  
  // 负载均衡配置
  BASE_WEIGHT_MULTIPLIER: parseInt(process.env.BASE_WEIGHT_MULTIPLIER || '20'),
  DYNAMIC_WEIGHT_MULTIPLIER: parseInt(process.env.DYNAMIC_WEIGHT_MULTIPLIER || '50'),
  ALPHA_INITIAL: parseFloat(process.env.ALPHA_INITIAL || '0.5'),
  ALPHA_ADJUSTMENT_STEP: parseFloat(process.env.ALPHA_ADJUSTMENT_STEP || '0.05'),
  MAX_SERVER_SWITCHES: parseInt(process.env.MAX_SERVER_SWITCHES || '3'),
  
  // 缓存配置
  MEMORY_CACHE_SIZE: parseInt(process.env.MEMORY_CACHE_SIZE || '100'),
  CACHE_DIR: process.env.CACHE_DIR || path.join(process.cwd(), 'cache'),
  CACHE_TTL: parseInt(process.env.CACHE_TTL || '3600000'),
  CACHE_CLEANUP_INTERVAL: parseInt(process.env.CACHE_CLEANUP_INTERVAL || '300000'),
  
  // 代理配置
  HTTP_PROXY: process.env.HTTP_PROXY || '',
  HTTPS_PROXY: process.env.HTTPS_PROXY || '',
  
  // 时区配置
  TZ: process.env.TZ || 'Asia/Shanghai'
};