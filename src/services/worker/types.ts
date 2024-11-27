export interface WorkerMessage {
  type: 'request';
  url: string;
  timeout: number;
  requestId?: string;
}

export interface WorkerResponse {
  success: boolean;
  requestId?: string;
  data?: any;
  error?: string;
  responseTime?: number;
  response?: {
    data: any;
    contentType: string;
    responseTime: number;
  };
}

export interface IWorkerPool {
  initialize(): Promise<void>;
  execute(task: WorkerMessage): Promise<WorkerResponse>;
  cleanup(): Promise<void>;
}

export interface WorkerConfig {
  upstreamType: string;
  tmdbApiKey?: string;
  requestTimeout: number;
  upstreamServers: Array<{
    url: string;
    weight: number;
  }>;
}