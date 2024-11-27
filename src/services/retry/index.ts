import { RetryConfig, RetryableRequest } from './types';

export class RetryService {
  private readonly maxRetries: number;
  private readonly retryDelay: number;
  private readonly timeout: number;

  constructor(config: RetryConfig = {}) {
    this.maxRetries = config.maxRetries || 3;
    this.retryDelay = config.retryDelay || 1000;
    this.timeout = config.timeout || 5000;
  }

  async execute<T>({ execute, validate }: RetryableRequest<T>): Promise<T> {
    let retryCount = 0;
    
    while (true) {
      try {
        const result = await Promise.race([
          execute(),
          new Promise<never>((_, reject) => 
            setTimeout(() => reject(new Error('请求超时')), this.timeout)
          )
        ]) as T;

        if (validate && !validate(result)) {
          throw new Error('响应验证失败');
        }

        return result;

      } catch (error) {
        retryCount++;
        if (error instanceof Error) {
          console.error(`请求失败 (${retryCount}/${this.maxRetries}): ${error.message}`);
        } else {
          console.error(`请求失败 (${retryCount}/${this.maxRetries}): Unknown error`);
        }

        if (retryCount >= this.maxRetries) {
          throw error;
        }

        await new Promise(resolve => setTimeout(resolve, this.retryDelay));
      }
    }
  }
}