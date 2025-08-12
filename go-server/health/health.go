package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	URL              string       `json:"url"`
	Status           HealthStatus `json:"status"`
	ErrorCount       int          `json:"errorCount"`
	RecoveryTime     time.Time    `json:"recoveryTime"`
	LastCheckTime    time.Time    `json:"lastCheckTime"`
	ResponseTimes    []int64      `json:"responseTimes"`
	LastResponseTime int64        `json:"lastResponseTime"`
	BaseWeight       int          `json:"baseWeight"`
	DynamicWeight    int          `json:"dynamicWeight"`
	CombinedWeight   int          `json:"combinedWeight"`
	LastEWMA         float64      `json:"lastEWMA"`
	RequestCount     int          `json:"requestCount"` // 请求计数器
	DynamicEWMA      float64      `json:"dynamicEWMA"`  // 动态EWMA（基于实际请求）
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

// HealthManager 健康管理器
type HealthManager struct {
	servers    map[string]*Server
	healthData map[string]*HealthData
	config     *config.Config
	mutex      sync.RWMutex
	healthFile string
	stopChan   chan struct{}

	// 添加健康检查锁，确保同一时间只进行一个健康检查
	healthCheckMutex sync.Mutex
	isChecking       bool
}

// NewHealthManager 创建健康管理器
func NewHealthManager(cfg *config.Config) *HealthManager {
	healthFile := filepath.Join(cfg.Cache.CacheDir, "health_data.json")

	hm := &HealthManager{
		servers:    make(map[string]*Server),
		healthData: make(map[string]*HealthData),
		config:     cfg,
		healthFile: healthFile,
		stopChan:   make(chan struct{}),
	}

	// 初始化服务器
	hm.initializeServers()

	// 加载健康数据
	hm.loadHealthData()



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
			ErrorCount:       0,
			RecoveryTime:     time.Time{},
			LastCheckTime:    time.Time{}, // 初始时间为零值，表示尚未检查
			ResponseTimes:    make([]int64, 0),
			LastResponseTime: 0,
			BaseWeight:       1,
			DynamicWeight:    1,
			CombinedWeight:   1,
			LastEWMA:         0,
			RequestCount:     0,
			DynamicEWMA:      0,
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
}

// healthCheckLoop 健康检查循环
func (hm *HealthManager) healthCheckLoop() {
	ticker := time.NewTicker(2 * time.Minute) // 每2分钟检查一次，减少频率
	defer ticker.Stop()

	// 立即执行第一次检查
	hm.performHealthCheck()

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

	// 使用并发检查，但简化超时控制，与proxy_app.go保持一致
	hm.mutex.RLock()
	servers := make([]*Server, 0, len(hm.servers))
	for _, server := range hm.servers {
		servers = append(servers, server)
	}
	hm.mutex.RUnlock()

	// 并发检查所有服务器
	var wg sync.WaitGroup
	for _, server := range servers {
		// 检查是否需要进行恢复检查
		if server.Status == HealthStatusUnhealthy && time.Now().Before(server.RecoveryTime) {
			logger.Info("跳过服务器 %s 的检查，仍在恢复期 (恢复时间: %v)", server.URL, server.RecoveryTime)
			continue
		}

		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
			logger.Info("开始检查服务器: %s", s.URL)
			checkResult := hm.checkServerHealth(s)
			logger.Info("服务器 %s 检查完成，结果: %t", s.URL, checkResult.Success)
			hm.updateServerState(s, checkResult)
		}(server)
	}

	// 等待所有检查完成
	wg.Wait()

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

// checkServerHealth 检查单个服务器健康状态
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

// makeRequest 发送HTTP请求
func (hm *HealthManager) makeRequest(method, url string, body io.Reader) (*http.Response, error) {
	// 使用配置的超时时间，与proxy_app.go保持一致
	timeout := hm.config.RequestTimeout

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		logger.Error("健康检查创建HTTP请求失败: %v", err)
		return nil, err
	}

	// 为健康检查请求设置特殊的User-Agent
	req.Header.Set("User-Agent", "tmdb-go-proxy-health-check/1.0")

	// 添加调试信息
	logger.Info("健康检查发送请求: %s %s (超时: %v)", method, url, timeout)
	
	startTime := time.Now()
	client := &http.Client{
		Timeout: timeout,
	}
	
	resp, err := client.Do(req)
	requestDuration := time.Since(startTime)
	
	if err != nil {
		logger.Error("健康检查请求失败: %v (耗时: %v)", err, requestDuration)
		
		// 检查是否是超时错误
		if ctx.Err() == context.DeadlineExceeded {
			logger.Error("健康检查请求超时: %s (超时设置: %v)", url, timeout)
		} else if ctx.Err() == context.Canceled {
			logger.Error("健康检查请求被取消: %s", url)
		}
		
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
		newStatus = server.Status
		newErrorCount = server.ErrorCount + 1
		newRecoveryTime = server.RecoveryTime

		if newErrorCount >= hm.config.MaxErrorsBeforeUnhealthy {
			newStatus = HealthStatusUnhealthy
			newRecoveryTime = time.Now().Add(hm.config.UnhealthyTimeout)
			logMessage = fmt.Sprintf("服务器 %s 标记为不健康 (错误次数达到阈值: %d)", server.URL, hm.config.MaxErrorsBeforeUnhealthy)
		} else {
			logMessage = fmt.Sprintf("服务器 %s 健康检查失败，错误次数: %d/%d", server.URL, newErrorCount, hm.config.MaxErrorsBeforeUnhealthy)
		}
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

// calculateCombinedWeight 计算综合权重
func (hm *HealthManager) calculateCombinedWeight(server *Server) int {
	if server.BaseWeight == 0 || server.DynamicWeight == 0 {
		return 1
	}

	// 使用加权平均计算综合权重，alpha为基础权重的比重
	const alpha = 0.3 // 降低基础权重比重，提高动态权重比重
	combined := int(alpha*float64(server.BaseWeight) + (1-alpha)*float64(server.DynamicWeight))

	// 确保权重在合理范围内 (1-100)
	if combined < 1 {
		combined = 1
	}
	if combined > 100 {
		combined = 100
	}
	return combined
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

	// 增加请求计数
	server.RequestCount++

	// 更新动态EWMA
	if server.DynamicEWMA == 0 {
		server.DynamicEWMA = float64(responseTime)
	} else {
		const beta = 0.2 // 与健康检查使用相同的衰减因子
		server.DynamicEWMA = beta*float64(responseTime) + (1-beta)*server.DynamicEWMA
	}

	// 每次请求都更新动态权重
	server.DynamicWeight = hm.calculateDynamicWeight(server.DynamicEWMA)

	// 重新计算综合权重
	if server.BaseWeight == 0 || server.DynamicWeight == 0 {
		server.CombinedWeight = 1
	} else {
		const alpha = 0.3 // 降低基础权重比重，提高动态权重比重
		combined := int(alpha*float64(server.BaseWeight) + (1-alpha)*float64(server.DynamicWeight))
		if combined < 1 {
			server.CombinedWeight = 1
		} else if combined > 100 {
			server.CombinedWeight = 100
		} else {
			server.CombinedWeight = combined
		}
	}

	logger.Info("动态权重更新 - %s: 请求计数=%d, 动态EWMA=%.0fms, 动态权重=%d, 综合权重=%d",
		serverURL, server.RequestCount, server.DynamicEWMA, server.DynamicWeight, server.CombinedWeight)
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
	// 使用快照机制，避免健康检查期间影响请求处理
	hm.snapshotMutex.RLock()
	defer hm.snapshotMutex.RUnlock()

	// 如果快照为空或太旧，立即同步更新快照，确保请求处理能够获得健康服务器
	if len(hm.healthyServersSnapshot) == 0 || time.Since(hm.lastSnapshotTime) > 5*time.Second {
		if len(hm.healthyServersSnapshot) == 0 {
			logger.Info("健康服务器快照为空，立即同步更新快照")
		} else {
			logger.Info("健康服务器快照过期，立即同步更新快照")
		}
		// 立即同步更新快照，确保请求处理能够获得健康服务器
		hm.updateHealthyServersSnapshot()
	}

	// 如果快照仍然为空，尝试从服务器状态直接获取
	if len(hm.healthyServersSnapshot) == 0 {
		logger.Warn("健康服务器快照仍然为空，尝试从服务器状态直接获取")
		hm.mutex.RLock()
		var healthyServers []*Server
		for _, server := range hm.servers {
			if server.Status == HealthStatusHealthy {
				healthyServers = append(healthyServers, server)
			}
		}
		hm.mutex.RUnlock()
		
		if len(healthyServers) > 0 {
			logger.Info("从服务器状态直接获取到 %d 个健康服务器", len(healthyServers))
			return healthyServers
		}
	}

	// 返回快照的副本，避免外部修改
	healthyServers := make([]*Server, len(hm.healthyServersSnapshot))
	copy(healthyServers, hm.healthyServersSnapshot)

	logger.Info("GetHealthyServers 结果: 从快照返回 %d 个健康服务器", len(healthyServers))
	return healthyServers
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
