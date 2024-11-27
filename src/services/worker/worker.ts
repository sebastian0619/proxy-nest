import { parentPort, workerData } from 'worker_threads';
import axios from 'axios';
import { WorkerMessage, WorkerResponse } from './types';

const { id, config } = workerData;

if (parentPort) {
  parentPort.on('message', async (message: WorkerMessage) => {
    try {
      const start = Date.now();
      
      // 选择上游服务器
      const upstreamUrl = config.upstreamServers[0].url; // 简化版本
      
      const response = await axios({
        url: `${upstreamUrl}${message.url}`,
        method: 'GET',
        timeout: config.requestTimeout,
        responseType: 'arraybuffer',
        headers: {
          ...(config.tmdbApiKey && {
            'Authorization': `Bearer ${config.tmdbApiKey}`
          })
        }
      });

      const workerResponse: WorkerResponse = {
        success: true,
        requestId: message.requestId,
        data: response.data,
        responseTime: Date.now() - start,
        response: {
          data: response.data,
          contentType: response.headers['content-type'],
          responseTime: Date.now() - start
        }
      };

      parentPort.postMessage(workerResponse);

    } catch (error) {
      parentPort.postMessage({
        success: false,
        requestId: message.requestId,
        error: error instanceof Error ? error.message : 'Unknown error'
      } as WorkerResponse);
    }
  });
}