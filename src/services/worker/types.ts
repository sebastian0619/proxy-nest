export interface WorkerMessage {
    type: 'request';
    requestId: string;
    url: string;
  }
  
  export interface WorkerResponse {
    requestId: string;
    response?: {
      data: Buffer | string | object;
      contentType: string;
      responseTime: number;
    };
    error?: string;
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