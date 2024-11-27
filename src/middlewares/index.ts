import { Application } from 'express';
import { errorHandler, notFoundHandler, AppError } from './error.middleware';
import { setupLogging, requestLogger } from './logging.middleware';
import { generateCacheKey } from './cache-key.middleware';

export const setupMiddlewares = (app: Application): void => {
  // 基础中间件
  app.use(setupLogging());
  app.use(requestLogger);
  
  // 请求处理中间件
  app.use(generateCacheKey);
  
  // 错误处理中间件 (必须在最后)
  app.use(notFoundHandler);
  app.use(errorHandler);
};

export {
  AppError,
  errorHandler,
  notFoundHandler
} from './error.middleware';

export {
  requestLogger,
  setupLogging
} from './logging.middleware';

export {
  generateCacheKey
} from './cache-key.middleware';