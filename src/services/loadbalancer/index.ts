import { UpstreamServer } from '../../types';

export class LoadBalancer {
  private servers: UpstreamServer[];
  
  constructor(servers: UpstreamServer[]) {
    this.servers = servers;
  }

  public updateServerWeight(serverUrl: string, responseTime: number): void {
    const server = this.servers.find(s => s.url === serverUrl);
    if (!server) return;

    // 更新响应时间历史
    server.responseTimes.push(responseTime);
    if (server.responseTimes.length > 10) {
      server.responseTimes.shift();
    }

    // 更新 EWMA
    const alpha = server.alpha;
    server.lastEWMA = server.lastEWMA === 0
      ? responseTime
      : alpha * responseTime + (1 - alpha) * server.lastEWMA;

    // 更新权重
    server.baseWeight = this.calculateBaseWeight(server);
    server.dynamicWeight = this.calculateDynamicWeight(server);
  }

  private calculateBaseWeight(server: UpstreamServer): number {
    const avgResponseTime = server.responseTimes.reduce((a, b) => a + b, 0) / server.responseTimes.length;
    return Math.max(1, 1000 / avgResponseTime);
  }

  private calculateDynamicWeight(server: UpstreamServer): number {
    return server.healthy ? 1 : 0;
  }

  public selectServer(): UpstreamServer {
    const healthyServers = this.servers.filter(s => s.healthy);
    if (healthyServers.length === 0) {
      throw new Error('没有健康的服务器可用');
    }

    // 计算总权重
    const totalWeight = healthyServers.reduce((sum, server) => {
      const weight = server.baseWeight * server.dynamicWeight;
      return sum + weight;
    }, 0);

    // 随机选择服务器
    let random = Math.random() * totalWeight;
    for (const server of healthyServers) {
      const weight = server.baseWeight * server.dynamicWeight;
      random -= weight;
      if (random <= 0) {
        return server;
      }
    }

    return healthyServers[0];
  }

  public markServerUnhealthy(serverUrl: string): void {
    const server = this.servers.find(s => s.url === serverUrl);
    if (server) {
      server.healthy = false;
    }
  }

  public markServerHealthy(serverUrl: string): void {
    const server = this.servers.find(s => s.url === serverUrl);
    if (server) {
      server.healthy = true;
    }
  }
}