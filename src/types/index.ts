// 基础配置接口
export interface AppConfig {
  port: number;
  numWorkers: number;
  requestTimeout: number;
  upstreamType: 'tmdb-api' | 'tmdb-image' | 'custom';
  tmdbApiKey?: string;
  tmdbImageTestUrl?: string;
}

// 缓存配置接口
export interface CacheConfig {
  cacheDir: string;
  indexFile: string;
  ttl: number;
  memoryCacheSize: number;
  maxSize: number;
  cleanupInterval: number;
  fileExt: string;
}

// 上游服务器接口
export interface UpstreamServer {
  url: string;
  healthy: boolean;
  baseWeight: number;
  dynamicWeight: number;
  alpha: number;
  responseTimes: number[];
  lastResponseTime: number;
  lastEWMA: number;
}

// 缓存项接口
export interface CacheItem {
  data: Buffer | object | string;
  contentType: string;
  timestamp: number;
}
