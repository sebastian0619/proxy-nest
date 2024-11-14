package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	
	"github.com/redis/go-redis/v9"
	"github.com/allegro/bigcache/v3"
)

// 配置常量
const (
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorReset  = "\033[0m"
	MaxRetryAttempts = 5
	LocalCacheSize        = 100 * 1024 * 1024  // 100MB
	LocalCacheExpiration = 5 * time.Minute     // 本地缓存过期时间
)

var (
	WeightUpdateInterval    = getDurationEnv("WEIGHT_UPDATE_INTERVAL_MINUTES", 30) * time.Minute
	BaseWeightMultiplier   = getIntEnv("BASE_WEIGHT_MULTIPLIER", 1000)
	DynamicWeightMultiplier = getIntEnv("DYNAMIC_WEIGHT_MULTIPLIER", 1000)
	RequestTimeout         = getDurationEnv("REQUEST_TIMEOUT_MINUTES", 5) * time.Minute
	AlphaInitial          = getFloat64Env("ALPHA_INITIAL", 0.5)
	AlphaAdjustmentStep   = getFloat64Env("ALPHA_ADJUSTMENT_STEP", 0.05)
	RecentRequestLimit    = getIntEnv("RECENT_REQUEST_LIMIT", 10)
	CacheTTL             = getDurationEnv("CACHE_TTL_MINUTES", 10) * time.Minute
)

// Redis客户端
var redisClient *redis.Client

// 环境变量获取函数
func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getIntEnv(key string, defaultVal int) int {
	if val, exists := os.LookupEnv(key); exists {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func getFloat64Env(key string, defaultVal float64) float64 {
	if val, exists := os.LookupEnv(key); exists {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	if val, exists := os.LookupEnv(key); exists {
		if i, err := strconv.Atoi(val); err == nil {
			return time.Duration(i)
		}
	}
	return defaultVal
}

// 初始化Redis客户端
func initRedis() {
	redisClient = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", getEnv("REDIS_HOST", "redis"), getEnv("REDIS_PORT", "6379")),
		Password: getEnv("REDIS_PASSWORD", ""),
		DB:       getIntEnv("REDIS_DB", 0),
	})

	// 测试连接
	ctx := context.Background()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("%s无法连接到Redis: %v%s", ColorRed, err, ColorReset)
	}
}

// 检查缓存
func checkCache(uri string) ([]byte, bool) {
	ctx := context.Background()
	data, err := redisClient.Get(ctx, uri).Bytes()
	if err == redis.Nil {
		return nil, false
	}
	if err != nil {
		logError(fmt.Sprintf("Redis get error: %v", err))
		return nil, false
	}
	return data, true
}

// 添加到缓存
func addToCache(uri string, data []byte, contentType string) {
	if !isImageContentType(contentType) {
		return
	}
	ctx := context.Background()
	err := redisClient.Set(ctx, uri, data, CacheTTL).Err()
	if err != nil {
		logError(fmt.Sprintf("Failed to cache data for %s: %v", uri, err))
	}
}

// 日志函数
func logInfo(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[信息]%s [%s] %s", ColorBlue, ColorReset, hostname, message)
}

func logDebug(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[调试]%s [%s] %s", ColorGreen, ColorReset, hostname, message)
}

func logError(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[错误]%s [%s] %s", ColorRed, ColorReset, hostname, message)
}

// Server 表示上游服务器结构体
type Server struct {
	URL            string
	Healthy        bool
	BaseWeight     int
	DynamicWeight  int
	Alpha          float64
	ResponseTimes  []time.Duration
	mutex          sync.RWMutex
	healthStats    *HealthStats
	circuitBreaker *CircuitBreaker
	smoothing      *ExponentialSmoothing
}

var (
	upstreamServers []Server
	mu              sync.Mutex
)

// 初始化上游服务器列表，从环境变量加载
func initUpstreamServers() {
	upstreamEnv := os.Getenv("UPSTREAM_SERVERS")
	if upstreamEnv == "" {
		log.Fatal(ColorRed + "错误: UPSTREAM_SERVERS 环境变量未设置" + ColorReset)
	}
	servers := strings.Split(upstreamEnv, ",")
	for _, serverURL := range servers {
		upstreamServers = append(upstreamServers, Server{
			URL:            strings.TrimSpace(serverURL),
			Alpha:          AlphaInitial,
			Healthy:        true,
			BaseWeight:     50,
			// 初始化熔断器
			circuitBreaker: &CircuitBreaker{
				threshold:    5,               // 5次失败后触发熔断
				resetTimeout: time.Minute * 1, // 1分钟后重试
			},
			// 初始化健康检查统计
			healthStats: NewHealthStats(100),  // 保存最近100次请求的状态
			// 初始化平滑计算
			smoothing: &ExponentialSmoothing{
				alpha: 0.2,
				value: 50.0,
			},
		})
	}
	logInfo("已加载上游服务器列表")
}

// 权重更新和健康检查
func updateBaseWeights() {
	logInfo("开始定期服务器健康检查和权重更新...")
	var wg sync.WaitGroup
	for i := range upstreamServers {
		wg.Add(1)
		go func(server *Server) {
			defer wg.Done()
			client := &http.Client{Timeout: RequestTimeout}
			start := time.Now()
			resp, err := client.Get(server.URL + "/t/p/original/dMwdyhWNgPOoOW4wo6QZK0BgTXm.jpg")
			responseTime := time.Since(start)
			if err == nil && resp.StatusCode == http.StatusOK {
				server.ResponseTimes = append(server.ResponseTimes, responseTime)
				if len(server.ResponseTimes) > RecentRequestLimit {
					server.ResponseTimes = server.ResponseTimes[1:]
				}
				server.BaseWeight = calculateBaseWeightEWMA(server)
				server.DynamicWeight = calculateDynamicWeight(server)
				server.Healthy = true
				logInfo(fmt.Sprintf("健康检查: 服务器 %s 运行正常，响应时间 %v 毫秒，基础权重 %d，动态系数 %.6f",
					server.URL, responseTime.Milliseconds(), server.BaseWeight, server.Alpha))
			} else {
				server.Healthy = false
				server.BaseWeight = 0
				logError(fmt.Sprintf("健康检查: 无法连接到服务器 %s: %v", server.URL, err))
			}
		}(&upstreamServers[i])
	}
	wg.Wait()
}

// 计算基础权重的指数加权移动平均 (EWMA)
func calculateBaseWeightEWMA(server *Server) int {
	if len(server.ResponseTimes) == 0 {
		return 50 // 默认权重
	}
	
	smoothingFactor := 0.2
	ewmaResponseTime := float64(server.ResponseTimes[0].Milliseconds())
	
	for i := 1; i < len(server.ResponseTimes); i++ {
		rt := float64(server.ResponseTimes[i].Milliseconds())
		ewmaResponseTime = (smoothingFactor * rt) + (1-smoothingFactor)*ewmaResponseTime
	}
	
	// 基准响应时间设为500ms
	baselineResponseTime := 500.0
	// 计算权重：基准时间/实际时间 * multiplier，响应越快权重越高
	weight := int((baselineResponseTime / ewmaResponseTime) * float64(BaseWeightMultiplier))
	
	// 限制权重在1-100之间
	return min(100, max(1, weight))
}

// 计算动态权重
func calculateDynamicWeight(server *Server) int {
	if len(server.ResponseTimes) == 0 {
		return 50 // 默认权重
	}
	
	avgResponseTime := float64(averageDuration(server.ResponseTimes).Milliseconds())
	// 基准响应时间设为500ms
	baselineResponseTime := 500.0
	// 计算权重：基准时间/实际时间 * multiplier，响应越快权重越高
	weight := int((baselineResponseTime / avgResponseTime) * float64(DynamicWeightMultiplier))
	
	// 限制权重在1-100之间
	return min(100, max(1, weight))
}

// 计算综合权重
func calculateCombinedWeight(server *Server) int {
	if !server.Healthy {
		return 0
	}
	
	baseWeight := float64(server.BaseWeight)
	dynamicWeight := float64(server.DynamicWeight)
	
	// 使用 Alpha 进行加权平均
	combinedWeight := int(server.Alpha*dynamicWeight + (1-server.Alpha)*baseWeight)
	
	return min(100, max(1, combinedWeight))
}

// 平均响应时间计算
func averageDuration(durations []time.Duration) time.Duration {
	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	return time.Duration(sum.Nanoseconds() / int64(len(durations)))
}

// 选择健康的上游服务器
func getWeightedRandomServer() *Server {
	healthyServers := make([]Server, 0)
	totalWeight := 0
	weightInfo := make(map[string]int)

	for _, server := range upstreamServers {
		if server.Healthy {
			combinedWeight := calculateCombinedWeight(&server)
			weightInfo[server.URL] = combinedWeight
			totalWeight += combinedWeight
			for i := 0; i < combinedWeight; i++ {
				healthyServers = append(healthyServers, server)
			}
		}
	}

	if len(healthyServers) == 0 {
		return nil
	}

	// 输出所有健康服务器的权重信息
	logDebug("当前可用服务器权重分布:")
	for url, weight := range weightInfo {
		percentage := float64(weight) / float64(totalWeight) * 100
		logDebug(fmt.Sprintf("  - %s: 权重 %d (%.2f%%)", url, weight, percentage))
	}

	selected := &healthyServers[rand.Intn(len(healthyServers))]
	return selected
}

// 添加并行请求的结果结构
type fetchResult struct {
	body        []byte
	contentType string
	statusCode  int
	serverURL   string
	err         error
}

// 修改代理请求处理函数
func handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	metrics.RequestCount.Add(1)
	uri := r.RequestURI

	// 检查缓存
	if data, contentType, found := checkCaches(uri); found {
		logInfo(fmt.Sprintf("缓存命中: %s", uri))
		w.Header().Set("Content-Type", contentType)
		w.Write(data)
		return
	}

	metrics.CacheMisses.Add(1)
	logDebug(fmt.Sprintf("缓存未命中: %s", uri))

	// 使用熔断器和重试逻辑获取图片
	var lastErr error
	attemptedServers := make(map[string]bool)
	
	for attempt := 0; attempt < MaxRetryAttempts; attempt++ {
		server := getWeightedRandomServer()
		if server == nil {
			metrics.ErrorCount.Add(1)
			http.Error(w, "没有可用的健康上游服务器", http.StatusBadGateway)
			return
		}

		if attemptedServers[server.URL] {
			continue
		}
		attemptedServers[server.URL] = true

		// 检查熔断器状态
		if server.circuitBreaker.IsOpen() {
			logError(fmt.Sprintf("服务器 %s 熔断器开启，跳过", server.URL))
			continue
		}

		logInfo(fmt.Sprintf("尝试第 %d 次请求，使用服务器: %s", attempt+1, server.URL))
		body, contentType, statusCode, err := tryFetchImage(server, r)
		
		if err != nil {
			server.circuitBreaker.Record(err)
			lastErr = err
			metrics.ErrorCount.Add(1)
			logError(fmt.Sprintf("请求失败: %s - %v", server.URL, err))
			continue
		}

		if !isImageContentType(contentType) {
			server.circuitBreaker.Record(fmt.Errorf("非图片响应"))
			logError(fmt.Sprintf("服务器 %s 返回非图片格式: %s", server.URL, contentType))
			markServerUnhealthy(server)
			continue
		}

		// 成功获取图片
		logInfo(fmt.Sprintf("成功获取图片: %s", server.URL))
		
		// 更新缓存
		localCache.Set(uri, body)
		addToCache(uri, body, contentType)
		
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(statusCode)
		
		// 使用缓冲池复制响应
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)
		w.Write(body)
		return
	}

	metrics.ErrorCount.Add(1)
	http.Error(w, fmt.Sprintf("经过 %d 次尝试后仍未能���取图片: %v", MaxRetryAttempts, lastErr), http.StatusBadGateway)
}

// 添加尝试获图片的函数
func tryFetchImage(server *Server, r *http.Request) ([]byte, string, int, error) {
	client := &http.Client{Timeout: RequestTimeout}
	url := server.URL + r.RequestURI
	req, err := http.NewRequest(r.Method, url, r.Body)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header = r.Header

	start := time.Now()
	resp, err := client.Do(req)
	responseTime := time.Since(start)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", 0, err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}

	// 计算平均响应时间
	avgResponseTime := "N/A"
	if len(server.ResponseTimes) > 0 {
		avg := averageDuration(server.ResponseTimes)
		avgResponseTime = fmt.Sprintf("%d", avg.Milliseconds())
	}

	// 获取权重信息
	baseWeight := server.BaseWeight
	dynamicWeight := server.DynamicWeight
	combinedWeight := calculateCombinedWeight(server)

	logInfo(fmt.Sprintf("[上游选择] 服务器: %s - 响应时间: %d 毫秒 - 平均响应: %s 毫秒 - 基础权重: %d - 动态权重: %d - 综合权重: %d",
		server.URL,
		responseTime.Milliseconds(),
		avgResponseTime,
		baseWeight,
		dynamicWeight,
		combinedWeight,
	))

	return body, contentType, resp.StatusCode, nil
}

// 添加标记服务器不健康的函数
func markServerUnhealthy(server *Server) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	server.Healthy = false
	server.BaseWeight = 0
	logError(fmt.Sprintf("服务器 %s 被标记为不健康（返回非图片响应）", server.URL))
}

// 添加图片格式验证函数
func isImageContentType(contentType string) bool {
	return strings.HasPrefix(contentType, "image/")
}

// 添加清理非图片缓存的函数
func cleanNonImageCache() {
	ctx := context.Background()
	iter := redisClient.Scan(ctx, 0, "*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		data, err := redisClient.Get(ctx, key).Bytes()
		if err != nil {
			continue
		}
		
		// 尝试检测内容类型
		contentType := http.DetectContentType(data)
		if !isImageContentType(contentType) {
			redisClient.Del(ctx, key)
			logInfo(fmt.Sprintf("清理非图片缓存: %s", key))
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// 新增结构体定义
type Metrics struct {
    RequestCount   atomic.Int64
    ErrorCount     atomic.Int64
    ResponseTimes  sync.Map  // URL -> []time.Duration
    CacheHits      atomic.Int64
    CacheMisses    atomic.Int64
    LocalCacheHits atomic.Int64
    RedisCacheHits atomic.Int64
}

type CircuitBreaker struct {
    failures     int32
    lastFailure  time.Time
    threshold    int32
    resetTimeout time.Duration
    mu          sync.RWMutex
}

type HealthStats struct {
    windowSize int
    requests   []bool
    index      int
    mu         sync.RWMutex
}

type ExponentialSmoothing struct {
    value float64
    alpha float64
}

// 全局变量
var (
    metrics     = &Metrics{}
    localCache  *bigcache.BigCache
    bufferPool  = sync.Pool{
        New: func() interface{} {
            return make([]byte, 32*1024)
        },
    }
    httpClient = &http.Client{
        Transport: &http.Transport{
            MaxIdleConns:        100,
            MaxIdleConnsPerHost: 100,
            IdleConnTimeout:     90 * time.Second,
            DisableCompression:  true,
        },
        Timeout: RequestTimeout,
    }
)

// 初始化本地缓存
func initCache() error {
    config := bigcache.DefaultConfig(LocalCacheExpiration)
    config.MaxEntriesInWindow = 10000 // 根据实际需求调整
    config.MaxEntrySize = 5 * 1024 * 1024 // 单个图片最大 5MB
    config.HardMaxCacheSize = LocalCacheSize // 最大缓存大小
    config.Verbose = false // 生产环境建议关闭详细日志
    
    var err error
    localCache, err = bigcache.NewBigCache(config)
    if err != nil {
        return fmt.Errorf("初始化本地缓存失败: %v", err)
    }
    return nil
}

// 修改缓存检查逻辑
func checkCaches(uri string) ([]byte, string, bool) {
    // 1. 先检查本地缓存
    if data, err := localCache.Get(uri); err == nil {
        contentType := http.DetectContentType(data)
        if isImageContentType(contentType) {
            metrics.LocalCacheHits.Add(1)
            return data, contentType, true
        }
    }

    // 2. 检查 Redis 缓存
    if data, found := checkCache(uri); found {
        contentType := http.DetectContentType(data)
        if isImageContentType(contentType) {
            metrics.RedisCacheHits.Add(1)
            // 更新本地缓存
            if err := localCache.Set(uri, data); err != nil {
                logError(fmt.Sprintf("更新本地缓存失败: %v", err))
            }
            return data, contentType, true
        }
    }

    metrics.CacheMisses.Add(1)
    return nil, "", false
}

func main() {
	// 初始化随机数种子
	rand.Seed(time.Now().UnixNano())
	
	// 初始化缓存
	if err := initCache(); err != nil {
		log.Printf("警告: 本地缓存初始化失败: %v", err)
		// 继续运行，仅使用 Redis 缓存
	} else {
		startCacheCleanup()
	}
	
	// 初始化Redis
	initRedis()
	
	// 从环境变量加载上游服务器
	initUpstreamServers()
	
	// 启动健康检查
	updateBaseWeights()
	
	// 启动指标收集
	go collectMetrics()
	
	// 启动HTTP服务
	http.HandleFunc("/", handleProxyRequest)
	http.HandleFunc("/metrics", handleMetrics) // 添加指标接口
	
	port := getEnv("PORT", "6637")
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), nil))
}

// 收集指标
func collectMetrics() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		logInfo(fmt.Sprintf(
			"性能指标 - 请求总数: %d, 错误数: %d, 缓存命中: %d, 缓存未命中: %d",
			metrics.RequestCount.Load(),
			metrics.ErrorCount.Load(),
			metrics.CacheHits.Load(),
			metrics.CacheMisses.Load(),
		))
	}
}

// 指标接口
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"requests":     metrics.RequestCount.Load(),
		"errors":       metrics.ErrorCount.Load(),
		"cache_hits":   metrics.CacheHits.Load(),
		"cache_misses": metrics.CacheMisses.Load(),
	})
}

// 定期清理过期本地缓存
func startCacheCleanup() {
    go func() {
        ticker := time.NewTicker(10 * time.Minute)
        for range ticker.C {
            before := localCache.Len()
            localCache.Reset()
            after := localCache.Len()
            logInfo(fmt.Sprintf("本地缓存清理完成: 清理前 %d 项, 清理后 %d 项", before, after))
        }
    }()
}

// CircuitBreaker 结构体及其方法
type CircuitBreaker struct {
    failures     int32
    lastFailure  time.Time
    threshold    int32
    resetTimeout time.Duration
    mu          sync.RWMutex
}

func (cb *CircuitBreaker) Record(err error) {
    if err != nil {
        cb.mu.Lock()
        defer cb.mu.Unlock()
        atomic.AddInt32(&cb.failures, 1)
        cb.lastFailure = time.Now()
    }
}

func (cb *CircuitBreaker) IsOpen() bool {
    cb.mu.RLock()
    defer cb.mu.RUnlock()
    if atomic.LoadInt32(&cb.failures) >= cb.threshold {
        // 如果超过重置时间，重置失败计数
        if time.Since(cb.lastFailure) > cb.resetTimeout {
            atomic.StoreInt32(&cb.failures, 0)
            return false
        }
        return true
    }
    return false
}

func (cb *CircuitBreaker) Reset() {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    atomic.StoreInt32(&cb.failures, 0)
}
