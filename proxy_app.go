package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"encoding/json"
	"net/url"
	
	"github.com/redis/go-redis/v9"
	"github.com/allegro/bigcache/v3"
	"sync/atomic"
	"math"
	"runtime"
)

// 配置常量
const (
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorReset  = "\033[0m"
	MaxRetryAttempts = 3  // API可以少一些重试次数
	HealthCheckPrefix = "health:"
	HealthDataTTL    = 5 * time.Minute
	MaxResponseSize = 15 * 1024 * 1024  // 15MB
	MaxResponseTimeRecords = 5  // 最大响应时间记录数
	MaxRetries = 3  // 添加缺失的常量

	// 日志级别常量
	LogLevelDebug = iota
	LogLevelInfo
	LogLevelWarning
	LogLevelError

	// 添加 swap 监控相关常量和结构体
	SwapCheckInterval   = 30 * time.Second
	HighSwapThreshold  = 65.0  // swap 使用率超过65%触发清理
	CriticalSwapThreshold = 75.0 // swap 使用率超过75%触发紧急清理
	SwapCleanupInterval = 5 * time.Minute
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
	LocalCacheSize       = getIntEnv("LOCAL_CACHE_SIZE_MB", 50) * 1024 * 1024
	METRICS_INTERVAL = time.Duration(getIntEnv("METRICS_INTERVAL_MINUTES", 10)) * time.Minute
	LocalCacheExpiration = getDurationEnv("LOCAL_CACHE_EXPIRATION_MINUTES", 5) * time.Minute

	// 日志级别变量
	currentLogLevel = getLogLevelFromEnv()
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
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
}

// 新增：验证JSON格式
func isValidJSON(data []byte) bool {
	var js json.RawMessage
	return json.Unmarshal(data, &js) == nil
}

// 提取公共的日志记录函数
func logMessage(level, color, message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[%s]%s [%s] %s", color, level, ColorReset, hostname, message)
}

func logInfo(message string) {
	if currentLogLevel <= LogLevelInfo {
		logMessage("信息", ColorBlue, message)
	}
}

func logError(message string) {
	if currentLogLevel <= LogLevelError {
		logMessage("错误", ColorRed, message)
	}
}

func logDebug(message string) {
	if currentLogLevel <= LogLevelDebug {
		logMessage("调试", ColorMagenta, message)
	}
}

// 通用验证函数
func validateData(data []byte, contentType string) bool {
	upstreamType := os.Getenv("UPSTREAM_TYPE")
	switch upstreamType {
	case "tmdb-api":
		return isValidJSON(data)
	case "tmdb-image":
		return isValidImage(data)
	case "custom":
		expectedContentType := os.Getenv("CUSTOM_CONTENT_TYPE")
		if expectedContentType == "" {
			logError("未设置 CUSTOM_CONTENT_TYPE 环境变量")
			return false
		}
		return strings.Contains(contentType, expectedContentType)
	default:
		logError(fmt.Sprintf("未知的 UPSTREAM_TYPE: %s", upstreamType))
		return false
	}
}

// 通用缓存接口定义
type GeneralCache interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// 本地缓存接口定义，扩展了通用缓存接口
type LocalCacheInterface interface {
	GeneralCache
	Len() int
	Reset()
	GetStats() CacheStats
}

// Redis缓存实现
type RedisCache struct {
	client *redis.Client
}

func (r *RedisCache) Get(key string) ([]byte, error) {
	ctx := context.Background()
	return r.client.Get(ctx, key).Bytes()
}

func (r *RedisCache) Set(key string, value []byte) error {
	ctx := context.Background()
	return r.client.Set(ctx, key, value, CacheTTL).Err()
}

func (r *RedisCache) Delete(key string) error {
	ctx := context.Background()
	return r.client.Del(ctx, key).Err()
}

// 本地缓存实现
type CacheItem struct {
	Data       []byte
	HitCount   int
	LastAccess time.Time
}

type LocalCache struct {
	cache     *bigcache.BigCache
	hitCount  int64
	missCount int64
	mutex     sync.RWMutex
}

// 实现 LocalCacheInterface 的方法
func (l *LocalCache) Get(key string) ([]byte, error) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	
	data, err := l.cache.Get(key)
	if err != nil {
		atomic.AddInt64(&l.missCount, 1)
		return nil, err
	}
	atomic.AddInt64(&l.hitCount, 1)
	return data, nil
}

func (l *LocalCache) Set(key string, value []byte) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.cache.Set(key, value)
}

func (l *LocalCache) Delete(key string) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.cache.Delete(key)
}

func (l *LocalCache) Len() int {
	return l.cache.Len()
}

func (l *LocalCache) Reset() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.cache.Reset()
	atomic.StoreInt64(&l.hitCount, 0)
	atomic.StoreInt64(&l.missCount, 0)
}

func (l *LocalCache) GetStats() CacheStats {
	return CacheStats{
		ItemCount: l.cache.Len(),
		Hits:      atomic.LoadInt64(&l.hitCount),
		Misses:    atomic.LoadInt64(&l.missCount),
	}
}

// 初始化缓存
var redisCache GeneralCache
var localCache LocalCacheInterface

func initCaches() error {
	// 初始化Redis缓存
	redisCache = &RedisCache{client: redisClient}

	// 初始化本地缓存
	config := bigcache.DefaultConfig(LocalCacheExpiration)
	config.MaxEntriesInWindow = getIntEnv("LOCAL_CACHE_MAX_ENTRIES", 1000)
	config.MaxEntrySize = getIntEnv("LOCAL_CACHE_MAX_ENTRY_SIZE_KB", 256) * 1024
	config.HardMaxCacheSize = getIntEnv("LOCAL_CACHE_SIZE_MB", 20)
	config.Verbose = false
	config.Shards = 256

	cache, err := bigcache.NewBigCache(config)
	if err != nil {
		return fmt.Errorf("初始化本地缓存失败: %v", err)
	}
	
	localCache = &LocalCache{
		cache: cache,
	}

	return nil
}

// 生成缓存键的函数
func generateCacheKey(r *http.Request) string {
	// 使用请求的URI为缓存键
	return r.URL.RequestURI()
}

// 检查缓存逻辑
func checkCaches(r *http.Request) ([]byte, bool) {
	uri := generateCacheKey(r)
	logDebug(fmt.Sprintf("检查缓存: %s", uri))

	// 检查本地缓存
	if data, err := localCache.Get(uri); err == nil && validateData(data, "") {
		metrics.LocalCacheHits.Add(1)
		logInfo(fmt.Sprintf("本地缓存命中: %s", uri))
		return data, true
	}

	// 检查Redis缓存
	if data, err := redisCache.Get(uri); err == nil && validateData(data, "") {
		metrics.RedisCacheHits.Add(1)
		logInfo(fmt.Sprintf("Redis缓存命中: %s", uri))
		// 更新本地缓存
		if err := localCache.Set(uri, data); err != nil {
			logError(fmt.Sprintf("更新本地缓存失败: %v", err))
		}
		return data, true
	}

	metrics.CacheMisses.Add(1)
	logInfo(fmt.Sprintf("缓存未命中: %s", uri))
	return nil, false
}

// 更新缓存逻辑
func addToCache(r *http.Request, data []byte) {
	uri := generateCacheKey(r)
	logDebug(fmt.Sprintf("更新缓存: %s, 数据长度: %d", uri, len(data)))

	// 确保数据是有效的JSON
	if !validateData(data, "") {
		logError(fmt.Sprintf("尝试缓存无效的JSON数据: %s", uri))
		return
	}

	// 更新Redis缓存
	if err := redisCache.Set(uri, data); err != nil {
		logError(fmt.Sprintf("Redis缓存更新失败 %s: %v", uri, err))
		return
	}

	// 更新本地缓存
	if err := localCache.Set(uri, data); err != nil {
		logError(fmt.Sprintf("本地缓存更新失败: %v", err))
	}

	logInfo(fmt.Sprintf("缓存更新成功: %s, 数据长度: %d", uri, len(data)))
}

// 辅助函数：截断数据以避免日志过长
func truncateData(data []byte) string {
	const maxLength = 200
	if len(data) > maxLength {
		return string(data[:maxLength]) + "..."
	}
	return string(data)
}


func cleanInvalidCache() {
	ctx := context.Background()
	iter := redisClient.Scan(ctx, 0, "*", 0).Iterator()
	upstreamType := os.Getenv("UPSTREAM_TYPE")
	for iter.Next(ctx) {
		key := iter.Val()
		data, err := redisClient.Get(ctx, key).Bytes()
		if err != nil {
			continue
		}
		
		if !validateData(data, upstreamType) {
			redisClient.Del(ctx, key)
			logInfo(fmt.Sprintf("已删除无效缓存: %s", key))
		}
	}
}

// Server 表示上游服务器结构体
type Server struct {
	URL            string
	Healthy        bool
	BaseWeight     int
	DynamicWeight  int
	Alpha          float64
	ResponseTimes  []time.Duration
	RetryCount     int32
	circuitBreaker *CircuitBreaker
	mutex          sync.RWMutex
}

var (
	upstreamServers []*Server
	mu              sync.Mutex
)

// 初始化上游服务器列表，从环境变量加载
func initUpstreamServers() error {
	upstreamEnv := os.Getenv("UPSTREAM_SERVERS")
	if upstreamEnv == "" {
		return fmt.Errorf("错误: UPSTREAM_SERVERS 环境变量未设置")
	}
	
	servers := strings.Split(upstreamEnv, ",")
	validServers := make([]string, 0, len(servers))
	
	// 首先验证所有URL
	for _, serverURL := range servers {
		serverURL = strings.TrimSpace(serverURL)
		if serverURL == "" {
			continue
		}
		
		// 验证URL格式
		_, err := url.Parse(serverURL)
		if err != nil {
			logError(fmt.Sprintf("无效的服务器URL: %s, 错误: %v", serverURL, err))
			continue
		}
		
		validServers = append(validServers, serverURL)
	}
	
	if len(validServers) == 0 {
		return fmt.Errorf("错误: 没有有效的上游服务器URL")
	}
	
	// 初始化服务器列表
	upstreamServers = make([]*Server, 0, len(validServers))
	
	// 创建服务器实例
	for _, serverURL := range validServers {
		server := NewServer(serverURL)
		upstreamServers = append(upstreamServers, server)
		logInfo(fmt.Sprintf("添加上游服务器: %s", serverURL))
	}
	
	logInfo(fmt.Sprintf("成功初始化 %d 个上游服务器", len(upstreamServers)))
	return nil
}

// 权重更新和健康检查
func updateBaseWeights() {
	logInfo("开始服务健康检查和基础权重更新...")
	
	var wg sync.WaitGroup
	for _, server := range upstreamServers {
		wg.Add(1)
		go func(server *Server) {
			defer wg.Done()
			
			// 健康检查并获取响应时间
			healthy, responseTime, err := checkServerHealth(server)
			if err != nil {
				logError(fmt.Sprintf("服务器 %s 健康检查失败: %v", server.URL, err))
				server.mutex.Lock()
				server.Healthy = false
				server.mutex.Unlock()
				return
			}
			
			server.mutex.Lock()
			server.Healthy = healthy
			
			// 直接根据这次健康检查的响应时间计算基础权重
			server.BaseWeight = calculateBaseWeight(responseTime)
			
			// 记录这次响应时间
			server.ResponseTimes = append(server.ResponseTimes, responseTime)
			if len(server.ResponseTimes) > MaxResponseTimeRecords {
				server.ResponseTimes = server.ResponseTimes[1:]
			}
			server.mutex.Unlock()
			
			// 保存健康数据
			if err := saveHealthData(server, responseTime); err != nil {
				logError(fmt.Sprintf("保存健康数据失败: %v", err))
			}
			
			logDebug(fmt.Sprintf("服务器 %s 基础权重更新完成: 健康状态=%v, 响应时间=%v, 基础权重=%d", 
				server.URL, healthy, responseTime, server.BaseWeight))
		}(server)
	}
	
	wg.Wait()
	reportHealthStatus()
}

// 修改基础权重计算函数
func calculateBaseWeight(responseTime time.Duration) int {
	const baseExpectedRT = 1 * time.Second
	weight := int(float64(BaseWeightMultiplier) * float64(baseExpectedRT) / float64(responseTime))
	
	// 限制权重范围
	if weight < 10 {
		weight = 10
	} else if weight > 100 {
		weight = 100
	}
	
	logDebug(fmt.Sprintf("计算基础权重 - 响应时间: %v, 基准时间: %v, 权重: %d", 
		responseTime, baseExpectedRT, weight))
	
	return weight
}

// 计算动态权重
func calculateDynamicWeight(server *Server) int {
	// 如果服务器不健康，返回最低权重
	if !server.Healthy {
		logDebug(fmt.Sprintf("服务器 %s 不健康，动态权重设为1", server.URL))
		return 1
	}
	
	if len(server.ResponseTimes) == 0 {
		return server.DynamicWeight
	}
	
	// 计算最近记录的平均响应时间
	var totalRT time.Duration
	for _, rt := range server.ResponseTimes {
		totalRT += rt
	}
	avgRT := totalRT / time.Duration(len(server.ResponseTimes))
	
	// 基于平均响应时间计算权重
	const expectedRT = 500 * time.Millisecond
	weight := int(float64(DynamicWeightMultiplier) * float64(expectedRT) / float64(avgRT))
	
	// 限制权重范围
	if weight < 1 {
		weight = 1
	} else if weight > 100 {
		weight = 100
	}
	
	logDebug(fmt.Sprintf("服务器 %s 更新动态权重: 健康状态=true, 平均响应时间=%v, 样本数=%d, 新权重=%d", 
		server.URL, 
		avgRT,
		len(server.ResponseTimes),
		weight))
	
	return weight
}

// 修改算综合权重的函数
func calculateCombinedWeight(server *Server) int {
	if server == nil {
		return 0
	}
	
	server.mutex.RLock()
	defer server.mutex.RUnlock()
	
	// 使用 Alpha 来平衡基础权重和动态权重
	// Alpha 越大，基础权重的影响越大
	// (1-Alpha) 越大，动态权重的影响越大
	weight := int(server.Alpha*float64(server.BaseWeight) + 
		(1-server.Alpha)*float64(server.DynamicWeight))
	
	// 确保权重在合理范围内
	if weight < 1 {
		weight = 1
	} else if weight > 100 {
		weight = 100
	}
	
	logDebug(fmt.Sprintf("服务器 %s 综合权重计算: 基础权重=%d, 动态权重=%d, Alpha=%.2f, 最终权重=%d",
		server.URL,
		server.BaseWeight,
		server.DynamicWeight,
		server.Alpha,
		weight))
	
	return weight
}

// 可以添加 Alpha 调整函数
func (s *Server) adjustAlpha() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	
	// 计算基础权重和动态权重的差异
	weightDiff := math.Abs(float64(s.BaseWeight - s.DynamicWeight))
	currentAlpha := s.Alpha
	
	// 根据权重差异调整 Alpha
	if weightDiff > 30 {  // 权重差异大
		// 增加 Alpha，更倾向于使用基权重（更稳定）
		newAlpha := math.Min(currentAlpha + AlphaAdjustmentStep, 0.9)
		if newAlpha != currentAlpha {
			logDebug(fmt.Sprintf("服务器 %s Alpha 增加: %.2f -> %.2f (权重差异: %.0f)", 
				s.URL, currentAlpha, newAlpha, weightDiff))
			s.Alpha = newAlpha
		}
	} else {  // 权重差异小
		// 减小 Alpha，更倾向于使用动态权重（更灵活）
		newAlpha := math.Max(currentAlpha - AlphaAdjustmentStep, 0.1)
		if newAlpha != currentAlpha {
			logDebug(fmt.Sprintf("服务器 %s Alpha 减小: %.2f -> %.2f (权重差异: %.0f)", 
				s.URL, currentAlpha, newAlpha, weightDiff))
			s.Alpha = newAlpha
		}
	}
}

// 平均响应时间计算
func averageDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		logError("计算平均响应时间时，输入的 durations 列表为空")
		return 0 // 返回一个默认值
	}

	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	return sum / time.Duration(len(durations))
}

// 选择健康的上游服务器
func getWeightedRandomServer() *Server {
	weightCache.mu.RLock()
	defer weightCache.mu.RUnlock()
	
	var healthyServers []*Server
	totalWeight := 0
	
	for _, server := range upstreamServers {
		if server.Healthy {
			weight := weightCache.weights[server.URL]
			if weight > 0 {
				healthyServers = append(healthyServers, server)
				totalWeight += weight
			}
		}
	}
	
	if len(healthyServers) == 0 {
		return nil
	}
	
	r := rand.Intn(totalWeight)
	currentWeight := 0
	
	for _, server := range healthyServers {
		currentWeight += weightCache.weights[server.URL]
		if r < currentWeight {
			return server
		}
	}
	
	return healthyServers[len(healthyServers)-1]
}

// 修改：尝试其他上游服务器
func tryOtherUpstreams(uri string, r *http.Request, failedURL string) (*http.Response, []byte) {
	var wg sync.WaitGroup
	responses := make(chan struct {
		resp *http.Response
		body []byte
		url  string
	}, len(upstreamServers))

	for _, server := range upstreamServers {
		if !server.Healthy || server.URL == failedURL {
			continue
		}

		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
			goroutinePool <- struct{}{} // 获取 Goroutine 池中的一个槽
			defer func() { <-goroutinePool }() // 释放槽

			for i := 0; i < MaxRetryAttempts; i++ {
				client := &http.Client{Timeout: RequestTimeout}
				url := s.URL + uri
				req, err := http.NewRequest(r.Method, url, r.Body)
				if err != nil {
					logError(fmt.Sprintf("创建请求失败: %v", err))
					return
				}
				req.Header = r.Header

				resp, err := client.Do(req)
				if err != nil {
					logError(fmt.Sprintf("请求上游服务器失败: %v", err))
					continue
				}
				defer resp.Body.Close()

				body, err := io.ReadAll(resp.Body)
				if err != nil {
					logError(fmt.Sprintf("读取响应体失败: %v", err))
					continue
				}

				contentType := resp.Header.Get("Content-Type")
				if isValidResponse(body, contentType) {
					responses <- struct {
						resp *http.Response
						body []byte
						url  string
					}{resp, body, s.URL}
					logInfo(fmt.Sprintf("成功从其他上游服务器 %s 获取预期格式响应", s.URL))
					return
				} else {
					incrementRetryCount(s)
					if getRetryCount(s) >= 3 {
						s.mutex.Lock()
						s.Healthy = false
						s.mutex.Unlock()
						logError(fmt.Sprintf("上游服务器 %s 连续 %d 次返回非预期格式响应，标记为不健康", s.URL, getRetryCount(s)))
						return
					} else {
						logError(fmt.Sprintf("上游服务器 %s 返回非预期格式响应 (重试次数: %d/3)", s.URL, getRetryCount(s)))
					}
				}
			}
		}(server)
	}

	go func() {
		wg.Wait()
		close(responses)
	}()

	for resp := range responses {
		return resp.resp, resp.body
	}

	return nil, nil
}

func isValidResponse(data []byte, contentType string) bool {
	upstreamType := os.Getenv("UPSTREAM_TYPE")
	switch upstreamType {
	case "tmdb-api":
		return isValidJSON(data)
	case "tmdb-image":
		return isValidImage(data)
	case "custom":
		expectedContentType := os.Getenv("CUSTOM_CONTENT_TYPE")
		if expectedContentType == "" {
			logError("未设置 CUSTOM_CONTENT_TYPE 环境变量")
			return false
		}
		return strings.Contains(contentType, expectedContentType)
	default:
		logError(fmt.Sprintf("未知的 UPSTREAM_TYPE: %s", upstreamType))
		return false
	}
}

// 修改：handleProxyRequest 中处理非JSON响应的部分
func handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	metrics.RequestCount.Add(1)
	requestID := generateRequestID()
	uri := r.RequestURI

	logInfo(fmt.Sprintf("[%s] 收到请求: %s", requestID, uri))

	if data, hit := checkCaches(r); hit {
		w.Write(data)
		return
	}

	server := getWeightedRandomServer()
	if server == nil {
		metrics.ErrorCount.Add(1)
		http.Error(w, "没有可用的服务器", http.StatusServiceUnavailable)
		logError(fmt.Sprintf("[%s] 没有可用的服务器", requestID))
		return
	}

	logInfo(fmt.Sprintf("[%s] 使用服务器: %s", requestID, server.URL))

	var lastErr error
	for i := 0; i < MaxRetryAttempts; i++ {
		resp, body, err := tryRequest(server, r)
		if err != nil {
			lastErr = err
			continue
		}

		// 如果响应体格式不符合预期，尝试其他上游服务器
		if !isValidResponse(body, resp.Header.Get("Content-Type")) {
			logError(fmt.Sprintf("服务器 %s 返回非预期格式响应，尝试其他上游", server.URL))
			resp, body = tryOtherUpstreams(uri, r, server.URL)
			if resp != nil {
				handleSuccessResponse(w, resp, requestID, r)
				resetRetryCount(server)
				return
			}
		} else {
			handleSuccessResponse(w, resp, requestID, r)
			resetRetryCount(server)
			return
		}
	}

	markServerUnhealthy(server)
	metrics.ErrorCount.Add(1)
	http.Error(w, fmt.Sprintf("请求失败: %v", lastErr), http.StatusBadGateway)
}

// 获取健康服务器列表
func getHealthyServers() []*Server {
	var servers []*Server
	var healthyServers []*Server
	totalWeight := 0
	
	// 首先只选择康的服务器
	for _, server := range upstreamServers {
		// 确保 CircuitBreaker 已初始化
		if server.circuitBreaker == nil {
			server.circuitBreaker = &CircuitBreaker{
				threshold:    5,
				resetTimeout: time.Minute * 1,
			}
		}
		
		// 只选择健康且熔断器未触发的服务器
		if server.Healthy && !server.circuitBreaker.IsOpen() {
			healthyServers = append(healthyServers, server)
			weight := calculateCombinedWeight(server)
			if weight <= 0 {
				weight = 1
			}
			totalWeight += weight
			
			// 根据权重添加服务器
			for j := 0; j < weight; j++ {
				servers = append(servers, server)
			}
		}
	}
	
	// 如果有健康的服务器，直接返回
	if len(servers) > 0 {
		// 随机打乱服务器顺序
		if len(servers) > 1 {
			rand.Shuffle(len(servers), func(i, j int) {
				servers[i], servers[j] = servers[j], servers[i]
			})
		}
		return servers
	}
	
	// 如果没有健康的服务器，记录警告
	logWarning(fmt.Sprintf("没有健康的服务器可用！健康服务器数: %d, 总服务器数: %d", 
		len(healthyServers), 
		len(upstreamServers)))
	
	// 输出当前所有服务器的状态
	for _, s := range upstreamServers {
		status := "健康"
		if !s.Healthy {
			status = "不健康"
		}
		if s.circuitBreaker.IsOpen() {
			status += "(熔断器开启)"
		}
		logInfo(fmt.Sprintf("服务器 %s 状态: %s", s.URL, status))
	}
	
	// 在没有健康服务器的情况下，尝试使用响应时间最好的不健康服务器
	var bestServer *Server
	var bestResponseTime time.Duration = time.Hour
	
	for _, server := range upstreamServers {
		if len(server.ResponseTimes) > 0 {
			avgTime := calculateAverageResponseTime(server)
			if avgTime < bestResponseTime {
				bestResponseTime = avgTime
				bestServer = server
			}
		}
	}
	
	if bestServer != nil {
		logWarning(fmt.Sprintf("使用备用服务器: %s (平均响应时间: %v)", 
			bestServer.URL, 
			bestResponseTime))
		return []*Server{bestServer}
	}
	
	// 如果实在没有可用服务器返回空切片
	return nil
}

// 添加计算平响应时间的辅助函数
func calculateAverageResponseTime(server *Server) time.Duration {
	if len(server.ResponseTimes) == 0 {
		return time.Hour
	}
	
	var total time.Duration
	for _, rt := range server.ResponseTimes {
		total += rt
	}
	return total / time.Duration(len(server.ResponseTimes))
}

// 尝试请求函数
func tryRequest(server *Server, r *http.Request) (*http.Response, []byte, error) {
	if !server.Healthy {
		return nil, nil, fmt.Errorf("服务器 %s 当前不健康", server.URL)
	}
	
	client := &http.Client{Timeout: RequestTimeout}
	url := server.URL + r.RequestURI
	
	req, err := http.NewRequest(r.Method, url, r.Body)
	if err != nil {
		return nil, nil, err
	}
	
	start := time.Now()
	resp, err := client.Do(req)
	responseTime := time.Since(start)
	
	server.mutex.Lock()
	defer server.mutex.Unlock()
	
	if err != nil {
		server.RetryCount++
		if server.RetryCount >= MaxRetries {
			server.Healthy = false
			server.DynamicWeight = 1
			logError(fmt.Sprintf("服务器 %s 连续失败次数过多，标记为不健康", server.URL))
		}
		return nil, nil, err
	}
	
	// 请求成功，更新服务器状态
	server.RetryCount = 0
	server.ResponseTimes = append(server.ResponseTimes, responseTime)
	if len(server.ResponseTimes) > MaxResponseTimeRecords {
		server.ResponseTimes = server.ResponseTimes[len(server.ResponseTimes)-MaxResponseTimeRecords:]
	}
	
	// 只在请求成功时更新动态权重并记录日志
	if server.Healthy {
		server.DynamicWeight = calculateDynamicWeight(server)
		logDebug(fmt.Sprintf("服务器 %s 更新动态权重: 健康状态=true, 平均响应时间=%v, 样本数=%d, 新权重=%d", 
			server.URL, 
			averageDuration(server.ResponseTimes),
			len(server.ResponseTimes),
			server.DynamicWeight))
	}
	
	// 根据 UPSTREAM_TYPE 验证响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取响应体失败: %v", err)
	}

	upstreamType := os.Getenv("UPSTREAM_TYPE")
	if upstreamType == "tmdb-api" && !isValidJSON(body) {
		return nil, nil, fmt.Errorf("响应体不是合法的 JSON")
	} else if upstreamType == "tmdb-image" && !isValidImage(body) {
		return nil, nil, fmt.Errorf("响应体不是合法的图片")
	}
	
	return resp, body, nil
}

// 处理成功响应
func handleSuccessResponse(w http.ResponseWriter, resp *http.Response, requestID string, r *http.Request) {
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "读取响应失败", http.StatusInternalServerError)
		return
	}
	
	// 根据 UPSTREAM_TYPE 验证响应体
	upstreamType := os.Getenv("UPSTREAM_TYPE")
	if upstreamType == "tmdb-api" && !isValidJSON(body) {
		http.Error(w, "响应体不是合法的 JSON", http.StatusInternalServerError)
		return
	} else if upstreamType == "tmdb-image" && !isValidImage(body) {
		http.Error(w, "响应体不是合法的图片", http.StatusInternalServerError)
		return
	}
	
	// 复制响应头
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
	
	// 缓存处理
	if resp.StatusCode == http.StatusOK {
		addToCache(r, body)
	}
	
	logInfo(fmt.Sprintf("[%s] 请求成功完成", requestID))
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

// 新增：打印权重分布统计
func logWeightDistribution() {
	var totalWeight int
	weights := make(map[string]int)
	
	// 计算总权重
	for _, server := range upstreamServers {
		if server.Healthy {
			weight := calculateCombinedWeight(server)
			weights[server.URL] = weight
			totalWeight += weight
		}
	}
	
	// 打印权重分布
	logDebug("当前可用服务器权重分布:")
	for url, weight := range weights {
		percentage := float64(weight) * 100 / float64(totalWeight)
		logDebug(fmt.Sprintf("  - %s: 权重 %d (%.2f%%)", url, weight, percentage))
		
		server := findServerByURL(url)
		if server != nil {
			logDebug(fmt.Sprintf("    基础权重: %d, 动态权重: %d, Alpha: %.2f", 
				server.BaseWeight, server.DynamicWeight, server.Alpha))
		}
	}
}

// 辅助函数：根据URL找到对应的服务器
func findServerByURL(url string) *Server {
	for _, server := range upstreamServers {
		if server.URL == url {
			return server
		}
	}
	return nil
}

// 添加新的全局变量
var (
	metrics     = &Metrics{}
	useLocalCache bool  // 新增：标记是否使用本地缓存
	bufferPool  = sync.Pool{
		New: func() interface{} {
			return make([]byte, 32*1024)
		},
	}
)

// 新增 Metrics 结构体
type Metrics struct {
	RequestCount   atomic.Int64
	ErrorCount     atomic.Int64
	ResponseTimes  sync.Map  // URL -> []time.Duration
	CacheHits      atomic.Int64
	CacheMisses    atomic.Int64
	LocalCacheHits atomic.Int64
	RedisCacheHits atomic.Int64
	APIErrors      sync.Map    // URL -> error types count
}

// 添加指标收集
func collectMetrics() {
	ticker := time.NewTicker(METRICS_INTERVAL)
	for range ticker.C {
		logInfo(fmt.Sprintf(
			"性能指标 - 请求总数: %d, 错误数: %d, 缓存命中: %d (本地: %d, Redis: %d), 缓存未命中: %d",
			metrics.RequestCount.Load(),
			metrics.ErrorCount.Load(),
			metrics.CacheHits.Load(),
			metrics.LocalCacheHits.Load(),
			metrics.RedisCacheHits.Load(),
			metrics.CacheMisses.Load(),
		))
	}
}

// 添加指标接口
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"requests":      metrics.RequestCount.Load(),
		"errors":        metrics.ErrorCount.Load(),
		"cache_hits":    metrics.CacheHits.Load(),
		"local_hits":    metrics.LocalCacheHits.Load(),
		"redis_hits":    metrics.RedisCacheHits.Load(),
		"cache_misses": metrics.CacheMisses.Load(),
	})
}

// HealthStats 结构体定义
type HealthStats struct {
	windowSize int
	requests   []bool
	index      int
	mu         sync.RWMutex
}

// 新增 NewHealthStats 函数
func NewHealthStats(windowSize int) *HealthStats {
	return &HealthStats{
		windowSize: windowSize,
		requests:   make([]bool, windowSize),
	}
}

// CircuitBreaker 结构体定义 (删除重定义，只保留一处)
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
		// 如果超过重置时间，重置失败数
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

// ExponentialSmoothing 结构体定义
type ExponentialSmoothing struct {
	value float64
	alpha float64
}

// 添加请求追踪
func generateRequestID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int63())
}

// 添加定期健康状态报函数
func startHealthStatusReporting() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)  // 每五分钟报告一次
		for range ticker.C {
			reportHealthStatus()
		}
	}()
}

// 健康状态报告函数
func reportHealthStatus() {
	var unhealthyServers []string
	var healthyCount int
	var totalServers = len(upstreamServers)
	
	// 收集不健康的服务器
	for _, server := range upstreamServers {
		if !server.Healthy {
			unhealthyServers = append(unhealthyServers, server.URL)
		} else {
			healthyCount++
		}
	}
	
	// 构建状态消息
	if len(unhealthyServers) > 0 {
		logError(fmt.Sprintf(
			"服务器健康状态报告 - 总计: %d, 健康: %d, 不健康: %d\n不健康服务器列表: %s",
			totalServers,
			healthyCount,
			len(unhealthyServers),
			strings.Join(unhealthyServers, "\n"),
		))
	} else {
		logInfo(fmt.Sprintf(
			"服务器健康状态报告 - 所有服务器运行正常 (总计: %d)",
			totalServers,
		))
	}
	
	// 额报告熔断器状态
	reportCircuitBreakerStatus()
}

// 熔断器状态报告
func reportCircuitBreakerStatus() {
	var breakerOpenServers []string
	
	for _, server := range upstreamServers {
		if server.circuitBreaker.IsOpen() {
			breakerOpenServers = append(breakerOpenServers, fmt.Sprintf(
				"%s (失败次数: %d, 最后失败时间: %s)",
				server.URL,
				server.circuitBreaker.failures,
					server.circuitBreaker.lastFailure.Format("15:04:05"),
			))
		}
	}
	
	if len(breakerOpenServers) > 0 {
		logError(fmt.Sprintf(
			"熔断器状态报告 - 以下服务器熔断器已触发:\n%s",
			strings.Join(breakerOpenServers, "\n"),
		))
	}
}

func main() {
	// 初始化日志配置
	initLogging()
	
	// 初始化随机数种子
	rand.Seed(time.Now().UnixNano())
	
	// 启动权重更新
	go updateWeights()
	
	// 初始化 Redis
	initRedis()
	logInfo("Redis 初始化完成")
	
	// 初始化本地缓存
	if err := initCaches(); err != nil {
		log.Printf("警告: 本地缓存初始化失败: %v", err)
		useLocalCache = false
	} else {
		useLocalCache = true
		startCacheCleanup()
	}
	logInfo("本地缓存初始化完成")
	
	// 从环境变量加载上游服务器
	if err := initUpstreamServers(); err != nil {
		logError(fmt.Sprintf("初始化上游服务器失败: %v", err))
		os.Exit(1)
	}
	logInfo("上游服务器加载完成")
	
	// 启动健康检查
	role := os.Getenv("ROLE")
	if role == "host" {
		go func() {
			updateBaseWeights()
			ticker := time.NewTicker(WeightUpdateInterval)
			for range ticker.C {
				updateBaseWeights()
			}
		}()
	} else if role == "backend" {
		go func() {
			ticker := time.NewTicker(WeightUpdateInterval)
			for range ticker.C {
				if err := loadHealthData(); err != nil {
					logError(fmt.Sprintf("加载共享健康数据失败: %v", err))
				}
			}
		}()
	}
	
	// 启动健康状态报告
	startHealthStatusReporting()
	
	// 启动指标收集
	go collectMetrics()
	
	// 启动 swap 监控
	startSwapMonitoring()
	
	// 添加 swap 使用情况日志
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			if err := swapController.updateSwapUsage(); err != nil {
				logError(fmt.Sprintf("获取 swap 使用情况失败: %v", err))
				continue
			}
			swapUsage := swapController.getSwapUsagePercent()
			logInfo(fmt.Sprintf("Swap 使用情况 - 使用率: %.2f%%, 已用: %d MB, 总量: %d MB",
				swapUsage,
				swapController.swapUsed/1024,
				swapController.swapTotal/1024))
		}
	}()

	// 启动 HTTP 服务
	port := getEnv("PORT", "6637")
	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", port),
		ReadTimeout:  RequestTimeout,
		WriteTimeout: RequestTimeout,
	}
	
	// 注册路由
	http.HandleFunc("/", handleProxyRequest)
	http.HandleFunc("/metrics", handleMetrics)
	
	logInfo(fmt.Sprintf("HTTP 服务启动在端口 %s", port))
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// 修改缓存清理函数，增加安全检查
func startCacheCleanup() {
	if !useLocalCache || localCache == nil {
		return
	}
	
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			if localCache != nil {  // 再次检查以确保安全
				localCache.mutex.Lock()
				before := len(localCache.cache)
				for key, item := range localCache.cache {
					// 计算命中率
					duration := time.Since(item.LastAccess).Minutes()
					hitRate := float64(item.HitCount) / duration

					// 清理命中率低于阈值的项目
					if hitRate < 0.1 { // 设定命中率阈值
						delete(localCache.cache, key)
						logInfo(fmt.Sprintf("清理低命中率缓存项: %s, 命中率: %.2f", key, hitRate))
					}
				}
				after := len(localCache.cache)
				localCache.mutex.Unlock()
				logInfo(fmt.Sprintf("本地缓存清理完成: 清理前 %d 项, 清理后 %d 项", before, after))
			}
		}
	}()
}

// 定义健康查数据结构
type ServerHealth struct {
	URL            string    `json:"url"`
	Healthy        bool      `json:"healthy"`
	LastCheck      time.Time `json:"last_check"`
	ResponseTime   int64     `json:"response_time_ms"` // 毫秒
	BaseWeight     int       `json:"base_weight"`
	DynamicWeight  int       `json:"dynamic_weight"`
	ErrorCount     int       `json:"error_count"`
}

// 添加健康数据共享函数
func saveHealthData(server *Server, responseTime time.Duration) error {
	if server == nil {
		return fmt.Errorf("服务器对象为空")
	}

	// 确保 CircuitBreaker 已初始化
	if server.circuitBreaker == nil {
		server.circuitBreaker = &CircuitBreaker{
			threshold:    5,
			resetTimeout: time.Minute * 1,
		}
	}

	health := ServerHealth{
		URL:           server.URL,
		Healthy:       server.Healthy,
		LastCheck:     time.Now(),
		ResponseTime:  responseTime.Milliseconds(),
		BaseWeight:    server.BaseWeight,
		DynamicWeight: server.DynamicWeight,
		ErrorCount:    int(atomic.LoadInt32(&server.circuitBreaker.failures)),
	}
	
	data, err := json.Marshal(health)
	if err != nil {
		return fmt.Errorf("序列化健康数据失败: %v", err)
	}
	
	if redisClient == nil {
		return fmt.Errorf("Redis 客户端未初始化")
	}
	
	ctx := context.Background()
	key := HealthCheckPrefix + server.URL
	err = redisClient.Set(ctx, key, data, HealthDataTTL).Err()
	if err != nil {
		return fmt.Errorf("保存健康数据到 Redis 失败: %v", err)
	}
	
	logDebug(fmt.Sprintf("已保存服务器 %s 的健康数据", server.URL))
	return nil
}

// 添加健康数据读取函数
func loadHealthData() error {
	if redisClient == nil {
		return fmt.Errorf("Redis 客户端未初始化")
	}

	ctx := context.Background()
	iter := redisClient.Scan(ctx, 0, HealthCheckPrefix+"*", 0).Iterator()
	
	for iter.Next(ctx) {
		key := iter.Val()
		data, err := redisClient.Get(ctx, key).Bytes()
		if err != nil {
			logError(fmt.Sprintf("从Redis加载健康数失败 [%s]: %v", key, err))
			continue
		}
		
		var health ServerHealth
		if err := json.Unmarshal(data, &health); err != nil {
			logError(fmt.Sprintf("解析健康数据失败 [%s]: %v", key, err))
			continue
		}
		
		// 更新服务状
		for i := range upstreamServers {
			if upstreamServers[i].URL == health.URL {
				server := upstreamServers[i]
				server.mutex.Lock()
				server.Healthy = health.Healthy
				server.BaseWeight = health.BaseWeight
				
				server.DynamicWeight = health.DynamicWeight
				if upstreamServers[i].circuitBreaker != nil {
					atomic.StoreInt32(&upstreamServers[i].circuitBreaker.failures, int32(health.ErrorCount))
				}
				server.mutex.Unlock()
				break
			}
		}
	}
	
	if err := iter.Err(); err != nil {
		return fmt.Errorf("遍历Redis健康数据时出错: %v", err)
	}
	
	return nil
}

// 修改健康检查函数，返回响应时间
func checkServerHealth(server *Server) (bool, time.Duration, error) {
	client := &http.Client{Timeout: RequestTimeout}

	upstreamType := os.Getenv("UPSTREAM_TYPE")
	var healthCheckURL string

	switch upstreamType {
	case "tmdb-api":
			healthCheckURL = server.URL + "/3/configuration?api_key=" + os.Getenv("TMDB_API_KEY")
	case "tmdb-image":
			healthCheckURL = server.URL + "/t/p/original/7eOTFvo5gyXJIHVDURKorE6ERgU.jpg"
	case "custom":
			healthCheckURL = os.Getenv("CUSTOM_HEALTH_CHECK_URL")
		if healthCheckURL == "" {
			healthCheckURL = server.URL + "/health"
		}
	default:
		return false, 0, fmt.Errorf("未知的 UPSTREAM_TYPE")
	}

	start := time.Now()
	req, err := http.NewRequest("GET", healthCheckURL, nil)
	if err != nil {
		return false, 0, fmt.Errorf("创建健康检查请求失败: %v", err)
	}

	resp, err := client.Do(req)
	responseTime := time.Since(start)
	if err != nil {
		return false, responseTime, fmt.Errorf("健康检查请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应内容
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, responseTime, fmt.Errorf("读取健康检查响应失败: %v", err)
	}

	// 根据不同类型验证响应
	isValid := false
	switch upstreamType {
	case "tmdb-api":
		isValid = isValidJSON(body)
	case "tmdb-image":
		isValid = isValidImage(body)
	case "custom":
		customResponseCheck := os.Getenv("CUSTOM_RESPONSE_CHECK") == "true"
		customContentType := os.Getenv("CUSTOM_CONTENT_TYPE")

		if customResponseCheck {
			isValid = resp.StatusCode == http.StatusOK
		}
		if customContentType != "" {
			isValid = strings.Contains(resp.Header.Get("Content-Type"), customContentType)
		}
	}

	return isValid, responseTime, nil
}

// 添加警告日志函数
func logWarning(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[警告]%s [%s] %s", ColorYellow, ColorReset, hostname, message)
}

// 添加 Server 结构体的初始化方法
func NewServer(url string) *Server {
	return &Server{
		URL:            url,
		Alpha:         AlphaInitial,
		Healthy:       true,
		BaseWeight:     50,
		DynamicWeight:  50,
		ResponseTimes:  make([]time.Duration, 0, MaxResponseTimeRecords),
		RetryCount:     0,
		circuitBreaker: &CircuitBreaker{
			threshold:    5,
			resetTimeout: time.Minute * 1,
		},
		mutex:          sync.RWMutex{},
	}
}

// 修改重试关的函数，使用原子操作处理 RetryCount
func incrementRetryCount(s *Server) {
	atomic.AddInt32(&s.RetryCount, 1)
}

func resetRetryCount(s *Server) {
	atomic.StoreInt32(&s.RetryCount, 0)
}

func getRetryCount(s *Server) int32 {
	return atomic.LoadInt32(&s.RetryCount)
}

// 修改使用 RetryCount 的地方，使用上述辅助函数
func shouldRetry(s *Server, err error) bool {
	currentRetries := getRetryCount(s)
	if currentRetries >= MaxRetryAttempts {
		logError(fmt.Sprintf("服务器 %s 达到最大重试次数 %d", s.URL, MaxRetryAttempts))
		return false
	}
	
	// 检查错误类型是否可重试
	if isRetryableError(err) {
		incrementRetryCount(s)
		return true
	}
	
	return false
}

// 添加错误类型判断函数
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	
	// 检查是否为超时错误
	if os.IsTimeout(err) {
		return true
	}
	
	// 检查是否为临时网络错误
	if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
		return true
	}
	
	// 检查特定的错误字串
	errStr := err.Error()
	retryableErrors := []string{
		"connection reset by peer",
		"broken pipe",
		"no such host",
		"too many open files",
	}
	
	for _, retryableErr := range retryableErrors {
		if strings.Contains(strings.ToLower(errStr), retryableErr) {
			return true
		}
	}
	
	return false
}

func isValidImage(data []byte) bool {
	contentType := http.DetectContentType(data)
	return strings.HasPrefix(contentType, "image/")
}

func markServerUnhealthy(server *Server) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	server.Healthy = false
	logError(fmt.Sprintf("服务器 %s 被标记为不健康", server.URL))
}

func monitorCacheSize() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		if localCache != nil {
				stats := localCache.GetStats()
				logInfo(fmt.Sprintf("缓存统计 - 条目数: %d, 命中数: %d, 未命中数: %d",
					stats.ItemCount,
					stats.Hits,
					stats.Misses))
		}
	}
}

// 在 Server 结构体中添加方��
func (s *Server) afterRequest(responseTime time.Duration) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	
	// 更新响应间记录
	s.ResponseTimes = append(s.ResponseTimes, responseTime)
	if len(s.ResponseTimes) > MaxResponseTimeRecords {
		s.ResponseTimes = s.ResponseTimes[1:]
	}
	
	// 计算新的动态权重
	s.DynamicWeight = calculateDynamicWeight(s)
	
	// 调整 Alpha
	s.adjustAlpha()
}

// 添加判断连接错误的函数
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	// 只有真正的连接问题才返回 true
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout() || netErr.Temporary()
	}
	// 检查具体的错误类型
	errStr := err.Error()
	return strings.Contains(errStr, "connection refused") ||
		   strings.Contains(errStr, "no such host") ||
		   strings.Contains(errStr, "network is unreachable")
}

// 添加缓存的权重数据结构
type WeightCache struct {
	weights map[string]int
	mu      sync.RWMutex
}

// 全局权重缓存
var weightCache = &WeightCache{
	weights: make(map[string]int),
}

// 后台更新权重的函数
func updateWeights() {
	ticker := time.NewTicker(10 * time.Second) // 每10秒更新一次权重
	for range ticker.C {
		weightCache.mu.Lock()
		for _, server := range upstreamServers {
			if !server.Healthy {
				weightCache.weights[server.URL] = 0
				continue
			}
			
			weight := calculateCombinedWeight(server)
			weightCache.weights[server.URL] = weight
		}
		weightCache.mu.Unlock()
		
		// 可选：记录权重分布
		if currentLogLevel <= LogLevelDebug {
			logWeightDistribution()
		}
	}
}

// 初始化日志配置函数
func initLogging() {
	logLevel := strings.ToUpper(getEnv("LOG_LEVEL", "INFO"))
	switch logLevel {
	case "DEBUG":
		currentLogLevel = LogLevelDebug
	case "INFO":
		currentLogLevel = LogLevelInfo
	case "WARNING":
		currentLogLevel = LogLevelWarning
	case "ERROR":
		currentLogLevel = LogLevelError
	}

	logInfo(fmt.Sprintf("日志级别设置为: %s", logLevel))
}

// 添加获取日志级别的函数
func getLogLevelFromEnv() int {
	logLevel := strings.ToUpper(getEnv("LOG_LEVEL", "INFO"))
	switch logLevel {
	case "DEBUG":
		return LogLevelDebug
	case "WARNING":
		return LogLevelWarning
	case "ERROR":
		return LogLevelError
	default:
		return LogLevelInfo
	}
}

var goroutinePool = make(chan struct{}, 100) // 例如，限制最大并发 Goroutine 数量为 100

// 添加 swap 监控相关常量和结构体

type SwapController struct {
    mu            sync.RWMutex
    swapTotal     uint64
    swapUsed      uint64
    lastCleanup   time.Time
}

var swapController = &SwapController{}

// 获取 swap 使用情况
func (sc *SwapController) updateSwapUsage() error {
    sc.mu.Lock()
    defer sc.mu.Unlock()

    // 读取 /proc/meminfo 获取 swap 信息
    data, err := os.ReadFile("/proc/meminfo")
    if err != nil {
        return fmt.Errorf("读取内存信息失败: %v", err)
    }

    lines := strings.Split(string(data), "\n")
    for _, line := range lines {
        if strings.HasPrefix(line, "SwapTotal:") {
            fmt.Sscanf(line, "SwapTotal: %d kB", &sc.swapTotal)
        } else if strings.HasPrefix(line, "SwapFree:") {
            var swapFree uint64
            fmt.Sscanf(line, "SwapFree: %d kB", &swapFree)
            sc.swapUsed = sc.swapTotal - swapFree
        }
    }
    return nil
}

// 获取 swap 使用率
func (sc *SwapController) getSwapUsagePercent() float64 {
    sc.mu.RLock()
    defer sc.mu.RUnlock()
    
    if sc.swapTotal == 0 {
        return 0
    }
    return float64(sc.swapUsed) / float64(sc.swapTotal) * 100
}

// 添加 swap 监控和控制逻辑
func startSwapMonitoring() {
    go func() {
        ticker := time.NewTicker(SwapCheckInterval)
        for range ticker.C {
            if err := swapController.updateSwapUsage(); err != nil {
                logError(fmt.Sprintf("更新 swap 使用情况失败: %v", err))
                continue
            }

            swapUsage := swapController.getSwapUsagePercent()
            logDebug(fmt.Sprintf("当前 Swap 使用率: %.2f%%", swapUsage))

            switch {
            case swapUsage > CriticalSwapThreshold:
                logError(fmt.Sprintf("Swap 使用率严重过高 (%.2f%%), 执行紧急清理", swapUsage))
                emergencyCleanup()
                runtime.GC()
            case swapUsage > HighSwapThreshold:
                logWarning(fmt.Sprintf("Swap 使用率过高 (%.2f%%), 执行常规清理", swapUsage))
                normalCleanup()
            }
        }
    }()
}

// 紧急清理函数
func emergencyCleanup() {
    if localCache == nil {
        return
    }

    // 获取所有键
    keys := make([]string, 0)
    // 使用 bigcache 的迭代器获取所有键
    iterator := localCache.cache.Iterator()
    for iterator.SetNext() {
        if entry, err := iterator.Value(); err == nil {
            keys = append(keys, entry.Key())
        }
    }

    // 删除一半的缓存项
    for i := 0; i < len(keys)/2; i++ {
        localCache.Delete(keys[i])
    }

    logWarning(fmt.Sprintf("紧急清理完成: 清理了 %d 个缓存项", len(keys)/2))
    runtime.GC()
}

// 常规清理函数
func normalCleanup() {
    if localCache == nil {
        return
    }

    cleanupCount := 0
    iterator := localCache.cache.Iterator()
    for iterator.SetNext() {
        if entry, err := iterator.Value(); err == nil {
            if time.Since(time.Unix(0, entry.Timestamp())) > 30*time.Minute {
                localCache.Delete(entry.Key())
                cleanupCount++
            }
        }
    }

    logInfo(fmt.Sprintf("常规清理完成: 清理了 %d 个缓存项", cleanupCount))
}
