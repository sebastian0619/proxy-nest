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
	URL              string      `json:"url"`
	Status           HealthStatus `json:"status"`
	ErrorCount       int         `json:"errorCount"`
	RecoveryTime     time.Time   `json:"recoveryTime"`
	LastCheckTime    time.Time   `json:"lastCheckTime"`
	ResponseTimes    []int64     `json:"responseTimes"`
	LastResponseTime int64       `json:"lastResponseTime"`
	BaseWeight       int         `json:"baseWeight"`
	DynamicWeight    int         `json:"dynamicWeight"`
	CombinedWeight   int         `json:"combinedWeight"`
	LastEWMA         float64     `json:"lastEWMA"`
}

// HealthData 健康数据
type HealthData struct {
	URL              string      `json:"url"`
	Status           HealthStatus `json:"status"`
	ErrorCount       int         `json:"errorCount"`
	LastError        *ErrorInfo  `json:"lastError,omitempty"`
	LastCheckTime    time.Time   `json:"lastCheckTime"`
	LastUpdate       time.Time   `json:"lastUpdate"`
	UnhealthySince   *time.Time  `json:"unhealthySince,omitempty"`
}

// ErrorInfo 错误信息
type ErrorInfo struct {
	WorkerID  string    `json:"workerId"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}

// HealthManager 健康管理器
type HealthManager struct {
	servers     map[string]*Server
	healthData  map[string]*HealthData
	config      *config.Config
	mutex       sync.RWMutex
	healthFile  string
	stopChan    chan struct{}
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
	if upstreamServers == "" {
		logger.Error("未配置上游服务器")
		return
	}
	
	servers := strings.Split(upstreamServers, ",")
	for _, serverURL := range servers {
		serverURL = strings.TrimSpace(serverURL)
		if serverURL == "" {
			continue
		}
		
		hm.servers[serverURL] = &Server{
			URL:              serverURL,
			Status:           HealthStatusHealthy,
			ErrorCount:       0,
			RecoveryTime:     time.Time{},
			LastCheckTime:    time.Now(),
			ResponseTimes:    make([]int64, 0),
			LastResponseTime: 0,
			BaseWeight:       1,
			DynamicWeight:    1,
			CombinedWeight:   1,
			LastEWMA:         0,
		}
	}
	
	logger.Info("已初始化 %d 个上游服务器", len(hm.servers))
}

// StartHealthCheck 启动健康检查
func (hm *HealthManager) StartHealthCheck() {
	go hm.healthCheckLoop()
	logger.Info("健康检查已启动")
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
				return
			}
			
			checkResult := hm.checkServerHealth(s)
			hm.updateServerState(s, checkResult)
		}(server)
	}
	
	wg.Wait()
	logger.Info("健康检查完成")
}

// checkServerHealth 检查单个服务器健康状态
func (hm *HealthManager) checkServerHealth(server *Server) *CheckResult {
	startTime := time.Now()
	
	var err error
	if hm.config.UpstreamType == "tmdb-api" {
		url := fmt.Sprintf("%s/3/configuration?api_key=%s", server.URL, hm.config.TMDBAPIKey)
		_, err = hm.makeRequest("GET", url, nil)
	} else if hm.config.UpstreamType == "tmdb-image" {
		testURL := hm.config.TMDBImageTestURL
		if testURL == "" {
			testURL = "/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png"
		}
		url := fmt.Sprintf("%s%s", server.URL, testURL)
		_, err = hm.makeRequest("HEAD", url, nil)
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
	hm.mutex.Lock()
	defer hm.mutex.Unlock()
	
	if checkResult.Success {
		if server.Status == HealthStatusUnhealthy {
			logger.Success("服务器 %s 已恢复健康状态", server.URL)
		}
		
		server.Status = HealthStatusHealthy
		server.ErrorCount = 0
		server.RecoveryTime = time.Time{}
		server.LastResponseTime = checkResult.ResponseTime
		
		// 更新响应时间历史
		server.ResponseTimes = append(server.ResponseTimes, checkResult.ResponseTime)
		if len(server.ResponseTimes) > 10 {
			server.ResponseTimes = server.ResponseTimes[1:]
		}
		
		// 计算平均响应时间
		var total int64
		for _, rt := range server.ResponseTimes {
			total += rt
		}
		avgResponseTime := float64(total) / float64(len(server.ResponseTimes))
		
		// 更新权重
		server.LastEWMA = avgResponseTime
		server.BaseWeight = hm.calculateBaseWeight(avgResponseTime)
		server.DynamicWeight = hm.calculateDynamicWeight(avgResponseTime)
		server.CombinedWeight = hm.calculateCombinedWeight(server)
		
	} else {
		server.ErrorCount++
		if server.ErrorCount >= hm.config.MaxErrorsBeforeUnhealthy {
			server.Status = HealthStatusUnhealthy
			server.RecoveryTime = time.Now().Add(hm.config.UnhealthyTimeout)
			logger.Error("服务器 %s 标记为不健康", server.URL)
		}
	}
	
	server.LastCheckTime = time.Now()
	
	// 更新健康数据
	hm.updateHealthData(server)
}

// calculateBaseWeight 计算基础权重
func (hm *HealthManager) calculateBaseWeight(responseTime float64) int {
	multiplier := float64(hm.config.BaseWeightMultiplier)
	return int(multiplier / (responseTime + 1))
}

// calculateDynamicWeight 计算动态权重
func (hm *HealthManager) calculateDynamicWeight(responseTime float64) int {
	multiplier := float64(hm.config.DynamicWeightMultiplier)
	return int(multiplier / (responseTime + 1))
}

// calculateCombinedWeight 计算综合权重
func (hm *HealthManager) calculateCombinedWeight(server *Server) int {
	alpha := hm.config.AlphaInitial
	combined := int(float64(server.BaseWeight)*(1-alpha) + float64(server.DynamicWeight)*alpha)
	if combined < 1 {
		combined = 1
	}
	return combined
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
	
	hm.healthData[server.URL] = healthData
	
	// 保存健康数据
	hm.saveHealthData()
}

// ReportUnhealthyServer 报告不健康服务器
func (hm *HealthManager) ReportUnhealthyServer(url, workerID, errorInfo string) error {
	hm.mutex.Lock()
	defer hm.mutex.Unlock()
	
	healthData := hm.healthData[url]
	if healthData == nil {
		healthData = &HealthData{
			URL:        url,
			Status:     HealthStatusHealthy,
			ErrorCount: 0,
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
	for _, server := range hm.servers {
		if server.Status == HealthStatusHealthy {
			healthyServers = append(healthyServers, server)
		}
	}
	
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