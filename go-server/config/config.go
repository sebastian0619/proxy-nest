package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// CacheConfig 缓存配置
type CacheConfig struct {
	CacheEnabled              bool
	CacheDir                  string
	CacheIndexFile           string
	DiskCacheTTL             time.Duration
	MemoryCacheTTL           time.Duration
	MemoryCacheSize          int
	CacheMaxSize             int
	DiskCacheCleanupInterval time.Duration
	MemoryCacheCleanupInterval time.Duration
	ContentTypeConfig        map[string]ContentTypeConfig
	CacheFileExt             string
}

// ContentTypeConfig 内容类型配置
type ContentTypeConfig struct {
	MemoryTTL   time.Duration
	DiskTTL     time.Duration
	SkipMemory  bool
}

// Config 主配置结构
type Config struct {
	// 服务器配置
	Port                    int
	NumWorkers              int
	UnhealthyTimeout        time.Duration
	MaxErrorsBeforeUnhealthy int
	
	// 请求超时配置
	InitialTimeout          time.Duration
	ParallelTimeout         time.Duration
	RequestTimeout          time.Duration
	
	// TMDB 相关配置
	UpstreamType            string
	TMDBAPIKey              string
	TMDBImageTestURL        string
	
	// 负载均衡配置
	BaseWeightMultiplier    int
	DynamicWeightMultiplier int
	AlphaInitial            float64
	AlphaAdjustmentStep     float64
	MaxServerSwitches       int
	
	// 代理配置
	HTTPProxy               string
	HTTPSProxy              string
	
	// 时区配置
	Timezone                string
	
	// Custom upstream 配置
	CustomContentType       string
	
	// 缓存配置
	Cache                   CacheConfig
}

// LoadConfig 加载配置
func LoadConfig() *Config {
	config := &Config{
		// 服务器配置
		Port:                    getEnvAsInt("PORT", 6635),
		NumWorkers:              getNumWorkers(),
		UnhealthyTimeout:        time.Duration(getEnvAsInt("UNHEALTHY_TIMEOUT", 900)) * time.Second,
		MaxErrorsBeforeUnhealthy: getEnvAsInt("MAX_ERRORS_BEFORE_UNHEALTHY", 3),
		
		// 请求超时配置
		InitialTimeout:          time.Duration(getEnvAsInt("INITIAL_TIMEOUT", 8)) * time.Second,
		ParallelTimeout:         time.Duration(getEnvAsInt("PARALLEL_TIMEOUT", 20)) * time.Second,
		RequestTimeout:          time.Duration(getEnvAsInt("REQUEST_TIMEOUT", 30)) * time.Second,
		
		// TMDB 相关配置
		UpstreamType:            getEnv("UPSTREAM_TYPE", "tmdb-api"),
		TMDBAPIKey:              getEnv("TMDB_API_KEY", ""),
		TMDBImageTestURL:        getEnv("TMDB_IMAGE_TEST_URL", ""),
		
		// 负载均衡配置
		BaseWeightMultiplier:    getEnvAsInt("BASE_WEIGHT_MULTIPLIER", 20),
		DynamicWeightMultiplier: getEnvAsInt("DYNAMIC_WEIGHT_MULTIPLIER", 50),
		AlphaInitial:            getEnvAsFloat("ALPHA_INITIAL", 0.5),
		AlphaAdjustmentStep:     getEnvAsFloat("ALPHA_ADJUSTMENT_STEP", 0.05),
		MaxServerSwitches:       getEnvAsInt("MAX_SERVER_SWITCHES", 3),
		
		// 代理配置
		HTTPProxy:               getEnv("HTTP_PROXY", ""),
		HTTPSProxy:              getEnv("HTTPS_PROXY", ""),
		
		// 时区配置
		Timezone:                getEnv("TZ", "Asia/Shanghai"),
		
		// Custom upstream 配置
		CustomContentType:       getEnv("CUSTOM_CONTENT_TYPE", "application/json"),
		
		// 缓存配置
		Cache: CacheConfig{
			CacheEnabled:              getEnvAsBool("CACHE_ENABLED", true),
			CacheDir:                  getEnv("CACHE_DIR", filepath.Join(".", "cache")),
			CacheIndexFile:           "cache_index.json",
			DiskCacheTTL:             time.Duration(getEnvAsInt("DISK_CACHE_TTL", 1440)) * time.Minute,
			MemoryCacheTTL:           time.Duration(getEnvAsInt("MEMORY_CACHE_TTL", 60)) * time.Minute,
			MemoryCacheSize:          getEnvAsInt("MEMORY_CACHE_SIZE", 100),
			CacheMaxSize:             getEnvAsInt("CACHE_MAX_SIZE", 1000),
			DiskCacheCleanupInterval: time.Duration(getEnvAsInt("DISK_CACHE_CLEANUP_INTERVAL", 5)) * time.Minute,
			MemoryCacheCleanupInterval: time.Duration(getEnvAsInt("MEMORY_CACHE_CLEANUP_INTERVAL", 1)) * time.Minute,
			ContentTypeConfig: map[string]ContentTypeConfig{
				"image": {
					MemoryTTL:  time.Duration(getEnvAsInt("IMAGE_MEMORY_TTL", 10)) * time.Minute,
					DiskTTL:    time.Duration(getEnvAsInt("IMAGE_DISK_TTL", 4320)) * time.Minute,
					SkipMemory: getEnvAsBool("IMAGE_SKIP_MEMORY", false),
				},
				"json": {
					MemoryTTL:  time.Duration(getEnvAsInt("JSON_MEMORY_TTL", 60)) * time.Minute,
					DiskTTL:    time.Duration(getEnvAsInt("JSON_DISK_TTL", 1440)) * time.Minute,
					SkipMemory: false,
				},
			},
			CacheFileExt: ".cache",
		},
	}
	
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