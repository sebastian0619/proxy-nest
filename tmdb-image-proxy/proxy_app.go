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
	
	"github.com/redis/go-redis/v9"
)

// 配置常量
const (
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorReset  = "\033[0m"
	MaxRetryAttempts = 5
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
}

var (
	upstreamServers []Server
	mu              sync.Mutex
)

// 初始化上游服���器列表，从环境变量加载
func initUpstreamServers() {
	upstreamEnv := os.Getenv("UPSTREAM_SERVERS")
	if upstreamEnv == "" {
		log.Fatal(ColorRed + "错误: UPSTREAM_SERVERS 环境变量未设置" + ColorReset)
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
	uri := r.RequestURI

	// 检查缓存
	if data, found := checkCache(uri); found {
		contentType := http.DetectContentType(data)
		if isImageContentType(contentType) {
			logInfo(fmt.Sprintf("命中缓存: %s", uri))
			w.Header().Set("Content-Type", contentType)
			w.Write(data)
			return
		}
	}
	logDebug(fmt.Sprintf("未命中缓存: %s", uri))

	// 尝试获取图片，最多重试5次
	var lastErr error
	attemptedServers := make(map[string]bool)
	
	for attempt := 0; attempt < MaxRetryAttempts; attempt++ {
		server := getWeightedRandomServer()
		if server == nil {
			http.Error(w, "没有可用的健康上游服务器", http.StatusBadGateway)
			return
		}

		// 检查是否已经尝试过这个服务器
		if attemptedServers[server.URL] {
			continue
		}
		attemptedServers[server.URL] = true

		logInfo(fmt.Sprintf("尝试第 %d 次请求，使用服务器: %s", attempt+1, server.URL))
		body, contentType, statusCode, err := tryFetchImage(server, r)
		
		if err != nil {
			lastErr = err
			logError(fmt.Sprintf("请求失败: %s - %v", server.URL, err))
			continue
		}

		if !isImageContentType(contentType) {
			logError(fmt.Sprintf("服务器 %s 返回非图片格式: %s", server.URL, contentType))
			markServerUnhealthy(server)
			continue
		}

		// 成功获取图片
		logInfo(fmt.Sprintf("成功获取图片: %s", server.URL))
		addToCache(uri, body, contentType)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(statusCode)
		w.Write(body)
		return
	}

	// 所有尝试都失败
	http.Error(w, fmt.Sprintf("经过 %d 次尝试后仍未能获取图片: %v", MaxRetryAttempts, lastErr), http.StatusBadGateway)
}

// 添加尝试获取图片的函数
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

func main() {
	// 初始化随机数种子
	rand.Seed(time.Now().UnixNano())
	
	// 初始化Redis
	initRedis()
	
	// 从环境变量加载上游服务器
	initUpstreamServers()
	
	// 启动健康检查定时任务
	go func() {
		updateBaseWeights()
		ticker := time.NewTicker(WeightUpdateInterval)
		for range ticker.C {
			updateBaseWeights()
		}
	}()
	
	// 启动清理缓存的定时任务
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			cleanNonImageCache()
		}
	}()
	
	// 启动HTTP服务
	port := getEnv("PORT", "6637")
	logInfo(fmt.Sprintf("服务器启动在端口 %s", port))
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), nil))
}
