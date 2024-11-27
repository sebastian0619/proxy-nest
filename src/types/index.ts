// 基础配置接口
export interface AppConfig {
  port: number;
  numWorkers: number;
  requestTimeout: number;
  upstreamType: 'tmdb-api' | 'tmdb-image' | 'custom';
  tmdbApiKey?: string;
  tmdbImageTestUrl?: string;
  upstreamServers?: string[];
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

// 缓存服务接口
export interface ICacheService {
  get(key: string): Promise<CacheItem | null>;
  set(key: string, value: CacheItem): Promise<void>;
  cleanup(): Promise<void>;
}

// 工作池接口
export interface IWorkerPool {
  initialize(): Promise<void>;
  execute(task: any): Promise<any>;
  cleanup(): Promise<void>;
}
