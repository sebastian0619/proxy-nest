package config

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// CacheConfig 缓存配置
type CacheConfig struct {
	CacheEnabled               bool
	CacheDir                   string  // 保留，用于向后兼容
	CacheIndexFile             string  // 保留，用于向后兼容
	DiskCacheTTL               time.Duration  // 保留，用于向后兼容
	MemoryCacheTTL             time.Duration
	MemoryCacheSize            int
	CacheMaxSize               int
	DiskCacheCleanupInterval   time.Duration  // 保留，用于向后兼容
	MemoryCacheCleanupInterval time.Duration
	ContentTypeConfig          map[string]ContentTypeConfig
	CacheFileExt               string  // 保留，用于向后兼容
	
	// Redis配置
	UseRedis                   bool
	RedisClusterNodes          []string
	RedisPassword              string
	RedisDB                    int
	RedisPoolSize              int
	RedisMinIdleConns          int
	RedisConnectTimeout        time.Duration
	RedisReadTimeout           time.Duration
	RedisWriteTimeout          time.Duration
	RedisIdleTimeout           time.Duration
	RedisMaxRetries            int
	RedisRetryDelay            time.Duration
}

// ContentTypeConfig 内容类型配置
type ContentTypeConfig struct {
	MemoryTTL  time.Duration
	DiskTTL    time.Duration
	SkipMemory bool
}

// Config 主配置结构
type Config struct {
	// 服务器配置
	Port                     int
	NumWorkers               int
	UnhealthyTimeout         time.Duration
	MaxErrorsBeforeUnhealthy int

	// 健康检查配置
	HealthCheckInterval      time.Duration
	HealthCheckInitialDelay  time.Duration

	// 请求超时配置
	InitialTimeout  time.Duration
	ParallelTimeout time.Duration
	RequestTimeout  time.Duration

	// TMDB 相关配置
	UpstreamType     string
	TMDBAPIKey       string
	TMDBImageTestURL string

	// 负载均衡配置
	BaseWeightMultiplier    int
	DynamicWeightMultiplier int
	AlphaInitial            float64
	AlphaAdjustmentStep     float64
	MaxServerSwitches       int

	// 代理配置
	HTTPProxy  string
	HTTPSProxy string

	// 时区配置
	Timezone string

	// Custom upstream 配置
	CustomContentType string

	// 缓存配置
	Cache CacheConfig

	// 嵌套代理检测配置
	EnableNestedProxyDetection bool

	// 上游代理服务器配置（用于同一套系统的其他实例）
	UpstreamProxyServers []string
}

// LoadConfig 加载配置
func LoadConfig() *Config {
	config := &Config{
		// 服务器配置
		Port:                     getEnvAsInt("PORT", 6635),
		NumWorkers:               getNumWorkers(),
		UnhealthyTimeout:         time.Duration(getEnvAsInt("UNHEALTHY_TIMEOUT", 900)) * time.Second,
		MaxErrorsBeforeUnhealthy: getEnvAsInt("MAX_ERRORS_BEFORE_UNHEALTHY", 3),

		// 健康检查配置
		HealthCheckInterval:      time.Duration(getEnvAsInt("HEALTH_CHECK_INTERVAL", 30)) * time.Second,
		HealthCheckInitialDelay:  time.Duration(getEnvAsInt("HEALTH_CHECK_INITIAL_DELAY", 10)) * time.Second,

		// 请求超时配置
		InitialTimeout:  time.Duration(getEnvAsInt("INITIAL_TIMEOUT", 8)) * time.Second,
		ParallelTimeout: time.Duration(getEnvAsInt("PARALLEL_TIMEOUT", 20)) * time.Second,
		RequestTimeout:  time.Duration(getEnvAsInt("REQUEST_TIMEOUT", 300)) * time.Second, // 5分钟默认超时，与proxy_app.go保持一致

		// TMDB 相关配置
		UpstreamType:     getEnv("UPSTREAM_TYPE", "tmdb-api"),
		TMDBAPIKey:       getEnv("TMDB_API_KEY", ""),
		TMDBImageTestURL: getEnv("TMDB_IMAGE_TEST_URL", ""),

		// 负载均衡配置
		BaseWeightMultiplier:    getEnvAsInt("BASE_WEIGHT_MULTIPLIER", 20),
		DynamicWeightMultiplier: getEnvAsInt("DYNAMIC_WEIGHT_MULTIPLIER", 50),
		AlphaInitial:            getEnvAsFloat("ALPHA_INITIAL", 0.5),
		AlphaAdjustmentStep:     getEnvAsFloat("ALPHA_ADJUSTMENT_STEP", 0.05),
		MaxServerSwitches:       getEnvAsInt("MAX_SERVER_SWITCHES", 3),

		// 代理配置
		HTTPProxy:  getEnv("HTTP_PROXY", ""),
		HTTPSProxy: getEnv("HTTPS_PROXY", ""),

		// 时区配置
		Timezone: getEnv("TZ", "Asia/Shanghai"),

		// Custom upstream 配置
		CustomContentType: getEnv("CUSTOM_CONTENT_TYPE", "application/json"),

		// 缓存配置
		Cache: CacheConfig{
			CacheEnabled:               getEnvAsBool("CACHE_ENABLED", true),
			CacheDir:                   getEnv("CACHE_DIR", "cache"),  // 保留向后兼容
			CacheIndexFile:             "cache_index.json",  // 保留向后兼容
			DiskCacheTTL:               time.Duration(getEnvAsInt("DISK_CACHE_TTL", 360)) * time.Minute,  // 降到6小时
			MemoryCacheTTL:             time.Duration(getEnvAsInt("MEMORY_CACHE_TTL", 15)) * time.Minute,  // 降到15分钟
			MemoryCacheSize:            getEnvAsInt("MEMORY_CACHE_SIZE", 100),
			CacheMaxSize:               getEnvAsInt("CACHE_MAX_SIZE", 1000),
			DiskCacheCleanupInterval:   time.Duration(getEnvAsInt("DISK_CACHE_CLEANUP_INTERVAL", 5)) * time.Minute,
			MemoryCacheCleanupInterval: time.Duration(getEnvAsInt("MEMORY_CACHE_CLEANUP_INTERVAL", 1)) * time.Minute,
			ContentTypeConfig: map[string]ContentTypeConfig{
				"image": {
					MemoryTTL:  time.Duration(getEnvAsInt("IMAGE_MEMORY_TTL", 30)) * time.Minute,  // 提升到30分钟
					DiskTTL:    time.Duration(getEnvAsInt("IMAGE_DISK_TTL", 10080)) * time.Minute,  // 提升到7天
					SkipMemory: getEnvAsBool("IMAGE_SKIP_MEMORY", false),
				},
				"json": {
					MemoryTTL:  time.Duration(getEnvAsInt("JSON_MEMORY_TTL", 15)) * time.Minute,  // 降到15分钟
					DiskTTL:    time.Duration(getEnvAsInt("JSON_DISK_TTL", 360)) * time.Minute,  // 降到6小时
					SkipMemory: false,
				},
			},
			CacheFileExt: ".cache",
			
			// Redis配置
			UseRedis:           getEnvAsBool("USE_REDIS", false),
			RedisClusterNodes:  getEnvAsStringSlice("REDIS_CLUSTER_NODES", []string{"localhost:6379"}),
			RedisPassword:      getEnv("REDIS_PASSWORD", ""),
			RedisDB:            getEnvAsInt("REDIS_DB", 0),
			RedisPoolSize:      getEnvAsInt("REDIS_POOL_SIZE", 10),
			RedisMinIdleConns:  getEnvAsInt("REDIS_MIN_IDLE_CONNS", 2),
			RedisConnectTimeout: time.Duration(getEnvAsInt("REDIS_CONNECT_TIMEOUT", 5)) * time.Second,
			RedisReadTimeout:   time.Duration(getEnvAsInt("REDIS_READ_TIMEOUT", 3)) * time.Second,
			RedisWriteTimeout:  time.Duration(getEnvAsInt("REDIS_WRITE_TIMEOUT", 3)) * time.Second,
			RedisIdleTimeout:   time.Duration(getEnvAsInt("REDIS_IDLE_TIMEOUT", 300)) * time.Second,
			RedisMaxRetries:    getEnvAsInt("REDIS_MAX_RETRIES", 3),
			RedisRetryDelay:    time.Duration(getEnvAsInt("REDIS_RETRY_DELAY", 500)) * time.Millisecond,
		},
	}

	// 嵌套代理检测配置
	config.EnableNestedProxyDetection = getEnvAsBool("ENABLE_NESTED_PROXY_DETECTION", true)

	// 上游代理服务器配置（用于同一套系统的其他实例）
	config.UpstreamProxyServers = getEnvAsStringSlice("UPSTREAM_PROXY_SERVERS", []string{})

	return config
}

// 辅助函数
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvAsFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

func getEnvAsBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvAsStringSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		// 支持逗号分隔的字符串转换为切片
		parts := strings.Split(value, ",")
		var result []string
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result
	}
	return defaultValue
}

func getNumWorkers() int {
	// 默认使用CPU核心数-1，最少1个
	numWorkers := runtime.NumCPU() - 1
	if numWorkers < 1 {
		numWorkers = 1
	}

	// 如果环境变量设置了，使用环境变量的值
	if envValue := os.Getenv("NUM_WORKERS"); envValue != "" {
		if intValue, err := strconv.Atoi(envValue); err == nil && intValue > 0 {
			return intValue
		}
	}

	return numWorkers
}
