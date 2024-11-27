import { App } from './app';
import { appConfig } from './config';

async function startServer() {
  try {
    const app = new App();
    await app.initialize();

    const server = app.getApp().listen(appConfig.port, () => {
      console.log(`服务器启动在端口 ${appConfig.port}`);
    });

    // 优雅关闭
    const shutdown = async () => {
      console.log('正在关闭服务器...');
      server.close();
      await app.cleanup();
      process.exit(0);
    };

    // 监听终止信号
    process.on('SIGTERM', shutdown);
    process.on('SIGINT', shutdown);

  } catch (error) {
    console.error('服务器启动失败:', error);
    process.exit(1);
  }
}

if (require.main === module) {
  startServer();
}

export { startServer };