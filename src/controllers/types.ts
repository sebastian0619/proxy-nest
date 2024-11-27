import { Request, Response, NextFunction } from 'express';
import { CacheItem, ICacheService, IWorkerPool } from '../types';

export interface ProxyResponse {
  data: Buffer | string | object;
  contentType: string;
  responseTime?: number;
}

export interface ProxyRequest extends Request {
  cacheKey?: string;
}

export interface ProxyControllerDeps {
  diskCache: ICacheService;
  memoryCache: ICacheService;
  workerPool: IWorkerPool;
}