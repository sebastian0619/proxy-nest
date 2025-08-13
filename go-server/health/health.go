package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proxy-nest-go/config"
	"proxy-nest-go/logger"
)

// HealthStatus 健康状态
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// Server 服务器信息
type Server struct {
	URL            string
	Status         HealthStatus
	BaseWeight     int           // 基础权重（基于连接率）
	DynamicWeight  int           // 动态权重（基于响应时间）
	CombinedWeight int           // 综合权重
	Priority       int           // 优先级（基于连接率）
	ErrorCount     int           // 错误次数
	LastCheckTime  time.Time     // 最后检查时间
	LastEWMA       float64       // 最后指数加权移动平均响应时间
	ResponseTimes  []time.Duration // 响应时间记录
	// 连接率相关字段
	TotalRequests    int64         // 总请求数
	SuccessRequests  int64         // 成功请求数
	ConnectionRate   float64       // 连接率（成功率）
	LastRateUpdate   time.Time     // 最后连接率更新时间
	RateWindowSize   int           // 连接率计算窗口大小（默认1000）
}

// HealthData 健康数据
type HealthData struct {
	URL            string       `json:"url"`
	Status         HealthStatus `json:"status"`
	ErrorCount     int          `json:"errorCount"`
	LastError      *ErrorInfo   `json:"lastError,omitempty"`
	LastCheckTime  time.Time    `json:"lastCheckTime"`
	LastUpdate     time.Time    `json:"lastUpdate"`
	UnhealthySince *time.Time   `json:"unhealthySince,omitempty"`
}

// ErrorInfo 错误信息
type ErrorInfo struct {
	WorkerID  string    `json:"workerId"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}

// ConnectionRateData 连接率数据
type ConnectionRateData struct {
	URL              string    `json:"url"`
	TotalRequests    int64     `json:"totalRequests"`
	SuccessRequests  int64     `json:"successRequests"`
	ConnectionRate   float64   `json:"connectionRate"`
	Confidence       float64   `json:"confidence"`
	Priority         int       `json:"priority"`
	BaseWeight       int       `json:"baseWeight"`
	LastUpdate       time.Time `json:"lastUpdate"`
	// 历史记录（保留最近1000次请求的结果）
	RequestHistory   []bool    `json:"requestHistory"`
	// 统计信息
	DailyStats       map[string]DailyStats `json:"dailyStats"` // 按日期统计
}

// DailyStats 每日统计
type DailyStats struct {
	Date            string  `json:"date"`
	TotalRequests   int64   `json:"totalRequests"`
	SuccessRequests int64   `json:"successRequests"`
	ConnectionRate  float64 `json:"connectionRate"`
}

// HealthManager 健康管理器
type HealthManager struct {
	servers    map[string]*Server
	healthData map[string]*HealthData
	config     *config.Config
	mutex      sync.RWMutex
	healthFile string
	stopChan   chan struct{}
	healthCheckMutex sync.Mutex
	isChecking       bool
	// 添加HTTP客户端池
	httpClient *http.Client
	transport  *http.Transport
	// 连接率数据文件
	connectionRateFile string
	// 连接率数据
	connectionRateData map[string]*ConnectionRateData
}

// NewHealthManager 创建新的健康管理器
func NewHealthManager(cfg *config.Config) *HealthManager {
	// 创建优化的HTTP传输层
	transport := &http.Transport{
		MaxIdleConns:        100,              // 最大空闲连接数
		MaxIdleConnsPerHost: 10,               // 每个主机的最大空闲连接数
		IdleConnTimeout:     90 * time.Second, // 空闲连接超时
		DisableCompression:  true,             // 禁用压缩，健康检查不需要
		ForceAttemptHTTP2:   true,             // 强制尝试HTTP/2
		// 连接池配置
		MaxConnsPerHost:     20,               // 每个主机的最大连接数
		// 超时配置
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,      // 连接超时
			KeepAlive: 30 * time.Second,      // Keep-Alive间隔
		}).DialContext,
	}

	// 创建HTTP客户端
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,          // 整体超时
	}

	hm := &HealthManager{
		servers:    make(map[string]*Server),
		healthData: make(map[string]*HealthData),
		config:     cfg,
		healthFile: "health_data.json",
		stopChan:   make(chan struct{}),
		httpClient: client,
		transport:  transport,
		connectionRateFile: "connection_rate_data.json",
		connectionRateData: make(map[string]*ConnectionRateData),
	}

	// 加载服务器配置
	hm.loadServers()

	// 加载健康数据
	hm.loadHealthData()
	
	// 加载连接率数据
	hm.loadConnectionRateData()

	return hm
}

// initializeServers 初始化服务器
func (hm *HealthManager) initializeServers() {
	upstreamServers := os.Getenv("UPSTREAM_SERVERS")
	logger.Info("读取UPSTREAM_SERVERS环境变量: '%s'", upstreamServers)

	if upstreamServers == "" {
		logger.Error("未配置上游服务器 - UPSTREAM_SERVERS环境变量为空")
		return
	}

	servers := strings.Split(upstreamServers, ",")
	logger.Info("解析到 %d 个服务器URL", len(servers))

	for i, serverURL := range servers {
		serverURL = strings.TrimSpace(serverURL)
		logger.Info("处理服务器 %d: '%s'", i+1, serverURL)

		if serverURL == "" {
			logger.Warn("跳过空的服务器URL")
			continue
		}

		hm.servers[serverURL] = &Server{
			URL:              serverURL,
			Status:           HealthStatusHealthy, // 初始状态假设健康，等待第一次检查确认
			BaseWeight:       75,                    // 默认基础权重（提高，确保可用性）
			DynamicWeight:    50,                    // 默认动态权重
			CombinedWeight:   75,                    // 默认综合权重（与基础权重一致）
			Priority:         2,                     // 默认中优先级（提高，确保可用性）
			ErrorCount:       0,
			LastCheckTime:    time.Time{}, // 初始时间为零值，表示尚未检查
			LastEWMA:         0,
			ResponseTimes:    make([]time.Duration, 0, 3),
			// 连接率相关字段
			TotalRequests:    0,                     // 总请求数
			SuccessRequests:  0,                     // 成功请求数
			ConnectionRate:   0.8,                   // 默认80%成功率（保守但可用）
			LastRateUpdate:   time.Now(),
			RateWindowSize:   1000,                  // 默认1000次请求窗口
		}
		logger.Success("成功初始化服务器: %s (状态=健康, 权重=1)", serverURL)
	}

	logger.Info("服务器初始化完成 - 总共初始化 %d 个上游服务器", len(hm.servers))

	// 输出最终的服务器映射
	if len(hm.servers) > 0 {
		logger.Info("服务器映射详情:")
		for url, server := range hm.servers {
			logger.Info("  - %s: 状态=%s, 权重=%d", url, server.Status, server.CombinedWeight)
		}
	} else {
		logger.Error("警告: 没有成功初始化任何服务器!")
	}
}

// StartHealthCheck 启动健康检查
func (hm *HealthManager) StartHealthCheck() {
	go hm.healthCheckLoop()
	logger.Info("健康检查已启动，将检查以下上游服务器:")
	hm.mutex.RLock()
	for url := range hm.servers {
		logger.Info("  - %s", url)
	}
	hm.mutex.RUnlock()
}

// StopHealthCheck 停止健康检查
func (hm *HealthManager) StopHealthCheck() {
	close(hm.stopChan)
	
	// 关闭HTTP传输层，清理连接池
	if hm.transport != nil {
		hm.transport.CloseIdleConnections()
	}
}

// CloseIdleConnections 关闭空闲连接
func (hm *HealthManager) CloseIdleConnections() {
	if hm.transport != nil {
		hm.transport.CloseIdleConnections()
		logger.Info("已关闭健康检查的空闲连接")
	}
}

// UpdateConnectionRate 更新服务器的连接率（持久化版本）
func (hm *HealthManager) UpdateConnectionRate(server *Server, success bool) {
	// 获取或创建连接率数据
	rateData := hm.getOrCreateConnectionRateData(server.URL)
	
	// 更新请求计数
	rateData.TotalRequests++
	if success {
		rateData.SuccessRequests++
	}
	
	// 更新请求历史记录
	rateData.RequestHistory = append(rateData.RequestHistory, success)
	
	// 更新每日统计
	hm.updateDailyStats(rateData, success)
	
	// 计算连接率
	if rateData.TotalRequests > 0 {
		rateData.ConnectionRate = float64(rateData.SuccessRequests) / float64(rateData.TotalRequests)
	}
	
	// 计算置信度（基于样本数量）
	confidence := hm.calculateConfidence(rateData.TotalRequests)
	rateData.Confidence = confidence
	
	// 基于置信度调整连接率
	adjustedRate := hm.adjustRateByConfidence(rateData.ConnectionRate, confidence)
	
	// 更新优先级（基于调整后的连接率）
	priority := hm.calculatePriority(adjustedRate)
	rateData.Priority = priority
	
	// 更新基础权重（基于调整后的连接率）
	baseWeight := hm.calculateBaseWeightFromRate(adjustedRate)
	rateData.BaseWeight = baseWeight
	
	// 更新最后更新时间
	rateData.LastUpdate = time.Now()
	
	// 同步更新Server结构体（兼容性）
	server.TotalRequests = rateData.TotalRequests
	server.SuccessRequests = rateData.SuccessRequests
	server.ConnectionRate = rateData.ConnectionRate
	server.Priority = priority
	server.BaseWeight = baseWeight
	server.LastRateUpdate = rateData.LastUpdate
	
	// 保存到文件
	go func() {
		if err := hm.saveConnectionRateData(); err != nil {
			logger.Error("保存连接率数据失败: %v", err)
		}
	}()
	
	logger.Debug("服务器 %s 连接率更新: 总请求=%d, 成功=%d, 原始连接率=%.2f%%, 置信度=%.1f%%, 调整后连接率=%.2f%%, 优先级=%d, 基础权重=%d",
		server.URL, rateData.TotalRequests, rateData.SuccessRequests, 
		rateData.ConnectionRate*100, confidence*100, adjustedRate*100, priority, baseWeight)
}

// calculateConfidence 计算置信度（基于样本数量）
func (hm *HealthManager) calculateConfidence(sampleCount int64) float64 {
	// 使用威尔逊得分区间方法计算置信度
	// 样本数量越多，置信度越高
	
	if sampleCount == 0 {
		return 0.0
	}
	
	// 基于样本数量的置信度计算
	switch {
	case sampleCount >= 1000: // 1000+样本，高置信度
		return 1.0
	case sampleCount >= 500: // 500-999样本，较高置信度
		return 0.9
	case sampleCount >= 200: // 200-499样本，中等置信度
		return 0.8
	case sampleCount >= 100: // 100-199样本，较低置信度
		return 0.7
	case sampleCount >= 50:  // 50-99样本，低置信度
		return 0.6
	case sampleCount >= 20:  // 20-49样本，很低置信度
		return 0.5
	case sampleCount >= 10:  // 10-19样本，极低置信度
		return 0.3
	default: // 1-9样本，最低置信度
		return 0.1
	}
}

// adjustRateByConfidence 基于置信度调整连接率
func (hm *HealthManager) adjustRateByConfidence(originalRate, confidence float64) float64 {
	// 样本不足时的处理策略：
	// 1. 样本数量 < 20：使用保守但可用的默认值（80%）
	// 2. 样本数量 20-50：使用中性值（75%）
	// 3. 样本数量 50+：逐渐相信原始连接率
	
	var defaultRate float64
	if confidence < 0.5 { // 样本数量 < 20
		defaultRate = 0.8 // 80% - 保守但可用的默认值
	} else if confidence < 0.7 { // 样本数量 20-50
		defaultRate = 0.75 // 75% - 中性值
	} else {
		defaultRate = 0.7 // 70% - 标准中性值
	}
	
	// 加权平均：置信度 * 原始连接率 + (1-置信度) * 默认值
	adjustedRate := confidence*originalRate + (1-confidence)*defaultRate
	
	// 确保调整后的连接率不会过低，最低不低于60%
	if adjustedRate < 0.6 {
		adjustedRate = 0.6
	}
	
	return adjustedRate
}

// calculatePriority 基于调整后的连接率计算优先级
func (hm *HealthManager) calculatePriority(adjustedRate float64) int {
	// 连接率越高，优先级越高
	// 由于adjustRateByConfidence确保最低不低于60%，所以最低优先级很少出现
	
	if adjustedRate >= 0.95 { // 95%以上成功率
		return 3 // 高优先级
	} else if adjustedRate >= 0.80 { // 80-95%成功率
		return 2 // 中优先级
	} else if adjustedRate >= 0.60 { // 60-80%成功率
		return 1 // 低优先级
	} else { // 60%以下成功率（理论上不会出现，因为adjustRateByConfidence有保护）
		return 1 // 强制设为低优先级，而不是最低优先级
	}
}

// calculateBaseWeightFromRate 基于调整后的连接率计算基础权重
func (hm *HealthManager) calculateBaseWeightFromRate(adjustedRate float64) int {
	// 连接率越高，基础权重越高
	baseWeight := int(adjustedRate * 100)
	
	// 设置最小和最大权重
	// 由于adjustedRate最低不低于60%，所以baseWeight最低不低于60
	if baseWeight < 60 {
		baseWeight = 60 // 提高最小权重，确保新服务器也能参与负载均衡
	} else if baseWeight > 100 {
		baseWeight = 100
	}
	
	return baseWeight
}

// healthCheckLoop 健康检查循环
func (hm *HealthManager) healthCheckLoop() {
	// 使用配置的健康检查间隔
	interval := hm.config.HealthCheckInterval
	if interval == 0 {
		interval = 5 * time.Minute // 默认5分钟
	}
	
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 使用配置的初始延迟
	initialDelay := hm.config.HealthCheckInitialDelay
	if initialDelay == 0 {
		initialDelay = 10 * time.Second // 默认10秒
	}
	
	logger.Info("健康检查配置 - 间隔: %v, 初始延迟: %v", interval, initialDelay)
	
	// 延迟执行第一次检查，给服务器一些启动时间
	time.Sleep(initialDelay)
	hm.performHealthCheck()
	
	// 启动数据清理任务
	go hm.startDataCleanupTask()

	for {
		select {
		case <-ticker.C:
			// 在goroutine中执行健康检查，避免阻塞主线程
			go func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("健康检查发生panic: %v", r)
					}
				}()
				hm.performHealthCheck()
			}()
		case <-hm.stopChan:
			return
		}
	}
}

// performHealthCheck 执行健康检查
func (hm *HealthManager) performHealthCheck() {
	// 检查是否已经在进行健康检查
	hm.healthCheckMutex.Lock()
	if hm.isChecking {
		logger.Info("健康检查正在进行中，跳过本次检查")
		hm.healthCheckMutex.Unlock()
		return
	}
	hm.isChecking = true
	hm.healthCheckMutex.Unlock()

	// 确保函数结束时重置状态
	defer func() {
		hm.healthCheckMutex.Lock()
		hm.isChecking = false
		hm.healthCheckMutex.Unlock()
	}()

	logger.Info("开始执行健康检查...")

	// 获取服务器列表
	hm.mutex.RLock()
	servers := make([]*Server, 0, len(hm.servers))
	for _, server := range hm.servers {
		servers = append(servers, server)
	}
	hm.mutex.RUnlock()

	// 批量健康检查，使用连接池提高效率
	checkResults := hm.batchHealthCheck(servers)
	
	// 处理检查结果
	for i, server := range servers {
		if checkResults[i] == nil {
			continue // 跳过不需要检查的服务器
		}
		
		logger.Info("服务器 %s 检查完成，结果: %t", server.URL, checkResults[i].Success)
		
		// 健康检查失败时的处理
		if !checkResults[i].Success {
			logger.Warn("服务器 %s 健康检查失败", server.URL)
			
			// 更新检查时间和错误计数
			hm.mutex.Lock()
			server.LastCheckTime = time.Now()
			server.ErrorCount++
			
			// 如果连续失败次数过多，标记为不健康
			if server.ErrorCount >= hm.config.MaxErrorsBeforeUnhealthy {
				server.Status = HealthStatusUnhealthy
				logger.Warn("服务器 %s 连续失败 %d 次，标记为不健康", server.URL, server.ErrorCount)
			}
			hm.mutex.Unlock()
			
			// 更新连接率（失败）
			hm.UpdateConnectionRate(server, false)
			continue
		}
		
		// 健康检查成功，重置错误计数并标记为健康
		hm.mutex.Lock()
		server.Status = HealthStatusHealthy
		server.ErrorCount = 0
		hm.mutex.Unlock()
		
		// 更新连接率（成功）
		hm.UpdateConnectionRate(server, true)
		
		hm.updateServerState(server, checkResults[i])
	}

	// 输出所有服务器状态
	logger.Info("健康检查完成，服务器状态:")
	hm.mutex.RLock()
	healthyCount := 0
	var healthyServers []*Server
	for _, server := range hm.servers {
		if server.Status == HealthStatusHealthy {
			healthyCount++
			healthyServers = append(healthyServers, server)
		}
	}
	hm.mutex.RUnlock()

	logger.Info("健康检查完成: %d/%d 个服务器健康", healthyCount, len(hm.servers))
}

// batchHealthCheck 批量健康检查，使用连接池提高效率
func (hm *HealthManager) batchHealthCheck(servers []*Server) []*CheckResult {
	results := make([]*CheckResult, len(servers))
	
	// 批量创建请求
	requests := make([]*http.Request, 0, len(servers))
	serverIndices := make([]int, 0, len(servers))
	
	for i, server := range servers {
		// 如果服务器当前是健康的，跳过健康检查，避免过度检查
		if server.Status == HealthStatusHealthy && time.Since(server.LastCheckTime) < 2*time.Minute {
			logger.Info("跳过服务器 %s 的检查，最近已检查过 (最后检查: %v)", server.URL, server.LastCheckTime)
			continue
		}
		
		// 创建健康检查请求
		req, err := hm.createHealthCheckRequest(server)
		if err != nil {
			logger.Error("为服务器 %s 创建健康检查请求失败: %v", server.URL, err)
			results[i] = &CheckResult{
				Success: false,
				Error:   fmt.Sprintf("创建请求失败: %v", err),
			}
			continue
		}
		
		requests = append(requests, req)
		serverIndices = append(serverIndices, i)
	}
	
	if len(requests) == 0 {
		logger.Info("没有需要检查的服务器")
		return results
	}
	
	logger.Info("开始批量健康检查 %d 个服务器", len(requests))
	
	// 批量发送请求，利用连接池的复用能力
	for i, req := range requests {
		serverIndex := serverIndices[i]
		server := servers[serverIndex]
		
		logger.Info("开始检查服务器: %s", server.URL)
		startTime := time.Now()
		
		// 发送请求
		resp, err := hm.httpClient.Do(req)
		responseTime := time.Since(startTime)
		
		if err != nil {
			logger.Error("健康检查请求失败: %v (耗时: %v)", err, responseTime)
			results[serverIndex] = &CheckResult{
				Success:      false,
				Error:        fmt.Sprintf("请求失败: %v", err),
				ResponseTime: responseTime,
			}
			continue
		}
		
		// 处理响应
		checkResult := hm.processHealthCheckResponse(server, resp, responseTime)
		results[serverIndex] = checkResult
	}
	
	return results
}

// createHealthCheckRequest 创建健康检查请求
func (hm *HealthManager) createHealthCheckRequest(server *Server) (*http.Request, error) {
	var healthCheckURL string
	var method string

	if hm.config.UpstreamType == "tmdb-api" {
		// 为健康检查添加特殊参数，避免与正常请求混淆
		healthCheckURL = fmt.Sprintf("%s/3/configuration?api_key=%s&_health_check=1&_timestamp=%d",
			server.URL, hm.config.TMDBAPIKey, time.Now().Unix())
		method = "GET"
		logger.Info("健康检查 %s: %s %s", server.URL, method, healthCheckURL)
	} else if hm.config.UpstreamType == "tmdb-image" {
		// 图片健康检查：使用与tmdb-api一致的简单请求方式，但验证图片数据
		testURL := hm.config.TMDBImageTestURL
		if testURL == "" {
			testURL = "/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png"
		}
		healthCheckURL = fmt.Sprintf("%s%s", server.URL, testURL)
		method = "GET"
		logger.Info("图片健康检查 %s: %s %s", server.URL, method, healthCheckURL)
	} else {
		return nil, fmt.Errorf("未知的上游类型: %s", hm.config.UpstreamType)
	}

	req, err := http.NewRequest(method, healthCheckURL, nil)
	if err != nil {
		return nil, err
	}

	// 为健康检查请求设置特殊的User-Agent
	req.Header.Set("User-Agent", "tmdb-go-proxy-health-check/1.0")
	
	return req, nil
}

// processHealthCheckResponse 处理健康检查响应
func (hm *HealthManager) processHealthCheckResponse(server *Server, resp *http.Response, responseTime time.Duration) *CheckResult {
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return &CheckResult{
			Success:      false,
			Error:        fmt.Sprintf("HTTP状态码错误: %d", resp.StatusCode),
			ResponseTime: responseTime,
		}
	}

	if hm.config.UpstreamType == "tmdb-image" {
		// 验证Content-Type
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "image/") {
			return &CheckResult{
				Success:      false,
				Error:        fmt.Sprintf("Content-Type不是图片类型: %s", contentType),
				ResponseTime: responseTime,
			}
		}

		// 读取响应体并验证确实是图片数据
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return &CheckResult{
				Success:      false,
				Error:        fmt.Sprintf("读取响应体失败: %v", err),
				ResponseTime: responseTime,
			}
		}

		logger.Info("响应体读取成功，大小: %d字节", len(body))

		// 验证图片数据大小（至少应该有几百字节）
		if len(body) < 100 {
			return &CheckResult{
				Success:      false,
				Error:        fmt.Sprintf("图片数据太小: %d字节", len(body)),
				ResponseTime: responseTime,
			}
		}

		// 验证图片数据格式，确保浏览器能够直接显示
		detectedType := http.DetectContentType(body)
		logger.Info("检测到的内容类型: %s", detectedType)

		if !strings.HasPrefix(detectedType, "image/") {
			return &CheckResult{
				Success:      false,
				Error:        fmt.Sprintf("检测到的内容类型不是图片: %s (声明类型: %s)", detectedType, contentType),
				ResponseTime: responseTime,
			}
		}

		logger.Info("图片健康检查成功 - 大小: %d字节, 检测类型: %s, 声明类型: %s, 耗时: %v",
			len(body), detectedType, contentType, responseTime)
	}

	// 成功
	return &CheckResult{
		Success:      true,
		ResponseTime: responseTime,
	}
}

// checkServerHealth 检查单个服务器健康状态（保留用于兼容性）
func (hm *HealthManager) checkServerHealth(server *Server) *CheckResult {
	startTime := time.Now()

	var err error
	var healthCheckURL string
	var method string

	if hm.config.UpstreamType == "tmdb-api" {
		// 为健康检查添加特殊参数，避免与正常请求混淆
		healthCheckURL = fmt.Sprintf("%s/3/configuration?api_key=%s&_health_check=1&_timestamp=%d",
			server.URL, hm.config.TMDBAPIKey, time.Now().Unix())
		method = "GET"
		logger.Info("健康检查 %s: %s %s", server.URL, method, healthCheckURL)
		_, err = hm.makeRequest(method, healthCheckURL, nil)
	} else if hm.config.UpstreamType == "tmdb-image" {
		// 图片健康检查：使用与tmdb-api一致的简单请求方式，但验证图片数据
		testURL := hm.config.TMDBImageTestURL
		if testURL == "" {
			testURL = "/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png"
		}
		healthCheckURL := fmt.Sprintf("%s%s", server.URL, testURL)
		method = "GET"
		logger.Info("图片健康检查 %s: %s %s", server.URL, method, healthCheckURL)

		// 使用makeRequest进行健康检查，与tmdb-api保持一致
		resp, err := hm.makeRequest(method, healthCheckURL, nil)
		if err != nil {
			return &CheckResult{
				Success: false,
				Error:   fmt.Sprintf("请求失败: %v", err),
			}
		}

		// 验证响应状态码
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return &CheckResult{
				Success: false,
				Error:   fmt.Sprintf("HTTP状态码错误: %d", resp.StatusCode),
			}
		}

		// 验证Content-Type
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "image/") {
			resp.Body.Close()
			return &CheckResult{
				Success: false,
				Error:   fmt.Sprintf("Content-Type不是图片类型: %s", contentType),
			}
		}

		// 读取响应体并验证确实是图片数据
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return &CheckResult{
				Success: false,
				Error:   fmt.Sprintf("读取响应体失败: %v", err),
			}
		}

		logger.Info("响应体读取成功，大小: %d字节", len(body))

		// 验证图片数据大小（至少应该有几百字节）
		if len(body) < 100 {
			return &CheckResult{
				Success: false,
				Error:   fmt.Sprintf("图片数据太小: %d字节", len(body)),
			}
		}

		// 验证图片数据格式，确保浏览器能够直接显示
		detectedType := http.DetectContentType(body)
		logger.Info("检测到的内容类型: %s", detectedType)

		if !strings.HasPrefix(detectedType, "image/") {
			return &CheckResult{
				Success: false,
				Error:   fmt.Sprintf("检测到的内容类型不是图片: %s (声明类型: %s)", detectedType, contentType),
			}
		}

		logger.Info("图片健康检查成功 - 大小: %d字节, 检测类型: %s, 声明类型: %s, 耗时: %v",
			len(body), detectedType, contentType, time.Since(startTime))

		return &CheckResult{
			Success:      true,
			ResponseTime: time.Since(startTime).Milliseconds(),
		}
	} else if hm.config.UpstreamType == "custom" {
		// 自定义类型：对根路径进行简单的HEAD请求检查
		healthCheckURL = fmt.Sprintf("%s/?_health_check=1&_timestamp=%d",
			server.URL, time.Now().Unix())
		method = "HEAD"
		logger.Info("健康检查 %s: %s %s", server.URL, method, healthCheckURL)
		_, err = hm.makeRequest(method, healthCheckURL, nil)
	} else {
		logger.Error("未知的上游类型: %s", hm.config.UpstreamType)
		return &CheckResult{
			Success: false,
			Error:   fmt.Sprintf("未知的上游类型: %s", hm.config.UpstreamType),
		}
	}

	responseTime := time.Since(startTime).Milliseconds()

	if err != nil {
		logger.Error("健康检查失败 - %s: %v", server.URL, err)
		return &CheckResult{
			Success: false,
			Error:   err.Error(),
		}
	}

	return &CheckResult{
		Success:      true,
		ResponseTime: responseTime,
	}
}

// CheckResult 检查结果
type CheckResult struct {
	Success      bool   `json:"success"`
	ResponseTime int64  `json:"responseTime,omitempty"`
	Error        string `json:"error,omitempty"`
}

// makeRequest 发送HTTP请求，使用连接池
func (hm *HealthManager) makeRequest(method, url string, body io.Reader) (*http.Response, error) {
	// 使用预配置的HTTP客户端，复用连接
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		logger.Error("健康检查创建HTTP请求失败: %v", err)
		return nil, err
	}

	// 为健康检查请求设置特殊的User-Agent
	req.Header.Set("User-Agent", "tmdb-go-proxy-health-check/1.0")

	// 添加调试信息
	logger.Info("健康检查发送请求: %s %s", method, url)

	startTime := time.Now()
	
	// 使用预配置的客户端，自动复用连接
	resp, err := hm.httpClient.Do(req)
	requestDuration := time.Since(startTime)

	if err != nil {
		logger.Error("健康检查请求失败: %v (耗时: %v)", err, requestDuration)
		return nil, err
	}

	logger.Info("健康检查请求成功: %s %s (状态码: %d, 耗时: %v)", method, url, resp.StatusCode, requestDuration)
	return resp, nil
}

// updateServerState 更新服务器状态
func (hm *HealthManager) updateServerState(server *Server, checkResult *CheckResult) {
	// 先在锁外计算所有需要的值
	var avgResponseTime float64
	var newEWMA float64
	var newBaseWeight, newCombinedWeight int
	var newStatus HealthStatus
	var newErrorCount int
	var newRecoveryTime time.Time
	var logMessage string

	if checkResult.Success {
		// 计算响应时间相关数据
		newResponseTimes := append(server.ResponseTimes, checkResult.ResponseTime)
		if len(newResponseTimes) > 3 {
			newResponseTimes = newResponseTimes[1:]
		}

		if len(newResponseTimes) == 3 {
			var total int64
			for _, rt := range newResponseTimes {
				total += rt
			}
			avgResponseTime = float64(total) / 3.0
		} else {
			avgResponseTime = float64(checkResult.ResponseTime)
		}

		// 计算EWMA
		if server.LastEWMA == 0 {
			newEWMA = avgResponseTime
		} else {
			const beta = 0.2
			newEWMA = beta*avgResponseTime + (1-beta)*server.LastEWMA
		}

		// 健康检查只更新基础权重，动态权重由实际请求更新
		newBaseWeight = hm.calculateBaseWeight(newEWMA)
		// 使用当前的动态权重重新计算综合权重
		if newBaseWeight == 0 || server.DynamicWeight == 0 {
			newCombinedWeight = 1
		} else {
			const alpha = 0.3 // 降低基础权重比重，提高动态权重比重
			combined := int(alpha*float64(newBaseWeight) + (1-alpha)*float64(server.DynamicWeight))
			if combined < 1 {
				newCombinedWeight = 1
			} else if combined > 100 {
				newCombinedWeight = 100
			} else {
				newCombinedWeight = combined
			}
		}

		newStatus = HealthStatusHealthy
		newErrorCount = 0
		newRecoveryTime = time.Time{}

		logMessage = fmt.Sprintf("健康检查成功 - %s: 响应时间=%dms, 最近3次平均=%.0fms, EWMA=%.0fms, 基础权重=%d, 综合权重=%d",
			server.URL, checkResult.ResponseTime, avgResponseTime, newEWMA,
			newBaseWeight, newCombinedWeight)
	} else {
		// 健康检查失败时，立即标记为不健康，不需要等待错误次数达到阈值
		newStatus = HealthStatusUnhealthy
		newErrorCount = server.ErrorCount + 1
		newRecoveryTime = time.Now().Add(hm.config.UnhealthyTimeout)

		logMessage = fmt.Sprintf("服务器 %s 健康检查失败，立即标记为不健康，错误次数: %d", server.URL, newErrorCount)
	}

	// 现在才获取锁，快速更新服务器状态
	hm.mutex.Lock()

	if checkResult.Success {
		if server.Status == HealthStatusUnhealthy {
			logger.Success("服务器 %s 已恢复健康状态", server.URL)
		}

		server.Status = newStatus
		server.ErrorCount = newErrorCount
		server.RecoveryTime = newRecoveryTime
		server.LastResponseTime = checkResult.ResponseTime
		server.ResponseTimes = append(server.ResponseTimes, checkResult.ResponseTime)
		if len(server.ResponseTimes) > 3 {
			server.ResponseTimes = server.ResponseTimes[1:]
		}
		server.LastEWMA = newEWMA
		server.BaseWeight = newBaseWeight
		server.CombinedWeight = newCombinedWeight
	} else {
		server.Status = newStatus
		server.ErrorCount = newErrorCount
		server.RecoveryTime = newRecoveryTime
	}

	server.LastCheckTime = time.Now()

	hm.mutex.Unlock()

	// 输出日志（在锁外）
	if checkResult.Success {
		logger.Success(logMessage)
	} else if newErrorCount >= hm.config.MaxErrorsBeforeUnhealthy {
		logger.Error(logMessage)
	} else {
		logger.Warn(logMessage)
	}

	// 更新健康数据（在锁外）
	hm.updateHealthData(server)
}

// calculateBaseWeight 计算基础权重（与JS版本保持一致）
func (hm *HealthManager) calculateBaseWeight(responseTime float64) int {
	if responseTime <= 0 {
		return 1
	}

	multiplier := float64(hm.config.BaseWeightMultiplier)
	// 基础权重计算：响应时间越短，权重越大
	weight := int(multiplier * (1000 / max(responseTime, 1)))

	// 确保权重在合理范围内 (1-100)
	if weight < 1 {
		weight = 1
	}
	if weight > 100 {
		weight = 100
	}
	return weight
}

// calculateDynamicWeight 计算动态权重（与JS版本保持一致）
func (hm *HealthManager) calculateDynamicWeight(avgResponseTime float64) int {
	if avgResponseTime <= 0 {
		return 1
	}

	multiplier := float64(hm.config.DynamicWeightMultiplier)
	// 动态权重计算：响应时间越短，权重越大
	weight := int(multiplier * (1000 / max(avgResponseTime, 1)))

	// 确保权重在合理范围内 (1-100)
	if weight < 1 {
		weight = 1
	}
	if weight > 100 {
		weight = 100
	}
	return weight
}

// calculateCombinedWeight 计算综合权重（新优先级机制）
func (hm *HealthManager) calculateCombinedWeight(server *Server) int {
	// 新的权重计算逻辑：
	// 1. 优先级决定基础权重范围
	// 2. 在相同优先级下，动态权重决定最终权重
	
	// 基于优先级的基础权重
	priorityWeight := server.Priority * 100 // 优先级越高，基础权重越高
	
	// 在相同优先级下，动态权重调整
	// 动态权重越高，在相同优先级中的排序越靠前
	adjustedWeight := priorityWeight + server.DynamicWeight
	
	// 确保权重在合理范围内
	if adjustedWeight < 1 {
		adjustedWeight = 1
	} else if adjustedWeight > 500 { // 允许更高的权重范围
		adjustedWeight = 500
	}
	
	return adjustedWeight
}

// max 辅助函数
func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// UpdateDynamicWeight 更新服务器的动态权重（基于实际请求）
func (hm *HealthManager) UpdateDynamicWeight(serverURL string, responseTime int64) {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()

	server, exists := hm.servers[serverURL]
	if !exists {
		return
	}

	// 更新响应时间记录
	server.ResponseTimes = append(server.ResponseTimes, time.Duration(responseTime))
	if len(server.ResponseTimes) > 3 {
		server.ResponseTimes = server.ResponseTimes[1:]
	}

	// 更新动态EWMA
	if server.LastEWMA == 0 {
		server.LastEWMA = float64(responseTime)
	} else {
		const beta = 0.2 // 与健康检查使用相同的衰减因子
		server.LastEWMA = beta*float64(responseTime) + (1-beta)*server.LastEWMA
	}

	// 计算动态权重（基于响应时间）
	server.DynamicWeight = hm.calculateDynamicWeight(server.LastEWMA)

	// 重新计算综合权重（使用新的优先级机制）
	server.CombinedWeight = hm.calculateCombinedWeight(server)

	logger.Info("动态权重更新 - %s: 响应时间=%dms, 动态EWMA=%.0fms, 动态权重=%d, 综合权重=%d",
		serverURL, responseTime, server.LastEWMA, server.DynamicWeight, server.CombinedWeight)
}

// updateHealthData 更新健康数据
func (hm *HealthManager) updateHealthData(server *Server) {
	healthData := &HealthData{
		URL:           server.URL,
		Status:        server.Status,
		ErrorCount:    server.ErrorCount,
		LastCheckTime: server.LastCheckTime,
		LastUpdate:    time.Now(),
	}

	if server.Status == HealthStatusUnhealthy {
		now := time.Now()
		healthData.UnhealthySince = &now
	}

	// 使用独立的锁保护healthData访问
	hm.mutex.Lock()
	hm.healthData[server.URL] = healthData
	hm.mutex.Unlock()

	// 保存健康数据（在锁外）
	hm.saveHealthData()
}

// ReportUnhealthyServer 报告不健康服务器
func (hm *HealthManager) ReportUnhealthyServer(url, workerID, errorInfo string) error {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()

	healthData := hm.healthData[url]
	if healthData == nil {
		healthData = &HealthData{
			URL:           url,
			Status:        HealthStatusHealthy,
			ErrorCount:    0,
			LastCheckTime: time.Now(),
			LastUpdate:    time.Now(),
		}
	}

	healthData.ErrorCount++
	healthData.LastError = &ErrorInfo{
		WorkerID:  workerID,
		Error:     errorInfo,
		Timestamp: time.Now(),
	}
	healthData.LastUpdate = time.Now()

	// 如果错误次数超过阈值，标记为不健康
	if healthData.ErrorCount >= hm.config.MaxErrorsBeforeUnhealthy {
		healthData.Status = HealthStatusUnhealthy
		now := time.Now()
		healthData.UnhealthySince = &now
	}

	hm.healthData[url] = healthData

	// 保存健康数据
	hm.saveHealthData()

	logger.Info("Worker %s 报告服务器 %s 不健康 (错误次数: %d)", workerID, url, healthData.ErrorCount)

	return nil
}

// GetHealthyServers 获取健康服务器
func (hm *HealthManager) GetHealthyServers() []*Server {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()

	var healthyServers []*Server
	for _, server := range hm.servers {
		if server.Status == HealthStatusHealthy {
			healthyServers = append(healthyServers, server)
		}
	}

	logger.Info("GetHealthyServers 结果: 从服务器状态直接获取到 %d 个健康服务器", len(healthyServers))
	return healthyServers
}

// updateHealthyServersSnapshot 更新健康服务器快照
func (hm *HealthManager) updateHealthyServersSnapshot() {
	// 快照机制已移除，此函数不再需要
	logger.Info("快照机制已移除，updateHealthyServersSnapshot不再执行")
}


// GetAllServers 获取所有服务器
func (hm *HealthManager) GetAllServers() []*Server {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()

	servers := make([]*Server, 0, len(hm.servers))
	for _, server := range hm.servers {
		servers = append(servers, server)
	}

	return servers
}

// loadHealthData 加载健康数据
func (hm *HealthManager) loadHealthData() error {
	data, err := os.ReadFile(hm.healthFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var healthData map[string]*HealthData
	if err := json.Unmarshal(data, &healthData); err != nil {
		return err
	}

	hm.mutex.Lock()
	hm.healthData = healthData
	hm.mutex.Unlock()

	logger.Info("已加载 %d 个服务器的健康数据", len(healthData))
	return nil
}

// saveHealthData 保存健康数据
func (hm *HealthManager) saveHealthData() error {
	hm.mutex.RLock()
	data := make(map[string]*HealthData)
	for k, v := range hm.healthData {
		data[k] = v
	}
	hm.mutex.RUnlock()

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(hm.healthFile, jsonData, 0644)
}

// GetServerConfidence 获取服务器置信度（用于监控）
func (hm *HealthManager) GetServerConfidence(serverURL string) float64 {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()
	
	// 优先使用持久化的连接率数据
	if data, exists := hm.connectionRateData[serverURL]; exists {
		return data.Confidence
	}
	
	// 兼容性：使用服务器数据
	server, exists := hm.servers[serverURL]
	if !exists {
		return 0.0
	}
	
	return hm.calculateConfidence(server.TotalRequests)
}

// loadConnectionRateData 加载连接率数据
func (hm *HealthManager) loadConnectionRateData() {
	filePath := filepath.Join(hm.config.Cache.CacheDir, hm.connectionRateFile)
	
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("连接率数据文件不存在，将创建新文件: %s", filePath)
			return
		}
		logger.Error("读取连接率数据文件失败: %v", err)
		return
	}
	
	var rateData map[string]*ConnectionRateData
	if err := json.Unmarshal(data, &rateData); err != nil {
		logger.Error("解析连接率数据失败: %v", err)
		return
	}
	
	hm.connectionRateData = rateData
	logger.Info("成功加载连接率数据，共 %d 个服务器", len(rateData))
}

// saveConnectionRateData 保存连接率数据
func (hm *HealthManager) saveConnectionRateData() error {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()
	
	// 确保缓存目录存在
	if err := os.MkdirAll(hm.config.Cache.CacheDir, 0755); err != nil {
		return fmt.Errorf("创建缓存目录失败: %v", err)
	}
	
	filePath := filepath.Join(hm.config.Cache.CacheDir, hm.connectionRateFile)
	
	// 序列化数据
	jsonData, err := json.MarshalIndent(hm.connectionRateData, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化连接率数据失败: %v", err)
	}
	
	// 写入文件
	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return fmt.Errorf("写入连接率数据文件失败: %v", err)
	}
	
	logger.Debug("连接率数据已保存到: %s", filePath)
	return nil
}

// getOrCreateConnectionRateData 获取或创建连接率数据
func (hm *HealthManager) getOrCreateConnectionRateData(serverURL string) *ConnectionRateData {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()
	
	if data, exists := hm.connectionRateData[serverURL]; exists {
		return data
	}
	
	// 创建新的连接率数据
	data := &ConnectionRateData{
		URL:              serverURL,
		TotalRequests:    0,
		SuccessRequests:  0,
		ConnectionRate:   0.8, // 默认80%成功率（保守但可用）
		Confidence:       0.0,
		Priority:         2,   // 默认中优先级（提高，确保可用性）
		BaseWeight:       75,  // 默认基础权重（提高，确保可用性）
		LastUpdate:       time.Now(),
		RequestHistory:   make([]bool, 0, 1000),
		DailyStats:       make(map[string]DailyStats),
	}
	
	hm.connectionRateData[serverURL] = data
	return data
}

// updateDailyStats 更新每日统计
func (hm *HealthManager) updateDailyStats(data *ConnectionRateData, success bool) {
	today := time.Now().Format("2006-01-02")
	
	if stats, exists := data.DailyStats[today]; exists {
		stats.TotalRequests++
		if success {
			stats.SuccessRequests++
		}
		stats.ConnectionRate = float64(stats.SuccessRequests) / float64(stats.TotalRequests)
		data.DailyStats[today] = stats
	} else {
		// 创建新的每日统计
		successCount := int64(0)
		if success {
			successCount = 1
		}
		data.DailyStats[today] = DailyStats{
			Date:            today,
			TotalRequests:   1,
			SuccessRequests: successCount,
			ConnectionRate:  float64(successCount),
		}
	}
}

// startDataCleanupTask 启动数据清理任务
func (hm *HealthManager) startDataCleanupTask() {
	// 每24小时清理一次旧数据
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	
	// 立即执行一次清理
	hm.cleanupOldData()
	
	for {
		select {
		case <-ticker.C:
			hm.cleanupOldData()
		case <-hm.stopChan:
			return
		}
	}
}

// cleanupOldData 清理旧数据
func (hm *HealthManager) cleanupOldData() {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()
	
	// 清理30天前的每日统计
	cutoffDate := time.Now().AddDate(0, 0, -30)
	
	for _, data := range hm.connectionRateData {
		for dateStr := range data.DailyStats {
			if date, err := time.Parse("2006-01-02", dateStr); err == nil {
				if date.Before(cutoffDate) {
					delete(data.DailyStats, dateStr)
				}
			}
		}
		
		// 限制请求历史记录数量
		if len(data.RequestHistory) > 1000 {
			data.RequestHistory = data.RequestHistory[len(data.RequestHistory)-1000:]
		}
	}
	
	logger.Info("已清理30天前的连接率统计数据")
}

// GetServerByURL 根据URL获取服务器对象
func (hm *HealthManager) GetServerByURL(serverURL string) *Server {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()
	
	server, exists := hm.servers[serverURL]
	if !exists {
		return nil
	}
	
	return server
}

// IsServerReady 判断服务器是否已经准备好（样本数量足够）
func (hm *HealthManager) IsServerReady(serverURL string) bool {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()
	
	// 检查连接率数据
	if data, exists := hm.connectionRateData[serverURL]; exists {
		return data.TotalRequests >= 100
	}
	
	// 检查服务器数据（兼容性）
	server, exists := hm.servers[serverURL]
	if !exists {
		return false
	}
	
	return server.TotalRequests >= 100
}
