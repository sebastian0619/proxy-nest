const fs = require('fs').promises;
const path = require('path');
const { LRUCache } = require('./utils');

const CACHE_DIR = process.env.CACHE_DIR || path.join(process.cwd(), 'cache');
const CACHE_INDEX_FILE = 'cache_index.json';
const MEMORY_CACHE_SIZE = parseInt(process.env.MEMORY_CACHE_SIZE || '100');
const CACHE_TTL = parseInt(process.env.CACHE_TTL || '3600000');

const diskCache = new Map();
const lruCache = new LRUCache(MEMORY_CACHE_SIZE);

async function initializeCache() {
  await fs.mkdir(CACHE_DIR, { recursive: true });
  try {
    const indexData = await fs.readFile(path.join(CACHE_DIR, CACHE_INDEX_FILE));
    const index = JSON.parse(indexData);
    for (const [key, meta] of Object.entries(index)) {
      if (Date.now() - meta.timestamp <= CACHE_TTL) {
        diskCache.set(key, meta);
      }
    }
    console.log(`[ 缓存信息 ] 已加载 ${diskCache.size} 个缓存项`);
  } catch (error) {
    console.error(`[ 错误 ] 加载缓存索引失败: ${error.message}`);
    await saveIndex();
  }
}

async function getCacheItem(key) {
  const memoryItem = lruCache.get(key);
  if (memoryItem && Date.now() - memoryItem.timestamp <= CACHE_TTL) {
    console.log(`[ 缓存命中 ] 内存缓存命中: ${key}`);
    return memoryItem;
  }

  const diskItem = diskCache.get(key);
  if (!diskItem || Date.now() - diskItem.timestamp > CACHE_TTL) {
    console.log(`[ 缓存未命中 ] 缓存未命中: ${key}`);
    return null;
  }

  try {
    const data = await fs.readFile(path.join(CACHE_DIR, diskItem.filename));
    const cacheItem = {
      data,
      contentType: diskItem.contentType,
      timestamp: Date.now()
    };
    lruCache.set(key, cacheItem);
    console.log(`[ 缓存命中 ] 磁盘缓存命中: ${key}`);
    return cacheItem;
  } catch (error) {
    console.error(`[ 错误 ] 缓存读取失败: ${error.message}`);
    await deleteCacheItem(key);
    return null;
  }
}

async function setCacheItem(key, data, contentType) {
  const timestamp = Date.now();
  const filename = Buffer.from(key).toString('base64') + '.cache';

  try {
    await fs.writeFile(path.join(CACHE_DIR, filename), data);
    diskCache.set(key, { filename, contentType, timestamp });
    lruCache.set(key, { data, contentType, timestamp });
    await saveIndex();
    console.log(`[ 缓存信息 ] 缓存写入成功 - 键值: ${key}`);
  } catch (error) {
    console.error(`[ 错误 ] 缓存写入失败: ${error.message}`);
  }
}

async function saveIndex() {
  try {
    const index = {};
    for (const [key, value] of diskCache.entries()) {
      index[key] = {
        filename: value.filename,
        contentType: value.contentType,
        timestamp: value.timestamp
      };
    }
    await fs.writeFile(path.join(CACHE_DIR, CACHE_INDEX_FILE), JSON.stringify(index, null, 2));
    console.log(`[ 缓存信息 ] 缓存索引已保存，共 ${diskCache.size} 项`);
  } catch (error) {
    console.error(`[ 错误 ] 保存缓存索引失败: ${error.message}`);
  }
}

async function deleteCacheItem(key) {
  const item = diskCache.get(key);
  if (item) {
    try {
      await fs.unlink(path.join(CACHE_DIR, item.filename));
      diskCache.delete(key);
      lruCache.cache.delete(key);
      await saveIndex();
      console.log(`[ 缓存信息 ] 缓存项已删除: ${key}`);
    } catch (error) {
      console.error(`[ 错误 ] 删除缓存失败: ${error.message}`);
    }
  }
}

module.exports = { initializeCache, getCacheItem, setCacheItem };