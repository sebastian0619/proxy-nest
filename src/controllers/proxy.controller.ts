import { Response, NextFunction } from 'express';
import { ProxyRequest, ProxyResponse, ProxyControllerDeps } from './types';
import { CacheItem } from '../types';
import { validateResponse } from '../utils/validation';
import { ICacheService } from '../services/cache/types';
import { IWorkerPool } from '../services/worker/types';

export class ProxyController {
  private diskCache: ICacheService;
  private memoryCache: ICacheService;
  private workerPool: IWorkerPool;

  constructor(deps: ProxyControllerDeps) {
    this.diskCache = deps.diskCache;
    this.memoryCache = deps.memoryCache;
    this.workerPool = deps.workerPool;
  }

  private async getCachedResponse(key: string): Promise<CacheItem | null> {
    // 先检查内存缓存
    let cachedItem = await this.memoryCache.get(key);
    if (cachedItem) {
      return cachedItem;
    }

    // 如果内存缓存没有，检查磁盘缓存
    cachedItem = await this.diskCache.get(key);
    if (cachedItem) {
      // 如果在磁盘缓存中找到，也放入内存缓存
      await this.memoryCache.set(key, cachedItem);
      return cachedItem;
    }

    return null;
  }

  async handleRequest(req: ProxyRequest, res: Response, next: NextFunction): Promise<void> {
    try {
      const cacheKey = req.cacheKey;
      if (!cacheKey) {
        throw new Error('Missing cache key');
      }

      // 尝试从缓存获取
      const cachedResponse = await this.getCachedResponse(cacheKey);
      if (cachedResponse) {
        res.setHeader('Content-Type', cachedResponse.contentType);
        res.setHeader('X-Cache', 'HIT');
        res.send(cachedResponse.data);
        return;
      }

      // 如果缓存中没有，则请求上游服务器
      const response = await this.workerPool.execute({
        type: 'request',
        url: req.originalUrl,
        timeout: 5000
      });

      if (!response.success || !response.data) {
        throw new Error(response.error || 'Failed to fetch from upstream');
      }

      // 验证响应
      if (!validateResponse(response.data)) {
        throw new Error('Invalid response from upstream');
      }

      // 缓存响应
      const cacheItem: CacheItem = {
        data: response.data,
        contentType: 'application/json',
        timestamp: Date.now()
      };

      await Promise.all([
        this.memoryCache.set(cacheKey, cacheItem),
        this.diskCache.set(cacheKey, cacheItem)
      ]);

      // 发送响应
      res.setHeader('Content-Type', cacheItem.contentType);
      res.setHeader('X-Cache', 'MISS');
      res.send(cacheItem.data);

    } catch (error) {
      next(error);
    }
  }
}