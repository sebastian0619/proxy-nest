package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"main/config"
	"main/logger"

	"github.com/redis/go-redis/v9"
)

// RedisCache Redis缓存实现
type RedisCache struct {
	client *redis.ClusterClient
	config *config.CacheConfig
	ctx    context.Context
}

// NewRedisCache 创建Redis缓存
func NewRedisCache(cfg *config.CacheConfig) (*RedisCache, error) {
	if !cfg.UseRedis {
		return nil, fmt.Errorf("Redis缓存未启用")
	}

	// 创建Redis集群客户端
	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        cfg.RedisClusterNodes,
		Password:     cfg.RedisPassword,
		PoolSize:     cfg.RedisPoolSize,
		MinIdleConns: cfg.RedisMinIdleConns,
		DialTimeout:  cfg.RedisConnectTimeout,
		ReadTimeout:  cfg.RedisReadTimeout,
		WriteTimeout: cfg.RedisWriteTimeout,
		IdleTimeout:  cfg.RedisIdleTimeout,
		MaxRetries:   cfg.RedisMaxRetries,
		MinRetryDelay: cfg.RedisRetryDelay,
	})

	ctx := context.Background()

	// 测试连接
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("Redis连接失败: %w", err)
	}

	logger.Info("Redis集群连接成功，节点: %v", cfg.RedisClusterNodes)

	return &RedisCache{
		client: client,
		config: cfg,
		ctx:    ctx,
	}, nil
}

// Get 获取缓存
func (rc *RedisCache) Get(key string) (*CacheItem, error) {
	if rc.client == nil {
		return nil, nil
	}

	// 生成Redis键
	redisKey := rc.generateRedisKey(key)

	// 从Redis获取数据
	data, err := rc.client.Get(rc.ctx, redisKey).Result()
	if err != nil {
		if err == redis.Nil {
			// 缓存未命中
			return nil, nil
		}
		logger.Error("Redis读取失败: %v", err)
		return nil, err
	}

	// 反序列化缓存项
	var item CacheItem
	if err := json.Unmarshal([]byte(data), &item); err != nil {
		logger.Error("Redis数据反序列化失败: %v", err)
		// 删除损坏的缓存
		rc.client.Del(rc.ctx, redisKey)
		return nil, nil
	}

	// 检查是否过期（双重保险）
	if time.Now().After(item.ExpireAt) {
		rc.Delete(key)
		return nil, nil
	}

	// 更新最后访问时间（异步更新，不阻塞读取）
	go func() {
		item.LastAccessed = time.Now()
		data, _ := json.Marshal(item)
		rc.client.Set(context.Background(), redisKey, data, time.Until(item.ExpireAt))
	}()

	return &item, nil
}

// Set 设置缓存
func (rc *RedisCache) Set(key string, value *CacheItem, contentType string) error {
	if rc.client == nil {
		return nil
	}

	// 获取过期时间
	expireTime := rc.config.DiskCacheTTL
	if contentType != "" {
		mimeCategory := strings.Split(contentType, ";")[0]
		if typeConfig, exists := rc.config.ContentTypeConfig[mimeCategory]; exists {
			expireTime = typeConfig.DiskTTL
		}
	}

	// 创建缓存项
	item := &CacheItem{
		Data:         value.Data,
		ContentType:  value.ContentType,
		CreatedAt:    time.Now(),
		ExpireAt:     time.Now().Add(expireTime),
		LastAccessed: time.Now(),
		IsImage:      strings.HasPrefix(contentType, "image/"),
	}

	// 序列化数据
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("Redis数据序列化失败: %w", err)
	}

	// 生成Redis键
	redisKey := rc.generateRedisKey(key)

	// 存储到Redis
	if err := rc.client.Set(rc.ctx, redisKey, data, expireTime).Err(); err != nil {
		logger.Error("Redis写入失败: %v", err)
		return err
	}

	return nil
}

// Delete 删除缓存
func (rc *RedisCache) Delete(key string) error {
	if rc.client == nil {
		return nil
	}

	redisKey := rc.generateRedisKey(key)
	return rc.client.Del(rc.ctx, redisKey).Err()
}

// GetStats 获取Redis缓存统计信息
func (rc *RedisCache) GetStats() *CacheStats {
	if rc.client == nil {
		return &CacheStats{}
	}

	// 获取Redis信息
	info, err := rc.client.Info(rc.ctx, "memory", "keyspace").Result()
	if err != nil {
		logger.Error("获取Redis统计信息失败: %v", err)
		return &CacheStats{}
	}

	stats := &CacheStats{}

	// 解析Redis信息
	lines := strings.Split(info, "\r\n")
	for _, line := range lines {
		if strings.Contains(line, "used_memory:") {
			if parts := strings.Split(line, ":"); len(parts) == 2 {
				if size, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
					stats.TotalSize = size
				}
			}
		}
		if strings.Contains(line, "keys=") {
			// 解析类似 "db0:keys=1000,expires=800" 的格式
			if parts := strings.Split(line, "keys="); len(parts) > 1 {
				if keysPart := strings.Split(parts[1], ","); len(keysPart) > 0 {
					if keys, err := strconv.Atoi(keysPart[0]); err == nil {
						stats.TotalFiles = keys
						stats.CurrentSize = keys
					}
				}
			}
		}
	}

	return stats
}

// Clear 清空Redis缓存
func (rc *RedisCache) Clear() error {
	if rc.client == nil {
		return nil
	}

	// 获取所有匹配的键
	prefix := rc.generateRedisKey("")
	keys, err := rc.client.Keys(rc.ctx, prefix+"*").Result()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		return nil
	}

	// 批量删除
	pipeline := rc.client.Pipeline()
	for _, key := range keys {
		pipeline.Del(rc.ctx, key)
	}

	if _, err := pipeline.Exec(rc.ctx); err != nil {
		logger.Error("Redis批量删除失败: %v", err)
		return err
	}

	logger.Info("Redis缓存已清空，删除了 %d 个键", len(keys))
	return nil
}

// GetKeys 获取缓存键列表
func (rc *RedisCache) GetKeys(limitStr, offsetStr string) ([]string, error) {
	if rc.client == nil {
		return []string{}, nil
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 100
	}

	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		offset = 0
	}

	// 获取所有匹配的键
	prefix := rc.generateRedisKey("")
	keys, err := rc.client.Keys(rc.ctx, prefix+"*").Result()
	if err != nil {
		return nil, err
	}

	// 移除前缀
	var cleanKeys []string
	for _, key := range keys {
		if cleanKey := strings.TrimPrefix(key, prefix); cleanKey != key {
			cleanKeys = append(cleanKeys, cleanKey)
		}
	}

	// 应用分页
	if offset >= len(cleanKeys) {
		return []string{}, nil
	}

	end := offset + limit
	if end > len(cleanKeys) {
		end = len(cleanKeys)
	}

	return cleanKeys[offset:end], nil
}

// Search 搜索缓存
func (rc *RedisCache) Search(query string) ([]*CacheMeta, error) {
	if rc.client == nil {
		return []*CacheMeta{}, nil
	}

	// 获取所有匹配的键
	prefix := rc.generateRedisKey("")
	keys, err := rc.client.Keys(rc.ctx, prefix+"*").Result()
	if err != nil {
		return nil, err
	}

	var results []*CacheMeta
	queryLower := strings.ToLower(query)

	for _, key := range keys {
		cleanKey := strings.TrimPrefix(key, prefix)
		
		// 搜索键名
		if strings.Contains(strings.ToLower(cleanKey), queryLower) {
			// 获取缓存项以获取元数据
			if item, err := rc.Get(cleanKey); err == nil && item != nil {
				meta := &CacheMeta{
					Key:          cleanKey,
					FilePath:     key, // 使用Redis键作为"文件路径"
					Size:         len(fmt.Sprintf("%v", item.Data)),
					CreatedAt:    item.CreatedAt,
					ExpireAt:     item.ExpireAt,
					LastAccessed: item.LastAccessed,
				}
				results = append(results, meta)
			}
		}
	}

	return results, nil
}

// generateRedisKey 生成Redis键
func (rc *RedisCache) generateRedisKey(key string) string {
	return fmt.Sprintf("tmdb_cache:%s", key)
}

// Close 关闭Redis连接
func (rc *RedisCache) Close() error {
	if rc.client != nil {
		return rc.client.Close()
	}
	return nil
}
