const os = require('os');
const path = require('path');

// 缓存清理间隔配置(毫秒)
const CLEANUP_INTERVALS = {
  'tmdb-api': 3600000,      // API缓存清理间隔1小时
  'tmdb-image': 86400000,   // 图片缓存清理间隔24小时
  'custom': parseInt(process.env.CUSTOM_CACHE_CLEANUP_INTERVAL || '3600000') // 自定义间隔,默认1小时
};

// 缓存相关配置
const CACHE_CONFIG = {
  // 缓存目录配置
  CACHE_DIR: process.env.CACHE_DIR || path.join(process.cwd(), 'cache'),
  CACHE_INDEX_FILE: 'cache_index.json',
  
  // 缓存参数配置
  CACHE_TTL: parseInt(process.env.CACHE_TTL || '3600000'), // 默认1小时
  MEMORY_CACHE_SIZE: parseInt(process.env.MEMORY_CACHE_SIZE || '100'),
  CACHE_MAX_SIZE: parseInt(process.env.CACHE_MAX_SIZE || '1000'),
  
  // 缓存清理配置
  get CACHE_CLEANUP_INTERVAL() {
    const upstreamType = process.env.UPSTREAM_TYPE || 'tmdb-api';
    return CLEANUP_INTERVALS[upstreamType] || CLEANUP_INTERVALS['tmdb-api'];
  },
  
  // 缓存文件配置
  CACHE_FILE_EXT: '.cache'
};

module.exports = {
  // 服务器配置
  PORT: process.env.PORT || 6635,
  NUM_WORKERS: Math.max(1, os.cpus().length - 1),
  UNHEALTHY_TIMEOUT: 300000,           // 不健康状态持续5分钟
  MAX_ERRORS_BEFORE_UNHEALTHY: 3,      // 连续3次错误后标记为不健康
  
  // 请求超时配置
  INITIAL_TIMEOUT: 8000,               // 初始请求超时时间（8秒）
  PARALLEL_TIMEOUT: 10000,             // 并行请求超时时间（10秒）
  REQUEST_TIMEOUT: 19000,              // 总请求超时时间（19秒，留1秒缓冲）
  WORKER_TIMEOUT: 20000,               // 工作线程超时时间（20秒）
  
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