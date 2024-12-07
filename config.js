const os = require('os');
const path = require('path');

// 缓存相关配置
const CACHE_CONFIG = {
  // 缓存目录配置
  CACHE_DIR: process.env.CACHE_DIR || path.join(process.cwd(), 'cache'),
  CACHE_INDEX_FILE: 'cache_index.json',
  
  // 缓存参数配置
  DISK_CACHE_TTL: parseInt(process.env.DISK_CACHE_TTL || '1440') * 60 * 1000,  // 磁盘缓存过期时间（默认24小时）
  MEMORY_CACHE_TTL: parseInt(process.env.MEMORY_CACHE_TTL || '60') * 60 * 1000, // 内存缓存过期时间（默认1小时）
  MEMORY_CACHE_SIZE: parseInt(process.env.MEMORY_CACHE_SIZE || '100'),          // 内存缓存条目数
  CACHE_MAX_SIZE: parseInt(process.env.CACHE_MAX_SIZE || '1000'),              // 磁盘缓存最大条目数
  
  // 缓存清理间隔配置（从分钟转换为毫秒）
  DISK_CACHE_CLEANUP_INTERVAL: parseInt(process.env.DISK_CACHE_CLEANUP_INTERVAL || '5') * 60 * 1000,
  MEMORY_CACHE_CLEANUP_INTERVAL: parseInt(process.env.MEMORY_CACHE_CLEANUP_INTERVAL || '1') * 60 * 1000,
  
  // 内容类型特定配置
  CONTENT_TYPE_CONFIG: {
    'image': {
      memory_ttl: parseInt(process.env.IMAGE_MEMORY_TTL || '10') * 60 * 1000,   // 图片内存缓存时间（默认10分钟）
      disk_ttl: parseInt(process.env.IMAGE_DISK_TTL || '4320') * 60 * 1000,     // 图片磁盘缓存时间（默认3天）
      skip_memory: process.env.IMAGE_SKIP_MEMORY === 'true'                      // 是否跳过内存缓存
    },
    'json': {
      memory_ttl: parseInt(process.env.JSON_MEMORY_TTL || '60') * 60 * 1000,    // JSON内存缓存时间（默认1小时）
      disk_ttl: parseInt(process.env.JSON_DISK_TTL || '1440') * 60 * 1000,      // JSON磁盘缓存时间（默认1天）
      skip_memory: false
    }
  },
  
  // 缓存文件配置
  CACHE_FILE_EXT: '.cache'
};

// 健康检查配置
const HEALTH_CHECK_CONFIG = {
  HEALTH_CHECK_INTERVAL: parseInt(process.env.HEALTH_CHECK_INTERVAL || '30000'),     // 健康检查间隔（30秒）
  MIN_HEALTH_CHECK_INTERVAL: parseInt(process.env.MIN_HEALTH_CHECK_INTERVAL || '5000'), // 最小健康检查间隔（5秒）
  MAX_CONSECUTIVE_FAILURES: parseInt(process.env.MAX_CONSECUTIVE_FAILURES || '3'),    // 最大连续失败次数
  WARMUP_REQUEST_COUNT: parseInt(process.env.WARMUP_REQUEST_COUNT || '10'),          // 预热请求数
  MAX_WARMUP_ATTEMPTS: parseInt(process.env.MAX_WARMUP_ATTEMPTS || '3')             // 最大预热尝试次数
};

// 重试配置
const RETRY_CONFIG = {
  MAX_RETRIES: parseInt(process.env.MAX_RETRIES || '3'),                           // 最大重试次数
  RETRY_DELAY_BASE: parseInt(process.env.RETRY_DELAY_BASE || '1000'),              // 基础重试延迟（毫秒）
  RETRY_DELAY_MAX: parseInt(process.env.RETRY_DELAY_MAX || '10000'),               // 最大重试延迟（毫秒）
  RETRY_JITTER: parseFloat(process.env.RETRY_JITTER || '0.1')                      // 重试抖动因子（0-1）
};

module.exports = {
  // 服务器配置
  PORT: process.env.PORT || 6635,
  NUM_WORKERS: Math.max(1, os.cpus().length - 1),
  UNHEALTHY_TIMEOUT: 900000,           // 不健康状态持续5分钟
  MAX_ERRORS_BEFORE_UNHEALTHY: 3,      // 连续3次错误后标记为不健康
  
  // 请求超时配置
  INITIAL_TIMEOUT: 8000,               // 初始请求超时时间（8秒）
  PARALLEL_TIMEOUT: 20000,             // 外部程序最大等待时间（20秒）
  REQUEST_TIMEOUT: parseInt(process.env.REQUEST_TIMEOUT || '30000'), // 单个请求的默认超时时间
  
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
  CACHE_CONFIG,
  
  // 健康检查配置
  HEALTH_CHECK_CONFIG,
  
  // 重试配置
  RETRY_CONFIG,
  
  // 错误处理配置
  ERROR_PENALTY_FACTOR: parseFloat(process.env.ERROR_PENALTY_FACTOR || '1.5'),      // 错误惩罚因子
  ERROR_RECOVERY_FACTOR: parseFloat(process.env.ERROR_RECOVERY_FACTOR || '0.8'),    // 错误恢复因子
};