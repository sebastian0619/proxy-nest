package cache

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/redis/go-redis/v9"
	"tmdb-go-proxy-nest/metrics"
)

var (
	localCache  *bigcache.BigCache
	redisClient *redis.Client
)

func InitLocalCache(expiration time.Duration, size int) error {
	config := bigcache.DefaultConfig(expiration)
	config.HardMaxCacheSize = size
	var err error
	localCache, err = bigcache.NewBigCache(config)
	if err != nil {
		log.Printf("初始化本地缓存失败: %v", err)
	}
	return err
}

func InitRedis(addr, password string, db int) {
	redisClient = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
}

func CheckCache(uri string) ([]byte, bool) {
	// 先从本地缓存中查找
	data, err := localCache.Get(uri)
	if err == nil {
		metrics.IncrementCacheHit()
		log.Printf("本地缓存命中: %s", uri)
		return data, true
	}

	// 本地缓存未命中，从 Redis 中查找
	ctx := context.Background()
	data, err = redisClient.Get(ctx, uri).Bytes()
	if err == redis.Nil {
		metrics.IncrementCacheMiss()
		log.Printf("缓存未命中: %s", uri)
		return nil, false
	}
	if err != nil {
		log.Printf("Redis获取错误: %v", err)
		return nil, false
	}

	// 验证数据有效性
	upstreamType := os.Getenv("UPSTREAM_TYPE")
	contentCheck := os.Getenv("CONTENT_CHECK") == "true"
	customContentType := os.Getenv("CONTENT_CHECK_TYPE")
	contentType := getContentTypeFromCache(uri, upstreamType, customContentType)

	if len(data) == 0 || !isValidContent(data, contentType, upstreamType, contentCheck, customContentType) {
		redisClient.Del(ctx, uri)
		log.Printf("缓存数据无效或格式不匹配，已删除: %s", uri)
		return nil, false
	}

	// 更新本地缓存
	localCache.Set(uri, data)
	metrics.IncrementCacheHit()
	log.Printf("Redis缓存命中并更新本地缓存: %s", uri)
	return data, true
}

func UpdateCache(uri string, data []byte) {
	// 更新本地缓存
	err := localCache.Set(uri, data)
	if err != nil {
		log.Printf("更新本地缓存失败: %v", err)
	}

	// 更新 Redis 缓存
	ctx := context.Background()
	err = redisClient.Set(ctx, uri, data, 0).Err()
	if err != nil {
		log.Printf("更新Redis缓存失败: %v", err)
	}
}

func getContentTypeFromCache(uri string, upstreamType string, customContentType string) string {
	switch upstreamType {
	case "tmdb_api":
		return "application/json"
	case "tmdb_image":
		return "image/"
	case "custom":
		return customContentType
	default:
		return ""
	}
}

func isValidContent(data []byte, contentType string, upstreamType string, contentCheck bool, customContentType string) bool {
	if !contentCheck {
		return true
	}

	switch upstreamType {
	case "tmdb_api":
		return isValidJSON(data)
	case "tmdb_image":
		return isValidImage(contentType)
	case "custom":
		return isValidCustomContent(contentType, customContentType)
	default:
		return false
	}
}

func isValidJSON(data []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(data, &js) == nil
}

func isValidImage(contentType string) bool {
	return strings.HasPrefix(contentType, "image/")
}

func isValidCustomContent(contentType, customContentType string) bool {
	return contentType == customContentType
}

var RedisClient *redis.Client

func InitRedisClient(options *redis.Options) {
	RedisClient = redis.NewClient(options)
}
