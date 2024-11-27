export interface RetryConfig {
    maxRetries?: number;
    retryDelay?: number;
    timeout?: number;
  }
  
  export interface RetryableRequest<T> {
    execute: () => Promise<T>;
    validate?: (response: T) => boolean;
  }