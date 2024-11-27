import { Request, Response, NextFunction } from 'express';
import morgan from 'morgan';

export const setupLogging = () => {
  // 自定义日志格式
  morgan.token('cache-status', (req: any) => req.cacheHit ? 'HIT' : 'MISS');
  
  const logFormat = ':remote-addr - :remote-user [:date[clf]] ":method :url HTTP/:http-version" :status :res[content-length] ":referrer" ":user-agent" :cache-status';

  return morgan(logFormat, {
    stream: {
      write: (message: string) => {
        console.log(message.trim());
      }
    }
  });
};

export const requestLogger = (
  req: Request,
  res: Response,
  next: NextFunction
) => {
  const start = Date.now();
  
  res.on('finish', () => {
    const duration = Date.now() - start;
    console.log(`${req.method} ${req.originalUrl} ${res.statusCode} ${duration}ms`);
  });

  next();
};