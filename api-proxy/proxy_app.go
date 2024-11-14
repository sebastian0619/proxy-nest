package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"encoding/json"
	
	"github.com/redis/go-redis/v9"
	"github.com/allegro/bigcache/v3"
	"sync/atomic"
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
	LocalCacheExpiration = getDurationEnv("LOCAL_CACHE_EXPIRATION_MINUTES", 10) * time.Minute
	LocalCacheSize       = getIntEnv("LOCAL_CACHE_SIZE_MB", 100) * 1024 * 1024
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
	var js interface{}
	return json.Unmarshal(data, &js) == nil
}

// 修改：检查缓存函数
func checkCache(uri string) ([]byte, bool) {
	ctx := context.Background()
	data, err := redisClient.Get(ctx, uri).Bytes()
	if err == redis.Nil {
		return nil, false
	}
	if err != nil {
		logError(fmt.Sprintf("Redis获取错误: %v", err))
		return nil, false
	}
	
	// 验证缓存数据是否为空或非JSON格式
	if len(data) == 0 || !isValidJSON(data) {
		// 删除无效缓存
		redisClient.Del(ctx, uri)
		logError("缓存数据无效或非JSON格式，已删除")
		return nil, false
	}
	
	return data, true
}

// 修改：添加到缓存函数
func addToCache(uri string, data []byte) {
	// 验证数据是否为空或非JSON格式
	if len(data) == 0 || !isValidJSON(data) {
		logError("尝试缓存无效数据或非JSON格式数据")
		return
	}
	
	ctx := context.Background()
	err := redisClient.Set(ctx, uri, data, CacheTTL).Err()
	if err != nil {
		logError(fmt.Sprintf("缓存数据失败 %s: %v", uri, err))
		return
	}
	
	logInfo(fmt.Sprintf("成功缓存JSON数据，URI: %s, 数据大小: %d bytes", uri, len(data)))
}

// 新增：清理非JSON缓存的函数
func cleanInvalidCache() {
	ctx := context.Background()
	iter := redisClient.Scan(ctx, 0, "*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		data, err := redisClient.Get(ctx, key).Bytes()
		if err != nil {
			continue
		}
		
		if !isValidJSON(data) {
			redisClient.Del(ctx, key)
			logInfo(fmt.Sprintf("已删除非JSON缓存: %s", key))
		}
	}
}

// 日志函数
func logInfo(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[信息]%s [%s] %s", ColorBlue, ColorReset, hostname, message)
}

func logError(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[错误]%s [%s] %s", ColorRed, ColorReset, hostname, message)
}

func logDebug(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[调试]%s [%s] %s", ColorMagenta, ColorReset, hostname, message)
}

// Server 表示上游服务器结构体
type Server struct {
	URL            string
	Healthy        bool
	BaseWeight     int
	DynamicWeight  int
	Alpha          float64
	ResponseTimes  []time.Duration
	RetryCount     int
	mutex          sync.RWMutex
}

var (
	upstreamServers []Server
	mu              sync.Mutex
)

// 初始化上游服务器列表，从环境变量加载
func initUpstreamServers() {
	upstreamEnv := os.Getenv("UPSTREAM_SERVERS")
	if upstreamEnv == "" {
		log.Fatal("UPSTREAM_SERVERS 环境变量未设置。")
	}
	servers := strings.Split(upstreamEnv, ",")
	for _, serverURL := range servers {
		upstreamServers = append(upstreamServers, Server{
			URL:        strings.TrimSpace(serverURL),
			Alpha:      AlphaInitial,
			Healthy:    true,
			BaseWeight: 50,
		})
	}
	logInfo("Loaded upstream servers")
}

// 权重更新和健康检查
func updateBaseWeights() {
	logInfo("Starting periodic server health check and weight update...")
	var wg sync.WaitGroup
	for i := range upstreamServers {
		wg.Add(1)
		go func(server *Server) {
			defer wg.Done()
			client := &http.Client{Timeout: RequestTimeout}
			start := time.Now()
			resp, err := client.Get(server.URL + "/3/movie/latest?api_key=59ef6336a19540cd1e254cae0565906e")
			responseTime := time.Since(start)
			
			if err != nil {
				server.mutex.Lock()
				server.Healthy = false
				server.BaseWeight = 0
				server.mutex.Unlock()
				logError(fmt.Sprintf("[Health Check] Failed to reach %s: %v", server.URL, err))
				return
			}
			defer resp.Body.Close()
			
			// 读取响应体并验证JSON格式
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				server.mutex.Lock()
				server.Healthy = false
				server.BaseWeight = 0
				server.mutex.Unlock()
				logError(fmt.Sprintf("[Health Check] Failed to read response from %s: %v", server.URL, err))
				return
			}
			
			// 验证JSON格式
			if !isValidJSON(body) {
				server.mutex.Lock()
				server.Healthy = false
				server.BaseWeight = 0
				logError(fmt.Sprintf("[Health Check] Server %s returned invalid JSON response", server.URL))
				server.mutex.Unlock()
				return
			}
			
			// 健康检查通过
			if resp.StatusCode == http.StatusOK {
				server.mutex.Lock()
				server.ResponseTimes = append(server.ResponseTimes, responseTime)
				if len(server.ResponseTimes) > RecentRequestLimit {
					server.ResponseTimes = server.ResponseTimes[1:]
				}
				server.BaseWeight = calculateBaseWeightEWMA(server)
				server.DynamicWeight = calculateDynamicWeight(server)
				server.Healthy = true
				server.RetryCount = 0  // 健康检查通过时重置重试计数器
				server.mutex.Unlock()
				
				logInfo(fmt.Sprintf("[Health Check] Server %s is healthy with response time %v ms, base weight %d, alpha %.6f",
					server.URL, responseTime.Milliseconds(), server.BaseWeight, server.Alpha))
			} else {
				server.mutex.Lock()
				server.Healthy = false
				server.BaseWeight = 0
				server.mutex.Unlock()
				logError(fmt.Sprintf("[Health Check] Server %s returned status code %d", server.URL, resp.StatusCode))
			}
		}(&upstreamServers[i])
	}
	wg.Wait()
	logWeightDistribution()
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

	for _, server := range upstreamServers {
		if server.Healthy {
			combinedWeight := calculateCombinedWeight(&server)
			totalWeight += combinedWeight
			for i := 0; i < combinedWeight; i++ {
				healthyServers = append(healthyServers, server)
			}
		}
	}

	if len(healthyServers) == 0 {
		return nil
	}

	return &healthyServers[rand.Intn(len(healthyServers))]
}

// 修改：尝试其他上游服务器
func tryOtherUpstreams(uri string, r *http.Request, failedURL string) (*http.Response, []byte) {
	var wg sync.WaitGroup
	responses := make(chan struct {
		resp *http.Response
		body []byte
		url  string
	}, len(upstreamServers))

	// 遍历所有健康的上游服务器（除了刚刚失败的那个）
	for i := range upstreamServers {
		server := &upstreamServers[i]
		// 跳过不健康的服务器和刚刚失败的服务器
		if !server.Healthy || server.URL == failedURL {
			continue
		}

		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
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
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				logError(fmt.Sprintf("读取响应体失败: %v", err))
				return
			}

			if isValidJSON(body) {
				responses <- struct {
					resp *http.Response
					body []byte
					url  string
				}{resp, body, s.URL}
				logInfo(fmt.Sprintf("成功从其他上游服务器 %s 获取JSON响应", s.URL))
			} else {
				s.mutex.Lock()
				s.RetryCount++
				if s.RetryCount >= 5 {
					s.Healthy = false
					logError(fmt.Sprintf("上游服务器 %s 连续 %d 次返回非JSON响应，标记为不健康", 
						s.URL, s.RetryCount))
				} else {
					logError(fmt.Sprintf("上游服务器 %s 返回非JSON格式响应 (重试次数: %d/5)", 
						s.URL, s.RetryCount))
				}
				s.mutex.Unlock()
			}
		}(server)
	}

	// 等待所有请求完成或者收到第一个成功的响应
	go func() {
		wg.Wait()
		close(responses)
	}()

	// 获取第一个成功的响应
	for resp := range responses {
		return resp.resp, resp.body
	}

	return nil, nil
}

// 修改：handleProxyRequest 中处理非JSON响应的部分
func handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	metrics.RequestCount.Add(1)
	uri := r.RequestURI

	// 检查缓存
	if data, found := checkCaches(uri); found {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		logInfo(fmt.Sprintf("%s[命中缓存]%s - %s", ColorGreen, ColorReset, uri))
		w.Write(data)
		return
	}
	logInfo(fmt.Sprintf("%s[未命中缓存]%s - %s", ColorYellow, ColorReset, uri))

	// 获取健康的上游服务器
	server := getWeightedRandomServer()
	if server == nil {
		http.Error(w, "无可用的上游服务器", http.StatusBadGateway)
		return
	}

	client := &http.Client{Timeout: RequestTimeout}
	url := server.URL + uri
	req, err := http.NewRequest(r.Method, url, r.Body)
	if err != nil {
		// 请求创建失败，尝试其他上游
		if resp, body := tryOtherUpstreams(uri, r, server.URL); resp != nil {
			handleSuccessResponse(w, resp, body, uri)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header = r.Header

	// 发请求并处理响应
	start := time.Now()
	resp, err := client.Do(req)
	responseTime := time.Since(start)
	
	if err != nil {
		logError(fmt.Sprintf("请求上游服务器 %s 失败: %v", server.URL, err))
		// 连接失败，立即尝试其他上游
		if resp, body := tryOtherUpstreams(uri, r, server.URL); resp != nil {
			handleSuccessResponse(w, resp, body, uri)
			return
		}
		http.Error(w, "所有上游服务器均无响应", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logError(fmt.Sprintf("读取响应体失败: %v", err))
		// 读取失败，尝试其他上游
		if resp, body := tryOtherUpstreams(uri, r, server.URL); resp != nil {
			handleSuccessResponse(w, resp, body, uri)
			return
		}
		http.Error(w, "读取响应失败", http.StatusInternalServerError)
		return
	}

	// 验证响应是否为有效的JSON
	if !isValidJSON(body) {
		logError(fmt.Sprintf("上游服务器 %s 返回非JSON格式响应", server.URL))
		server.mutex.Lock()
		server.RetryCount++
		if server.RetryCount >= 5 {
			server.Healthy = false
			logError(fmt.Sprintf("上游服务器 %s 连续 %d 次返回非JSON响应，标记为不健康", 
				server.URL, server.RetryCount))
		}
		server.mutex.Unlock()

		// 非JSON响应，尝试其他上游
		if resp, body := tryOtherUpstreams(uri, r, server.URL); resp != nil {
			handleSuccessResponse(w, resp, body, uri)
			return
		}
		http.Error(w, "所有上游服务器均返回非JSON响应", http.StatusBadGateway)
		return
	}

	// 处理成功响应
	handleSuccessResponse(w, resp, body, uri)
	
	logInfo(fmt.Sprintf("%s[已选择上游]%s %s - 响应时间: %v ms - 基础权重: %d - 动态权重: %d - 综合权重: %d",
		ColorCyan, ColorReset,
		server.URL, 
		responseTime.Milliseconds(), 
		server.BaseWeight, 
		server.DynamicWeight, 
		calculateCombinedWeight(server)))
}

// 新增：处理成功响应的辅助函数
func handleSuccessResponse(w http.ResponseWriter, resp *http.Response, body []byte, uri string) {
	// 设置响应头
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	
	// 只有成功的请求才缓存
	if resp.StatusCode == http.StatusOK {
		addToCache(uri, body)
	}
	
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
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
			weight := calculateCombinedWeight(&server)
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
	for i := range upstreamServers {
		if upstreamServers[i].URL == url {
			return &upstreamServers[i]
		}
	}
	return nil
}

// 添加新的全局变量
var (
	metrics     = &Metrics{}
	localCache  *bigcache.BigCache
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
}

// 初始化本地缓存
func initCache() error {
	config := bigcache.DefaultConfig(LocalCacheExpiration)
	config.MaxEntriesInWindow = 10000
	config.MaxEntrySize = 1 * 1024 * 1024  // API响应通常小于1MB
	config.HardMaxCacheSize = LocalCacheSize
	config.Verbose = false
	
	var err error
	localCache, err = bigcache.NewBigCache(config)
	if err != nil {
		return fmt.Errorf("初始化本地缓存失败: %v", err)
	}
	return nil
}

// 修改缓存检查逻辑
func checkCaches(uri string) ([]byte, bool) {
	// 1. 先检查本地缓存
	if data, err := localCache.Get(uri); err == nil {
		if isValidJSON(data) {
			metrics.LocalCacheHits.Add(1)
			return data, true
		}
	}

	// 2. 检查 Redis 缓存
	if data, found := checkCache(uri); found {
		if isValidJSON(data) {
			metrics.RedisCacheHits.Add(1)
			// 更新本地缓存
			if err := localCache.Set(uri, data); err != nil {
				logError(fmt.Sprintf("更新本地缓存失败: %v", err))
			}
			return data, true
		}
	}

	metrics.CacheMisses.Add(1)
	return nil, false
}

// 添加指标收集
func collectMetrics() {
	ticker := time.NewTicker(1 * time.Minute)
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

// CircuitBreaker 结构体定义 (删除重复定义，只保留一处)
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

// ExponentialSmoothing 结构体定义
type ExponentialSmoothing struct {
	value float64
	alpha float64
}

func main() {
	// 初始化随机数种子
	rand.Seed(time.Now().UnixNano())
	
	// 初始化Redis
	initRedis()
	
	// 从环境变量加载上游服务器
	initUpstreamServers()
	
	// 启动健康检查
	updateBaseWeights()
	
	// 启动HTTP服务
	http.HandleFunc("/", handleProxyRequest)
	port := getEnv("PORT", "6637")
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), nil))
	
	// 添加定时清理非JSON缓存的任务
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		for range ticker.C {
			cleanInvalidCache()
		}
	}()
	
	// 初始化缓存
	if err := initCache(); err != nil {
		log.Printf("警告: 本地缓存初始化失败: %v", err)
	}
	
	// 启动指标收集
	go collectMetrics()
	
	// 添加指标接口
	http.HandleFunc("/metrics", handleMetrics)
}