import express, { Application } from 'express';
import { setupMiddlewares } from './middlewares';
import { createProxyController } from './controllers/proxy.controller';
import { DiskCache } from './services/cache/disk-cache';
import { MemoryCache } from './services/cache/memory-cache';
import { WorkerPool } from './services/worker/worker-pool';
import { appConfig, cacheConfig } from './config';

export class App {
  private app: Application;
  private diskCache: DiskCache;
  private memoryCache: MemoryCache;
  private workerPool: WorkerPool;
  private server: http.Server | null = null;

  constructor() {
    this.app = express();
  }

  private async initializeCaches(): Promise<void> {
    this.diskCache = new DiskCache(
      cacheConfig.cacheDir,
      cacheConfig.indexFile,
      {
        ttl: cacheConfig.ttl,
        maxSize: cacheConfig.maxSize
      }
    );

    this.memoryCache = new MemoryCache({
      ttl: cacheConfig.ttl,
      maxSize: cacheConfig.memoryCacheSize
    });
  }

  private async initializeWorkerPool(): Promise<void> {
    this.workerPool = new WorkerPool({
      numWorkers: appConfig.numWorkers,
      timeout: appConfig.requestTimeout
    });
    await this.workerPool.initialize();
  }

  private setupRoutes(): void {
    const proxyController = createProxyController({
      diskCache: this.diskCache,
      memoryCache: this.memoryCache,
      workerPool: this.workerPool
    });

    // 主代理路由
    this.app.use('/', proxyController.handleRequest);
  }

  public async initialize(): Promise<void> {
    // 初始化中间件
    setupMiddlewares(this.app);

    // 初始化缓存
    await this.initializeCaches();

    // 初始化工作线程池
    await this.initializeWorkerPool();

    // 设置路由
    this.setupRoutes();
  }

  public getApp(): Application {
    return this.app;
  }

  public async cleanup(): Promise<void> {
    await this.workerPool.cleanup();
  }

  public async start(port: number): Promise<void> {
    this.server = this.app.listen(port, () => {
      console.log(`服务器启动在端口 ${port}`);
    });

    // 设置优雅关闭
    this.setupGracefulShutdown();
  }

  private setupGracefulShutdown(): void {
    // SIGTERM - Docker 发出的默认终止信号
    process.on('SIGTERM', async () => {
      console.log('收到 SIGTERM 信号，开始优雅关闭...');
      await this.shutdown();
    });

    // SIGINT - 用户按 Ctrl+C
    process.on('SIGINT', async () => {
      console.log('收到 SIGINT 信号，开始优雅关闭...');
      await this.shutdown();
    });
  }

  private async shutdown(): Promise<void> {
    try {
      // 1. 停止接收新请求
      if (this.server) {
        await new Promise((resolve) => {
          this.server?.close(resolve);
        });
      }

      // 2. 等待当前请求处理完成（给定超时时间）
      const forceShutdownTimeout = setTimeout(() => {
        console.log('强制关闭服务器（超时）');
        process.exit(1);
      }, 30000); // 30秒超时

      // 3. 清理资源
      await Promise.all([
        this.workerPool.cleanup(),  // 关闭所有工作线程
        this.diskCache.cleanup(),   // 保存缓存状态
        this.memoryCache.cleanup()  // 清理内存缓存
      ]);

      // 4. 取消超时
      clearTimeout(forceShutdownTimeout);

      console.log('服务器已安全关闭');
      process.exit(0);

    } catch (error) {
      console.error('关闭过程出错:', error);
      process.exit(1);
    }
  }
}