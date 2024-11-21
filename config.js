const os = require('os');
const path = require('path');

// 缓存相关配置
const CACHE_CONFIG = {
  // 缓存目录配置
  CACHE_DIR: process.env.CACHE_DIR || path.join(process.cwd(), 'cache'),
  CACHE_INDEX_FILE: 'cache_index.json',
  
  // 缓存参数配置
  CACHE_TTL: parseInt(process.env.CACHE_TTL || '3600000'), // 默认1小时
  MEMORY_CACHE_SIZE: parseInt(process.env.MEMORY_CACHE_SIZE || '100'), // 内存缓存条目数
  CACHE_MAX_SIZE: parseInt(process.env.CACHE_MAX_SIZE || '1000'), // 磁盘缓存最大条目数
  CACHE_CLEANUP_INTERVAL: 300000, // 缓存清理间隔(5分钟)
  
  // 缓存文件配置
  CACHE_FILE_EXT: '.cache'
};

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
  
  // 代理配置
  HTTP_PROXY: process.env.HTTP_PROXY || '',
  HTTPS_PROXY: process.env.HTTPS_PROXY || '',
  
  // 时区配置
  TZ: process.env.TZ || 'Asia/Shanghai',
  
  // Custom upstream 配置
  CUSTOM_CONTENT_TYPE: process.env.CUSTOM_CONTENT_TYPE || 'application/json',  // 默认值
  CACHE_CONFIG
};