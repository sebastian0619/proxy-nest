import { CacheItem } from '../../types';

export interface ICacheService {
  get(key: string): Promise<CacheItem | null>;
  set(key: string, value: CacheItem): Promise<void>;
  delete(key: string): Promise<void>;
  clear(): Promise<void>;
  has(key: string): Promise<boolean>;
}

export interface ICacheOptions {
  ttl: number;
  maxSize: number;
}