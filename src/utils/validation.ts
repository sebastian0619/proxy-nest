import { CacheItem } from '../types';

export function validateResponse(response: any): boolean {
  if (!response || typeof response !== 'object') {
    return false;
  }

  // 添加你的验证逻辑
  return true;
}