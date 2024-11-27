import { Request, Response, NextFunction } from 'express';
import crypto from 'crypto';
import { ProxyRequest } from '../controllers/types';

export const generateCacheKey = (
  req: ProxyRequest,
  res: Response,
  next: NextFunction
) => {
  const normalizedUrl = req.originalUrl.toLowerCase();
  
  // 生成缓存键
  const hash = crypto
    .createHash('md5')
    .update(normalizedUrl)
    .digest('hex');
    
  req.cacheKey = hash;
  next();
};