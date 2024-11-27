import path from 'path';
import { AppConfig, CacheConfig } from '../types';

export const appConfig: AppConfig = {
  port: parseInt(process.env.PORT || '6635'),
  numWorkers: parseInt(process.env.NUM_WORKERS || '4'),
  requestTimeout: parseInt(process.env.REQUEST_TIMEOUT || '5000'),
  upstreamType: (process.env.UPSTREAM_TYPE || 'tmdb-api') as AppConfig['upstreamType'],
  tmdbApiKey: process.env.TMDB_API_KEY,
  tmdbImageTestUrl: process.env.TMDB_IMAGE_TEST_URL,
  upstreamServers: (process.env.UPSTREAM_SERVERS || '')
    .split(',')
    .filter(Boolean)
    .map(url => url.trim())
};

export const cacheConfig: CacheConfig = {
  cacheDir: process.env.CACHE_DIR || path.join(process.cwd(), 'cache'),
  indexFile: process.env.CACHE_INDEX_FILE || 'cache-index.json',
  ttl: parseInt(process.env.CACHE_TTL || '3600'),
  memoryCacheSize: parseInt(process.env.MEMORY_CACHE_SIZE || '100'),
  maxSize: parseInt(process.env.CACHE_MAX_SIZE || '1073741824'), // 1GB
  cleanupInterval: parseInt(process.env.CACHE_CLEANUP_INTERVAL || '300'),
  fileExt: process.env.CACHE_FILE_EXT || '.cache'
};