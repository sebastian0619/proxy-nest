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

	"main/config"
	"main/logger"
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
	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	// 立即执行第一次检查
	hm.performHealthCheck()

	for {
		select {
		case <-ticker.C:
			hm.performHealthCheck()
		case <-hm.stopChan:
			return
		}
	}
}

// performHealthCheck 执行健康检查
func (hm *HealthManager) performHealthCheck() {
	logger.Info("开始执行健康检查...")

	var wg sync.WaitGroup
	hm.mutex.RLock()
	servers := make([]*Server, 0, len(hm.servers))
	for _, server := range hm.servers {
		servers = append(servers, server)
	}
	hm.mutex.RUnlock()

	for _, server := range servers {
		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()

			// 检查是否需要进行恢复检查
			if s.Status == HealthStatusUnhealthy && time.Now().Before(s.RecoveryTime) {
				logger.Info("跳过服务器 %s 的检查，仍在恢复期 (恢复时间: %v)", s.URL, s.RecoveryTime)
				return
			}

			logger.Info("开始检查服务器: %s", s.URL)
			checkResult := hm.checkServerHealth(s)
			logger.Info("服务器 %s 检查完成，结果: %t", s.URL, checkResult.Success)
			hm.updateServerState(s, checkResult)
		}(server)
	}

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

	// 计算总权重和选择概率
	var totalWeight int
	for _, server := range healthyServers {
		totalWeight += server.CombinedWeight
	}

	// 输出健康服务器状态和选择概率
	for _, server := range healthyServers {
		selectionProbability := 0.0
		if totalWeight > 0 {
			selectionProbability = float64(server.CombinedWeight) / float64(totalWeight) * 100
		}
		logger.Success("  ✓ %s: 健康 (基础权重=%d, 动态权重=%d, 综合权重=%d, EWMA=%.0fms, 选择概率=%.1f%%)",
			server.URL, server.BaseWeight, server.DynamicWeight, server.CombinedWeight, server.LastEWMA, selectionProbability)
	}

	// 输出不健康服务器
	for _, server := range hm.servers {
		if server.Status == HealthStatusUnhealthy {
			logger.Error("  ✗ %s: 不健康 (错误次数=%d, 恢复时间=%v)",
				server.URL, server.ErrorCount, server.RecoveryTime)
		}
	}
	hm.mutex.RUnlock()
	logger.Info("健康检查统计: %d/%d 服务器健康", healthyCount, len(hm.servers))
}

// checkServerHealth 检查单个服务器健康状态
func (hm *HealthManager) checkServerHealth(server *Server) *CheckResult {
	startTime := time.Now()

	var err error
	var healthCheckURL string
	var method string

	if hm.config.UpstreamType == "tmdb-api" {
		healthCheckURL = fmt.Sprintf("%s/3/configuration?api_key=%s", server.URL, hm.config.TMDBAPIKey)
		method = "GET"
		logger.Info("健康检查 %s: %s %s", server.URL, method, healthCheckURL)
		_, err = hm.makeRequest(method, healthCheckURL, nil)
	} else if hm.config.UpstreamType == "tmdb-image" {
		testURL := hm.config.TMDBImageTestURL
		if testURL == "" {
			testURL = "/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png"
		}
		healthCheckURL = fmt.Sprintf("%s%s", server.URL, testURL)
		method = "HEAD"
		logger.Info("健康检查 %s: %s %s", server.URL, method, healthCheckURL)
		_, err = hm.makeRequest(method, healthCheckURL, nil)
	} else if hm.config.UpstreamType == "custom" {
		// 自定义类型：对根路径进行简单的HEAD请求检查
		healthCheckURL = fmt.Sprintf("%s/", server.URL)
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
	ctx, cancel := context.WithTimeout(context.Background(), hm.config.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	client := &http.Client{}
	return client.Do(req)
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

	// 每3次请求更新一次动态权重
	if server.RequestCount%3 == 0 {
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

// GetHealthyServers 获取健康服务器列表
func (hm *HealthManager) GetHealthyServers() []*Server {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()

	var healthyServers []*Server
	logger.Info("GetHealthyServers 调用 - 开始检查服务器状态:")
	for url, server := range hm.servers {
		logger.Info("  服务器 %s: 状态=%s, 错误次数=%d, 最后检查=%v",
			url, server.Status, server.ErrorCount, server.LastCheckTime)
		if server.Status == HealthStatusHealthy {
			healthyServers = append(healthyServers, server)
			logger.Info("    ✓ 添加到健康服务器列表")
		} else {
			logger.Info("    ✗ 服务器不健康，跳过")
		}
	}

	logger.Info("GetHealthyServers 结果: 找到 %d 个健康服务器", len(healthyServers))
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
