import { CacheItem } from '../../types';
import { ICacheService, ICacheOptions } from './types';

export class MemoryCache implements ICacheService {
  private cache: Map<string, CacheItem>;
  private options: ICacheOptions;

  constructor(options: ICacheOptions) {
    this.cache = new Map();
    this.options = options;
  }

  async get(key: string): Promise<CacheItem | null> {
    const item = this.cache.get(key);
    if (!item) return null;

    // 检查是否过期
    if (Date.now() - item.timestamp > this.options.ttl) {
      this.cache.delete(key);
      return null;
    }

    return item;
  }

  async set(key: string, value: CacheItem): Promise<void> {
    // 如果缓存已满，删除最旧的项
    if (this.cache.size >= this.options.maxSize) {
      const oldestKey = this.cache.keys().next().value;
      this.cache.delete(oldestKey);
    }

    this.cache.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.cache.delete(key);
  }

  async clear(): Promise<void> {
    this.cache.clear();
  }

  async has(key: string): Promise<boolean> {
    return this.cache.has(key);
  }

  async cleanup(): Promise<void> {
    this.cache.clear();
  }
}