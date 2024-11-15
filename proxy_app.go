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
	MaxRetryAttempts = 3  // API可以少一些重试次数
	HealthCheckPrefix = "health:"
	HealthDataTTL    = 5 * time.Minute
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
	logMessage("信息", ColorBlue, message)
}

func logError(message string) {
	logMessage("错误", ColorRed, message)
}

func logDebug(message string) {
	logMessage("调试", ColorMagenta, message)
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
type LocalCache struct {
	cache *bigcache.BigCache
}

func (l *LocalCache) Get(key string) ([]byte, error) {
	return l.cache.Get(key)
}

func (l *LocalCache) Set(key string, value []byte) error {
	return l.cache.Set(key, value)
}

func (l *LocalCache) Delete(key string) error {
	return l.cache.Delete(key)
}

func (l *LocalCache) Len() int {
	return l.cache.Len()
}

func (l *LocalCache) Reset() {
	l.cache.Reset()
}

// 初始化缓存
var redisCache GeneralCache
var localCache LocalCacheInterface

func initCaches() error {
	// 初始化Redis缓存
	redisCache = &RedisCache{client: redisClient}

	// 初始化本地缓存
	config := bigcache.DefaultConfig(LocalCacheExpiration)
	config.MaxEntriesInWindow = 10000
	config.MaxEntrySize = 1 * 1024 * 1024
	config.HardMaxCacheSize = LocalCacheSize
	config.Verbose = false

	cache, err := bigcache.NewBigCache(config)
	if err != nil {
		return fmt.Errorf("初始化本地缓存失败: %v", err)
	}
	localCache = &LocalCache{cache: cache}

	return nil
}

// 修改后的缓存检查逻辑
func checkCaches(uri string) ([]byte, bool) {
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

// 修改后的缓存更新逻辑
func addToCache(uri string, data []byte) {
	// 计算数据的摘要（例如，长度或哈希值）
	dataSummary := fmt.Sprintf("长度: %d", len(data))
	// 或者使用哈希值
	// dataSummary := fmt.Sprintf("哈希: %x", sha256.Sum256(data))

	logDebug(fmt.Sprintf("更新缓存: %s, 数据摘要: %s", uri, dataSummary))

	// 更新Redis缓存
	if err := redisCache.Set(uri, data); err != nil {
		logError(fmt.Sprintf("Redis缓存更新失败 %s: %v", uri, err))
		return
	}

	// 更新本地缓存
	if err := localCache.Set(uri, data); err != nil {
		logError(fmt.Sprintf("本地缓存更新失败: %v", err))
	}

	logInfo(fmt.Sprintf("缓存更新成功: %s, 数据摘要: %s", uri, dataSummary))
}

// 辅助函数：截断数据以避免日志过长
func truncateData(data []byte) string {
	const maxLength = 200
	if len(data) > maxLength {
		return string(data[:maxLength]) + "..."
	}
	return string(data)
}

// 新增：清理非JSON缓存的函数
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
		
		if (upstreamType == "tmdb-api" && !isValidJSON(data)) || 
		   (upstreamType == "tmdb-image" && !isValidImage(data)) {
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
	RetryCount     int32           // 添加重试计数器
	circuitBreaker *CircuitBreaker
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
		log.Fatal(ColorRed + "错误: UPSTREAM_SERVERS 环境变量未设置" + ColorReset)
	}
	
	servers := strings.Split(upstreamEnv, ",")
	upstreamServers = make([]Server, 0, len(servers))  // 预分配切片容量
	
	for _, serverURL := range servers {
		server := NewServer(strings.TrimSpace(serverURL))
		upstreamServers = append(upstreamServers, server)
	}
	
	logInfo(fmt.Sprintf("已加载 %d 个上游服务器", len(upstreamServers)))
}

// 权重更新和健康检查
func updateBaseWeights() {
	logInfo("开始定期服务器健康检查和权重更新...")
	
	// 首先尝试加载共享的健康检查数据
	if err := loadHealthData(); err != nil {
		logError(fmt.Sprintf("加载共享健康数据失败: %v", err))
		// 继续执行，不要中断健康检查
	}
	
	var wg sync.WaitGroup
	for i := range upstreamServers {
		wg.Add(1)
		go func(server *Server) {
			defer wg.Done()
			
			start := time.Now()
			healthy, healthErr := checkServerHealth(server)
			responseTime := time.Since(start)
			
			server.mutex.Lock()
			server.Healthy = healthy
			if healthy {
				server.ResponseTimes = append(server.ResponseTimes, responseTime)
				if len(server.ResponseTimes) > RecentRequestLimit {
					server.ResponseTimes = server.ResponseTimes[1:]
				}
				server.BaseWeight = calculateBaseWeightEWMA(server)
				server.DynamicWeight = calculateDynamicWeight(server)
			} else {
				server.BaseWeight = 0
				server.DynamicWeight = 0
				if healthErr != nil {
					logError(fmt.Sprintf("服务器 %s 健康检查失败: %v", server.URL, healthErr))
				}
			}
			server.mutex.Unlock()
			
			// 保存健康检查数据到 Redis
			if err := saveHealthData(server, responseTime); err != nil {
				logError(fmt.Sprintf("保存健康检查数据失败: %v", err))
			}
		}(&upstreamServers[i])
	}
	
	wg.Wait()
	reportHealthStatus()
}

// 计基础权重的指数加权移动平均 (EWMA)
func calculateBaseWeightEWMA(server *Server) int {
	if len(server.ResponseTimes) == 0 {
		return server.BaseWeight
	}
	
	// 计算平均响应时间
	var total time.Duration
	for _, rt := range server.ResponseTimes {
		total += rt
	}
	avgResponseTime := total / time.Duration(len(server.ResponseTimes))
	
	// 使用 EWMA 计算权重
	weight := float64(server.BaseWeight)
	target := float64(BaseWeightMultiplier) / float64(avgResponseTime.Milliseconds())
	
	return int(weight*0.7 + target*0.3) // 平滑权重调整
}

// 计算动态权重
func calculateDynamicWeight(server *Server) int {
	if len(server.ResponseTimes) < 2 {
		return server.DynamicWeight
	}
	
	// 使用最近的响应时间计算动态权重
	latestRT := server.ResponseTimes[len(server.ResponseTimes)-1]
	weight := float64(DynamicWeightMultiplier) / float64(latestRT.Milliseconds())
	
	// 限制权重范围
	if weight < 1 {
		weight = 1
	} else if weight > 100 {
		weight = 100
	}
	
	return int(weight)
}

// 计综合权重
func calculateCombinedWeight(server *Server) int {
	if server == nil {
		return 0
	}
	
	server.mutex.RLock()
	defer server.mutex.RUnlock()
	
	// 基础权重和动态权重的组合
	weight := server.BaseWeight
	if server.DynamicWeight > 0 {
		weight = (weight + server.DynamicWeight) / 2
	}
	
	// 确保权重在合理范围内
	if weight < 1 {
		weight = 1
	} else if weight > 100 {
		weight = 100
	}
	
	return weight
}

// 平均响应时间计算
func averageDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		logError("计算平均响应时间时，输入的 durations 列表为空")
		return 0 // 回一个默认值
	}

	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	return sum / time.Duration(len(durations))
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
		logWarning("没有健康的服务器可用")
		return nil
	}

	// 选择服务器
	selectedServer := &healthyServers[rand.Intn(len(healthyServers))]

	// 记录详细日志
	logInfo(fmt.Sprintf("选择的服务器: %s", selectedServer.URL))
	selectedServer.mutex.RLock()
	defer selectedServer.mutex.RUnlock()

	avgResponseTime := averageDuration(selectedServer.ResponseTimes)
	logInfo(fmt.Sprintf("选择的服务器: %s", selectedServer.URL))
	logInfo(fmt.Sprintf("平均响应时间: %v", avgResponseTime))
	logInfo(fmt.Sprintf("基础权重: %d", selectedServer.BaseWeight))
	logInfo(fmt.Sprintf("动态权重: %d", selectedServer.DynamicWeight))
	logInfo(fmt.Sprintf("综合权重: %d", calculateCombinedWeight(selectedServer)))
	logInfo(fmt.Sprintf("总权重: %d, 选择比例: %.2f%%", totalWeight, float64(calculateCombinedWeight(selectedServer))*100/float64(totalWeight)))

	return selectedServer
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

			
			contentType := resp.Header.Get("Content-Type")
			if isValidResponse(body, contentType) {
				responses <- struct {
					resp *http.Response
					body []byte
					url  string
				}{resp, body, s.URL}
				logInfo(fmt.Sprintf("成功从其他上游服务器 %s 获取预期格式响应", s.URL))
			} else {
				incrementRetryCount(s)
				if getRetryCount(s) >= 3 {
					s.mutex.Lock()
					s.Healthy = false
					s.mutex.Unlock()
					logError(fmt.Sprintf("上游服务器 %s 连续 %d 次返回非预期格式响应，标记为不健康", s.URL, getRetryCount(s)))
				} else {
					logError(fmt.Sprintf("上游服务器 %s 返回非预期格式响应 (重试次数: %d/3)", s.URL, getRetryCount(s)))
				}
			}
		}(server)
	}

	// 等待所有请求完成或者收到第一个成功的响应
	go func() {
		wg.Wait()
		close(responses)
	}()

	// 获第一个成功的响应
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
	logInfo(fmt.Sprintf("处理代理请求, 请求ID: %s, URI: %s", requestID, uri))

	logInfo(fmt.Sprintf("[%s] 收到请求: %s", requestID, uri))

	// 检查缓存
	if data, found := checkCaches(uri); found {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		w.Write(data)
		logInfo(fmt.Sprintf("[%s] 缓存中，URI: %s，数据长度: %d", requestID, uri, len(data)))
		return
	}

	// 获取加权随机的健康服务器
	server := getWeightedRandomServer()
	if server == nil {
		metrics.ErrorCount.Add(1)
		http.Error(w, "没有可用的服务器", http.StatusServiceUnavailable)
		logError(fmt.Sprintf("[%s] 没有可用的服务器", requestID))
		return
	}

	// 尝试请求
	var lastErr error
	for i := 0; i < MaxRetryAttempts; i++ {
		logInfo(fmt.Sprintf("[%s] 尝试使用服务器: %s", requestID, server.URL))
		resp, err := tryRequest(server, r)
		if err != nil {
			lastErr = err
			logError(fmt.Sprintf("[%s] 服务器 %s 请求失败: %v", requestID, server.URL, err))
			continue
		}

		// 处理成功响应
		handleSuccessResponse(w, resp, requestID)
		resetRetryCount(server)
		return
	}

	// 如果多次重试后仍然失败，将服务器标记为不健康
	markServerUnhealthy(server)

	metrics.ErrorCount.Add(1)
	http.Error(w, fmt.Sprintf("所有可用上游服务器都失败: %v", lastErr), http.StatusBadGateway)
	logError(fmt.Sprintf("[%s] 所有上游服务器请求失败", requestID))
}

// 获取健康服务器列表
func getHealthyServers() []*Server {
	var servers []*Server
	var healthyServers []*Server
	totalWeight := 0
	
	// 首先只选择健康的服务器
	for i := range upstreamServers {
		server := &upstreamServers[i]
		
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
	
	// 在没有健康服务器的情况下，尝试使用响应时最好的不健康服务器
	var bestServer *Server
	var bestResponseTime time.Duration = time.Hour
	
	for i := range upstreamServers {
		server := &upstreamServers[i]
		if len(server.ResponseTimes) > 0 {
			avgTime := calculateAverageResponseTime(server)
			if avgTime < bestResponseTime {
				bestResponseTime = avgTime
				bestServer = server
			}
		}
	}
	
	if bestServer != nil {
		logWarning(fmt.Sprintf("使用备服务器: %s (平均响应时间: %v)", 
			bestServer.URL, 
			bestResponseTime))
		return []*Server{bestServer}
	}
	
	// 如果实在没可用服务器返回空切片
	return nil
}

// 添加计算平���响应时间的辅助函数
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
func tryRequest(server *Server, r *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: RequestTimeout}
	url := server.URL + r.RequestURI
	
	req, err := http.NewRequest(r.Method, url, r.Body)
	if err != nil {
		return nil, err
	}
	
	// 复制原始求头
	req.Header = make(http.Header)
	for k, v := range r.Header {
		req.Header[k] = v
	}
	
	start := time.Now()
	resp, err := client.Do(req)
	responseTime := time.Since(start)
	
	if err != nil {
		return nil, err
	}
	
	// 记录响应时
	server.mutex.Lock()
	server.ResponseTimes = append(server.ResponseTimes, responseTime)
	if len(server.ResponseTimes) > RecentRequestLimit {
		server.ResponseTimes = server.ResponseTimes[1:]
	}
	server.mutex.Unlock()
	
	return resp, nil
}

// 处理成功响应
func handleSuccessResponse(w http.ResponseWriter, resp *http.Response, requestID string) {
	defer resp.Body.Close()
	
	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "读取响应失败", http.StatusInternalServerError)
		return
	}
	
	// 复制响应头
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	
	// 设置状态码
	w.WriteHeader(resp.StatusCode)
	
	// 写入响应体
	w.Write(body)
	
	// 如果是缓存的响应，添加到缓存
	if resp.StatusCode == http.StatusOK {
		addToCache(requestID, body)
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
	
	// 计总权重
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
			"服务器健康状态报告 - 总计: %d, 健康: %d, 不健康: %d\n不健康服务器列表:\n%s",
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
	// 初始化随机数种子
	rand.Seed(time.Now().UnixNano())
	
	// 1. 初始化 Redis
	initRedis()
	logInfo("Redis 初始化完成")
	
	// 2. 初始化本地缓存
	if err := initCaches(); err != nil {
		log.Printf("警告: 本地缓存初始化失败: %v", err)
		useLocalCache = false
	} else {
		useLocalCache = true
		startCacheCleanup()
	}
	logInfo("本地缓存初始化完成")
	
	// 3. 从环境变量加载上游服务器
	initUpstreamServers()
	logInfo("上游服务器加载完成")
	
	// 4. 启动健康检查（包括加载共享健康数据）
	role := os.Getenv("ROLE")

	if role == "host" {
		// 执行健康检查并写入 Redis
		go func() {
			// 首次健康检查
			updateBaseWeights()
			
			// 定期健康检查
			ticker := time.NewTicker(WeightUpdateInterval)
			for range ticker.C {
				updateBaseWeights()
			}
		}()
	} else if role == "backend" {
		// 只从 Redis 读取健康的务器列表
		go func() {
			ticker := time.NewTicker(WeightUpdateInterval)
			for range ticker.C {
				if err := loadHealthData(); err != nil {
					logError(fmt.Sprintf("加载共享健康数据失败: %v", err))
				}
			}
		}()
	}
	
	// 5. 启动健康状态报告
	startHealthStatusReporting()
	
	// 6. 启动指标收集
	go collectMetrics()
	
	// 7. 启动 HTTP 服务
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
				before := localCache.Len()
				localCache.Reset()
				after := localCache.Len()
				logInfo(fmt.Sprintf("本地缓存清理完成: 清理前 %d 项, 清理后 %d 项", before, after))
			}
		}
	}()
}

// 定义健康检查数据结构
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
			logError(fmt.Sprintf("从Redis加载健康数据失败 [%s]: %v", key, err))
			continue
		}
		
		var health ServerHealth
		if err := json.Unmarshal(data, &health); err != nil {
			logError(fmt.Sprintf("解析健康数据失败 [%s]: %v", key, err))
			continue
		}
		
		// 更新服务器状态
		for i := range upstreamServers {
			if upstreamServers[i].URL == health.URL {
				upstreamServers[i].mutex.Lock()
				upstreamServers[i].Healthy = health.Healthy
				upstreamServers[i].BaseWeight = health.BaseWeight
				
				upstreamServers[i].DynamicWeight = health.DynamicWeight
				if upstreamServers[i].circuitBreaker != nil {
					atomic.StoreInt32(&upstreamServers[i].circuitBreaker.failures, int32(health.ErrorCount))
				}
				upstreamServers[i].mutex.Unlock()
				break
			}
		}
	}
	
	if err := iter.Err(); err != nil {
		return fmt.Errorf("遍历Redis健康数据时出错: %v", err)
	}
	
	return nil
}

// 修改健康检查函数
func checkServerHealth(server *Server) (bool, error) {
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
			healthCheckURL = server.URL + "/health" // 默值
		}
	default:
		log.Fatal("未知的 UPSTREAM_TYPE")
	}

	req, err := http.NewRequest("GET", healthCheckURL, nil)
	if err != nil {
		return false, fmt.Errorf("创建健康检查请求失败: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("健康检查请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 根据 upstream_type 检测内容类型
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("读取健康检查响应失败: %v", err)
	}

	switch upstreamType {
	case "tmdb-api":
		if !isValidJSON(body) {
			return false, fmt.Errorf("康检查返回无效的 JSON 响应")
		}
	case "tmdb-image":
		if !isValidImage(body) {
			return false, fmt.Errorf("健康检查返回非图片响应")
		}
	case "custom":
		// 自定义响应检查
		customResponseCheck := os.Getenv("CUSTOM_RESPONSE_CHECK") == "true"
		customContentType := os.Getenv("CUSTOM_CONTENT_TYPE")

		if customResponseCheck {
			if resp.StatusCode != http.StatusOK {
				return false, fmt.Errorf("自定义健康检查返回非 200 状态码")
			}
		}

		if customContentType != "" {
			contentType := resp.Header.Get("Content-Type")
			if !strings.Contains(contentType, customContentType) {
				return false, fmt.Errorf("自定义健康检查返回的内容类型不匹配: 期望 %s, 实际 %s", customContentType, contentType)
			}
		}
	}

	return true, nil
}

// 添加警告日志函数
func logWarning(message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[警告]%s [%s] %s", ColorYellow, ColorReset, hostname, message)
}

// 添加 Server 结构体的初始化方法
func NewServer(url string) Server {
	return Server{
		URL:           url,
		Alpha:         AlphaInitial,
		Healthy:       true,
		BaseWeight:    50,
		RetryCount:    0,          // 初始化重试计数器
		circuitBreaker: &CircuitBreaker{
			threshold:    5,
			resetTimeout: time.Minute * 1,
		},
		mutex:         sync.RWMutex{},
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
	
	// 检查特定的错误字符串
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
