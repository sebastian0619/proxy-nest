import fs from 'fs/promises';
import path from 'path';
import { CacheItem } from '../../types';
import { ICacheService, ICacheOptions } from './types';

export class DiskCache implements ICacheService {
  private cacheDir: string;
  private options: ICacheOptions;
  private indexFile: string;

  constructor(cacheDir: string, indexFile: string, options: ICacheOptions) {
    this.cacheDir = cacheDir;
    this.indexFile = path.join(cacheDir, indexFile);
    this.options = options;
    this.initialize();
  }

  private async initialize(): Promise<void> {
    await fs.mkdir(this.cacheDir, { recursive: true });
    try {
      await fs.access(this.indexFile);
    } catch {
      await fs.writeFile(this.indexFile, JSON.stringify({}));
    }
  }

  private getFilePath(key: string): string {
    return path.join(this.cacheDir, `${key}.cache`);
  }

  async get(key: string): Promise<CacheItem | null> {
    try {
      const filePath = this.getFilePath(key);
      const data = await fs.readFile(filePath);
      const item: CacheItem = JSON.parse(data.toString());

      if (Date.now() - item.timestamp > this.options.ttl) {
        await this.delete(key);
        return null;
      }

      return item;
    } catch {
      return null;
    }
  }

  async set(key: string, value: CacheItem): Promise<void> {
    const filePath = this.getFilePath(key);
    await fs.writeFile(filePath, JSON.stringify(value));

    // 更新索引
    const index = JSON.parse(await fs.readFile(this.indexFile, 'utf-8'));
    index[key] = { timestamp: value.timestamp };
    await fs.writeFile(this.indexFile, JSON.stringify(index));
  }

  async delete(key: string): Promise<void> {
    try {
      await fs.unlink(this.getFilePath(key));
      const index = JSON.parse(await fs.readFile(this.indexFile, 'utf-8'));
      delete index[key];
      await fs.writeFile(this.indexFile, JSON.stringify(index));
    } catch {
      // Ignore errors if file doesn't exist
    }
  }

  async clear(): Promise<void> {
    const files = await fs.readdir(this.cacheDir);
    await Promise.all(
      files.map(file => fs.unlink(path.join(this.cacheDir, file)))
    );
    await fs.writeFile(this.indexFile, JSON.stringify({}));
  }

  async has(key: string): Promise<boolean> {
    try {
      await fs.access(this.getFilePath(key));
      return true;
    } catch {
      return false;
    }
  }

  async cleanup(): Promise<void> {
    try {
      const files = await fs.readdir(this.cacheDir);
      for (const file of files) {
        if (file === path.basename(this.indexFile)) continue;
        
        const filePath = path.join(this.cacheDir, file);
        try {
          await fs.unlink(filePath);
        } catch (error) {
          console.error(`Failed to delete cache file ${filePath}:`, error);
        }
      }
      await fs.writeFile(this.indexFile, '{}');
    } catch (error) {
      console.error('Failed to cleanup disk cache:', error);
    }
  }
}