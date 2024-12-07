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

// 从环境变量获取配置，如果没有则使用默认值
const config = {
  // 服务器配置
  PORT: process.env.PORT || 3000,
  NUM_WORKERS: parseInt(process.env.NUM_WORKERS || '4', 10),
  
  // 上游服务器配置
  UPSTREAM_SERVERS: process.env.UPSTREAM_SERVERS,
  UPSTREAM_TYPE: process.env.UPSTREAM_TYPE || 'tmdb',
  
  // TMDB 配置
  TMDB_API_KEY: process.env.TMDB_API_KEY,
  TMDB_IMAGE_TEST_URL: process.env.TMDB_IMAGE_TEST_URL || '/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png',

  // 请求配置
  REQUEST_TIMEOUT: parseInt(process.env.REQUEST_TIMEOUT || '5000', 10),
  
  // 权重计算配置
  BASE_WEIGHT_MULTIPLIER: parseFloat(process.env.BASE_WEIGHT_MULTIPLIER || '10'),
  DYNAMIC_WEIGHT_MULTIPLIER: parseFloat(process.env.DYNAMIC_WEIGHT_MULTIPLIER || '15'),
  ALPHA_INITIAL: parseFloat(process.env.ALPHA_INITIAL || '0.2'),
  ALPHA_ADJUSTMENT_STEP: parseFloat(process.env.ALPHA_ADJUSTMENT_STEP || '0.05'),
  
  // 健康检查配置
  HEALTH_CHECK_CONFIG: {
    HEALTH_CHECK_INTERVAL: parseInt(process.env.HEALTH_CHECK_INTERVAL || '30000', 10),
    MIN_HEALTH_CHECK_INTERVAL: parseInt(process.env.MIN_HEALTH_CHECK_INTERVAL || '5000', 10),
    MAX_CONSECUTIVE_FAILURES: parseInt(process.env.MAX_CONSECUTIVE_FAILURES || '3', 10),
    WARMUP_REQUEST_COUNT: parseInt(process.env.WARMUP_REQUEST_COUNT || '10', 10),
    MAX_WARMUP_ATTEMPTS: parseInt(process.env.MAX_WARMUP_ATTEMPTS || '3', 10),
    UNHEALTHY_TIMEOUT: parseInt(process.env.UNHEALTHY_TIMEOUT || '900000', 10), // 15分钟
  },

  // 重试配置
  RETRY_CONFIG: {
    MAX_RETRIES: parseInt(process.env.MAX_RETRIES || '3', 10),
    RETRY_DELAY_BASE: parseInt(process.env.RETRY_DELAY_BASE || '1000', 10),
    RETRY_DELAY_MAX: parseInt(process.env.RETRY_DELAY_MAX || '5000', 10),
    RETRY_JITTER: parseFloat(process.env.RETRY_JITTER || '0.2', 10)
  },

  // 缓存配置
  CACHE_CONFIG: {
    CACHE_DIR: process.env.CACHE_DIR || './cache',
    CACHE_INDEX_FILE: process.env.CACHE_INDEX_FILE || 'cache-index.json',
    CACHE_TTL: parseInt(process.env.CACHE_TTL || '3600000', 10), // 1小时
    MEMORY_CACHE_SIZE: parseInt(process.env.MEMORY_CACHE_SIZE || '100', 10), // 项目数
    CACHE_MAX_SIZE: parseInt(process.env.CACHE_MAX_SIZE || '1073741824', 10), // 1GB
    CACHE_CLEANUP_INTERVAL: parseInt(process.env.CACHE_CLEANUP_INTERVAL || '3600000', 10), // 1小时
    CACHE_FILE_EXT: process.env.CACHE_FILE_EXT || '.cache'
  }
};

// 验证必需的配置项
function validateConfig() {
  const requiredConfigs = [
    'UPSTREAM_SERVERS',
    'TMDB_API_KEY'
  ];

  const missingConfigs = requiredConfigs.filter(key => !config[key]);
  
  if (missingConfigs.length > 0) {
    throw new Error(`缺少必需的配置项: ${missingConfigs.join(', ')}`);
  }
}

// 在导出之前验证配置
validateConfig();

module.exports = config;