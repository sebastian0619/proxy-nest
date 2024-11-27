import axios from 'axios';
import { LoadBalancer } from './index';
import { UpstreamServer } from '../../types';
import { appConfig } from '../../config';

export class HealthChecker {
  private loadBalancer: LoadBalancer;
  private checkInterval: number;
  private timer: NodeJS.Timeout | null = null;

  constructor(loadBalancer: LoadBalancer, checkInterval = 30000) {
    this.loadBalancer = loadBalancer;
    this.checkInterval = checkInterval;
  }

  public start(): void {
    this.timer = setInterval(() => this.checkAll(), this.checkInterval);
  }

  public stop(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  private async checkServer(server: UpstreamServer): Promise<void> {
    try {
      const testUrl = this.getTestUrl();
      const start = Date.now();
      
      await axios.get(`${server.url}${testUrl}`, {
        timeout: appConfig.requestTimeout,
        headers: {
          ...(appConfig.tmdbApiKey && {
            'Authorization': `Bearer ${appConfig.tmdbApiKey}`
          })
        }
      });

      const responseTime = Date.now() - start;
      this.loadBalancer.updateServerWeight(server.url, responseTime);
      this.loadBalancer.markServerHealthy(server.url);

    } catch (error) {
      console.error(`健康检查失败 - ${server.url}:`, error.message);
      this.loadBalancer.markServerUnhealthy(server.url);
    }
  }

  private async checkAll(): Promise<void> {
    const servers = await this.getServers();
    await Promise.all(servers.map(server => this.checkServer(server)));
  }

  private getTestUrl(): string {
    switch (appConfig.upstreamType) {
      case 'tmdb-api':
        return '/3/configuration';
      case 'tmdb-image':
        if (!appConfig.tmdbImageTestUrl) {
          throw new Error('TMDB_IMAGE_TEST_URL 未设置');
        }
        return appConfig.tmdbImageTestUrl;
      case 'custom':
        return '/';
      default:
        throw new Error(`未知的上游类型: ${appConfig.upstreamType}`);
    }
  }

  private async getServers(): Promise<UpstreamServer[]> {
    // 这里可以从配置或数据库获取服务器列表
    return [];
  }
}