import { Request, Response, NextFunction } from 'express';

export class AppError extends Error {
  constructor(
    public statusCode: number,
    public message: string,
    public isOperational = true
  ) {
    super(message);
    Object.setPrototypeOf(this, AppError.prototype);
  }
}

export const errorHandler = (
  err: Error | AppError,
  req: Request,
  res: Response,
  next: NextFunction
) => {
  if (err instanceof AppError) {
    return res.status(err.statusCode).json({
      status: 'error',
      message: err.message
    });
  }

  // 未知错误
  console.error('未知错误:', err);
  return res.status(500).json({
    status: 'error',
    message: '服务器内部错误'
  });
};

export const notFoundHandler = (
  req: Request,
  res: Response
) => {
  res.status(404).json({
    status: 'error',
    message: '请求的资源不存在'
  });
};