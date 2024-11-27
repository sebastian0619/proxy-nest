import { Worker } from 'worker_threads';
import path from 'path';
import { WorkerMessage, WorkerResponse } from './types';
import { appConfig } from '../../config';

export class WorkerPool {
  private workers: Map<number, Worker> = new Map();
  private readonly numWorkers: number;
  private readonly timeout: number;

  constructor(options: { numWorkers: number; timeout: number }) {
    this.numWorkers = options.numWorkers;
    this.timeout = options.timeout;
  }

  public async initialize(): Promise<void> {
    const workerPath = path.join(__dirname, 'worker.js');

    for (let i = 0; i < this.numWorkers; i++) {
      const worker = new Worker(workerPath, {
        workerData: {
          id: i,
          config: appConfig
        }
      });

      this.workers.set(i, worker);

      // 错误处理
      worker.on('error', (error) => {
        console.error(`工作线程 ${i} 发生错误:`, error);
        this.restartWorker(i);
      });
    }
  }

  private async restartWorker(id: number): Promise<void> {
    const oldWorker = this.workers.get(id);
    if (oldWorker) {
      oldWorker.terminate();
      this.workers.delete(id);
    }

    await this.initialize();
  }

  public async handleRequest(request: { url: string; requestId: string }): Promise<WorkerResponse> {
    const workerId = Math.floor(Math.random() * this.numWorkers);
    const worker = this.workers.get(workerId);

    if (!worker) {
      throw new Error('没有可用的工作线程');
    }

    return new Promise((resolve, reject) => {
      const timeoutId = setTimeout(() => {
        reject(new Error('工作线程响应超时'));
      }, this.timeout);

      const messageHandler = (response: WorkerResponse) => {
        if (response.requestId === request.requestId) {
          clearTimeout(timeoutId);
          worker.removeListener('message', messageHandler);
          
          if (response.error) {
            reject(new Error(response.error));
          } else {
            resolve(response);
          }
        }
      };

      worker.on('message', messageHandler);

      const message: WorkerMessage = {
        type: 'request',
        requestId: request.requestId,
        url: request.url
      };

      worker.postMessage(message);
    });
  }

  public async cleanup(): Promise<void> {
    const workers = Array.from(this.workers.values());
    await Promise.all(workers.map(worker => worker.terminate()));
    this.workers.clear();
  }
}