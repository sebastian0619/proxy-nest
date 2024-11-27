import { Response, NextFunction } from 'express';
import { ProxyRequest, ProxyResponse, ProxyControllerDeps } from './types';
import { CacheItem } from '../types';
import { validateResponse } from '../utils/validation';

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

    // 检查磁盘缓存
    cachedItem = await this.diskCache.get(key);
    if (cachedItem) {
      // 将磁盘缓存项加入内存缓存
      await this.memoryCache.set(key, cachedItem);
      return cachedItem;
    }

    return null;
  }

  private async cacheResponse(key: string, response: ProxyResponse): Promise<void> {
    const cacheItem: CacheItem = {
      data: response.data,
      contentType: response.contentType,
      timestamp: Date.now()
    };

    // 并行写入两级缓存
    await Promise.all([
      this.diskCache.set(key, cacheItem),
      this.memoryCache.set(key, cacheItem)
    ]);
  }

  private sendResponse(res: Response, item: CacheItem | ProxyResponse): void {
    res.setHeader('Content-Type', item.contentType);
    
    if (Buffer.isBuffer(item.data)) {
      res.end(item.data);
    } else if (typeof item.data === 'string') {
      res.send(item.data);
    } else {
      res.json(item.data);
    }
  }

  public handleRequest = async (
    req: ProxyRequest,
    res: Response,
    next: NextFunction
  ): Promise<void> => {
    try {
      const cacheKey = req.cacheKey;
      if (!cacheKey) {
        throw new Error('Missing cache key');
      }

      // 尝试获取缓存响应
      const cachedItem = await this.getCachedResponse(cacheKey);
      if (cachedItem) {
        this.sendResponse(res, cachedItem);
        return;
      }

      // 通过工作线程处理新请求
      const response = await this.workerPool.handleRequest({
        url: req.originalUrl,
        requestId: cacheKey
      });

      // 验证响应
      if (!validateResponse(response.data, response.contentType)) {
        throw new Error('Invalid upstream response');
      }

      // 缓存响应
      await this.cacheResponse(cacheKey, response);

      // 发送响应
      this.sendResponse(res, response);

    } catch (error) {
      next(error);
    }
  };
}