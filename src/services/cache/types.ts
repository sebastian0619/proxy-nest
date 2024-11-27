import { CacheItem } from '../../types';

export interface ICacheOptions {
  ttl: number;
  maxSize: number;
}

export interface ICacheService {
  get(key: string): Promise<CacheItem | null>;
  set(key: string, value: CacheItem): Promise<void>;
  cleanup(): Promise<void>;
}