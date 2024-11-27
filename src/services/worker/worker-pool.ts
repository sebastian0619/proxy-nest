import { Worker } from 'worker_threads';
import path from 'path';
import { WorkerMessage, WorkerResponse, IWorkerPool } from './types';
import { appConfig } from '../../config';

export class WorkerPool implements IWorkerPool {
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
          workerId: i,
          timeout: this.timeout,
          ...appConfig
        }
      });

      this.workers.set(i, worker);
    }
  }

  public async execute(task: WorkerMessage): Promise<WorkerResponse> {
    const workerId = Math.floor(Math.random() * this.numWorkers);
    const worker = this.workers.get(workerId);

    if (!worker) {
      throw new Error('Worker not found');
    }

    return new Promise((resolve, reject) => {
      const timeoutId = setTimeout(() => {
        reject(new Error('Worker timeout'));
      }, this.timeout);

      worker.once('message', (response: WorkerResponse) => {
        clearTimeout(timeoutId);
        resolve(response);
      });

      worker.once('error', (error) => {
        clearTimeout(timeoutId);
        reject(error);
      });

      worker.postMessage(task);
    });
  }

  public async cleanup(): Promise<void> {
    const terminationPromises = Array.from(this.workers.values()).map(worker => {
      return new Promise<void>((resolve) => {
        worker.once('exit', () => resolve());
        worker.terminate();
      });
    });

    await Promise.all(terminationPromises);
    this.workers.clear();
  }
}