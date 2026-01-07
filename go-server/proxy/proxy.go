package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"main/cache"
	"main/config"
	"main/health"
	"main/logger"
)

// ProxyResponse 代理响应
type ProxyResponse struct {
	Data         interface{} `json:"data"`
	ContentType  string      `json:"contentType"`
	ResponseTime int64       `json:"responseTime"`
	IsImage      bool        `json:"isImage"`
}

// ProxyManager 代理管理器
type ProxyManager struct {
	config        *config.Config
	cacheManager  *cache.CacheManager
	healthManager *health.HealthManager
	client        *http.Client
	mutex         sync.RWMutex
	// 上游服务器嵌套代理检测
	upstreamProxies map[string]*UpstreamProxyInfo
}

// UpstreamProxyInfo 上游代理信息
type UpstreamProxyInfo struct {
	URL           string    // 上游服务器URL
	IsTMDBProxy   bool      // 是否为TMDB代理
	Version       string    // 代理版本
	LastChecked   time.Time // 最后检查时间
	CheckCount    int       // 检查次数
}

// NewProxyManager 创建代理管理器
func NewProxyManager(cfg *config.Config, cacheManager *cache.CacheManager, healthManager *health.HealthManager) *ProxyManager {
	// 配置HTTP客户端，确保能够处理大文件
	transport := &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		// 添加更多配置以确保稳定性
		DisableCompression: true,  // 禁用压缩，避免处理问题
		ForceAttemptHTTP2:  false, // 强制使用HTTP/1.1，避免HTTP/2的复杂性
		// 添加连接超时设置
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second, // 连接超时
			KeepAlive: 30 * time.Second, // Keep-Alive时间
		}).DialContext,
		// 添加TLS握手超时
		TLSHandshakeTimeout: 10 * time.Second,
		// 添加响应头超时
		ResponseHeaderTimeout: 30 * time.Second,
		// 添加期望继续超时
		ExpectContinueTimeout: 1 * time.Second,
		// 为图片请求优化
		MaxResponseHeaderBytes: 1024 * 1024, // 1MB响应头限制
		WriteBufferSize:        64 * 1024,   // 64KB写缓冲区
		ReadBufferSize:         64 * 1024,   // 64KB读缓冲区
	}

	client := &http.Client{
		Transport: transport,
		// 设置客户端超时，与配置的RequestTimeout保持一致
		Timeout: cfg.RequestTimeout,
	}

	logger.Info("HTTP客户端配置完成 - MaxIdleConns: %d, MaxIdleConnsPerHost: %d, IdleConnTimeout: %v",
		transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.IdleConnTimeout)

	return &ProxyManager{
		config:          cfg,
		cacheManager:    cacheManager,
		healthManager:   healthManager,
		client:          client,
		upstreamProxies: make(map[string]*UpstreamProxyInfo),
	}
}

// HandleRequest 处理请求
func (pm *ProxyManager) HandleRequest(path string, headers http.Header) (*ProxyResponse, error) {
	logger.Info("进入HandleRequest，路径: %s", path)

	// 添加配置信息调试输出
	logger.Info("当前配置 - UPSTREAM_TYPE: %s, REQUEST_TIMEOUT: %v",
		pm.config.UpstreamType, pm.config.RequestTimeout)

	// 选择上游服务器
	logger.Info("开始选择上游服务器...")
	server := pm.selectUpstreamServer()
	logger.Info("选择上游服务器完成，结果: %v", server != nil)
	if server == nil {
		logger.Error("没有可用的上游服务器")
		return nil, fmt.Errorf("没有可用的上游服务器")
	}

	// 构建请求URL
	upstreamURL := fmt.Sprintf("%s%s", server.URL, path)
	logger.Info("发送上游请求: %s (服务器: %s)", upstreamURL, server.URL)

	// 使用超时重试机制发送请求
	response, err := pm.makeRequestWithTimeoutRetry(upstreamURL, headers, server)
	if err != nil {
		logger.Error("请求失败: %v", err)
		return nil, err
	}

	// 处理响应
	return pm.processResponse(response, server.URL)
}

// selectUpstreamServer 选择上游服务器
func (pm *ProxyManager) selectUpstreamServer() *health.Server {
	healthyServers := pm.healthManager.GetHealthyServers()
	allServers := pm.healthManager.GetAllServers()

	logger.Info("服务器状态检查: 总共%d个服务器, 健康%d个", len(allServers), len(healthyServers))

	// 详细输出所有服务器状态
	logger.Info("所有服务器状态详情:")
	for _, server := range allServers {
		confidence := pm.healthManager.GetServerConfidence(server.URL)
		isReady := pm.healthManager.IsServerReady(server.URL)
		status := "未准备好"
		if isReady {
			status = "已准备好"
		}

		logger.Info("  - %s: 状态=%s, %s, 优先级=%d, 连接率=%.1f%% (置信度=%.1f%%), 基础权重=%d, 动态权重=%d, 综合权重=%d",
			server.URL, server.Status, status, server.Priority, server.ConnectionRate*100, confidence*100,
			server.BaseWeight, server.DynamicWeight, server.CombinedWeight)
	}

	// 如果没有健康服务器，使用所有服务器，但降低权重
	var availableServers []*health.Server
	if len(healthyServers) == 0 {
		logger.Warn("没有健康的上游服务器，使用所有服务器进行请求（降低权重）:")
		availableServers = allServers
		// 为不健康的服务器降低权重，但允许它们参与请求
		for _, server := range availableServers {
			if server.Status == health.HealthStatusUnhealthy {
				// 不健康服务器的权重降低到原来的1/4，但仍然可以参与
				if server.CombinedWeight > 4 {
					server.CombinedWeight = server.CombinedWeight / 4
				} else {
					server.CombinedWeight = 1
				}
				logger.Info("  - %s: 状态=%s, 降低权重=%d", server.URL, server.Status, server.CombinedWeight)
			}
		}
	} else {
		availableServers = healthyServers
		logger.Info("健康服务器列表:")
		for _, server := range availableServers {
			logger.Info("  - %s: 权重=%d, EWMA=%.0fms", server.URL, server.CombinedWeight, server.LastEWMA)
		}
	}

	// 计算总权重（使用综合权重）
	var totalWeight int
	for _, server := range availableServers {
		totalWeight += server.CombinedWeight
	}

	// 按优先级分组选择服务器
	selectedServer := pm.selectServerByPriority(availableServers)

	if selectedServer != nil {
		// 输出累计统计信息
		logger.Success("选择服务器 %s [状态=%s 优先级=%d 连接率=%.1f%% 基础权重=%d 动态权重=%d 综合权重=%d]",
			selectedServer.URL, selectedServer.Status, selectedServer.Priority, selectedServer.ConnectionRate*100,
			selectedServer.BaseWeight, selectedServer.DynamicWeight, selectedServer.CombinedWeight)
		logger.Info("累计统计: 总请求=%d, 成功请求=%d, 样本进度=%d/1000 (%.1f%%)",
			selectedServer.TotalRequests, selectedServer.SuccessRequests,
			selectedServer.TotalRequests, float64(selectedServer.TotalRequests)/10.0)
		return selectedServer
	}

	// 保底返回第一个服务器
	server := availableServers[0]
	logger.Warn("保底选择服务器 %s [状态=%s 优先级=%d 连接率=%.1f%% 基础权重=%d 动态权重=%d 综合权重=%d]",
		server.URL, server.Status, server.Priority, server.ConnectionRate*100,
		server.BaseWeight, server.DynamicWeight, server.CombinedWeight)
	logger.Info("累计统计: 总请求=%d, 成功请求=%d, 样本进度=%d/1000 (%.1f%%)",
		server.TotalRequests, server.SuccessRequests,
		server.TotalRequests, float64(server.TotalRequests)/10.0)
	return server
}

// selectServerByPriority 按优先级选择服务器（样本数量感知）
func (pm *ProxyManager) selectServerByPriority(servers []*health.Server) *health.Server {
	// 按优先级分组
	priorityGroups := make(map[int][]*health.Server)

	for _, server := range servers {
		priority := server.Priority
		priorityGroups[priority] = append(priorityGroups[priority], server)
	}

	logger.Info("优先级分组完成:")
	for priority := 3; priority >= 0; priority-- {
		if group, exists := priorityGroups[priority]; exists && len(group) > 0 {
			logger.Info("  优先级 %d: %d 个服务器", priority, len(group))
			for _, server := range group {
				logger.Info("    - %s [连接率=%.1f%% 基础权重=%d 动态权重=%d 综合权重=%d]",
					server.URL, server.ConnectionRate*100, server.BaseWeight, server.DynamicWeight, server.CombinedWeight)
			}
		}
	}

	// 从高优先级到低优先级选择
	for priority := 3; priority >= 0; priority-- {
		if group, exists := priorityGroups[priority]; exists && len(group) > 0 {
			logger.Info("尝试优先级 %d 组，包含 %d 个服务器", priority, len(group))

			// 在相同优先级组内，按动态权重选择
			if len(group) == 1 {
				logger.Info("优先级 %d 组只有一个服务器，直接选择: %s", priority, group[0].URL)
				return group[0]
			}

			// 多个服务器时，考虑样本数量
			logger.Info("优先级 %d 组有多个服务器，进入样本数量选择阶段", priority)
			selectedServer := pm.selectServerBySampleSize(group)
			if selectedServer != nil {
				logger.Success("优先级 %d 组选择成功: %s", priority, selectedServer.URL)
				return selectedServer
			}
		}
	}

	logger.Warn("所有优先级组都没有找到合适的服务器")
	return nil
}

// selectServerBySampleSize 基于样本数量选择服务器
func (pm *ProxyManager) selectServerBySampleSize(servers []*health.Server) *health.Server {
	// 分离已准备好和未准备好的服务器
	var readyServers, unreadyServers []*health.Server

	for _, server := range servers {
		if pm.healthManager.IsServerReady(server.URL) {
			readyServers = append(readyServers, server)
		} else {
			unreadyServers = append(unreadyServers, server)
		}
	}

	// 优先选择已准备好的服务器
	if len(readyServers) > 0 {
		logger.Info("在 %d 个已准备好的服务器中选择", len(readyServers))
		return pm.selectServerByWeight(readyServers)
	}

	// 如果没有准备好的服务器，使用未准备好的服务器
	// 这是关键：确保服务启动后立即可用，即使样本不足
	if len(unreadyServers) > 0 {
		logger.Info("使用 %d 个未准备好的服务器（样本不足，但服务立即可用）", len(unreadyServers))
		return pm.selectServerByWeight(unreadyServers)
	}

	return nil
}

// selectServerByWeight 基于权重选择服务器
func (pm *ProxyManager) selectServerByWeight(servers []*health.Server) *health.Server {
	if len(servers) == 1 {
		logger.Info("只有一个服务器可选: %s [优先级=%d 连接率=%.1f%% 基础权重=%d 动态权重=%d 综合权重=%d]",
			servers[0].URL, servers[0].Priority, servers[0].ConnectionRate*100,
			servers[0].BaseWeight, servers[0].DynamicWeight, servers[0].CombinedWeight)
		return servers[0]
	}

	// 按动态权重概率选择
	var totalWeight int
	for _, server := range servers {
		totalWeight += server.DynamicWeight
	}

	logger.Info("权重选择开始 - 总动态权重: %d, 服务器数量: %d", totalWeight, len(servers))

	random := float64(time.Now().UnixNano()) / float64(time.Second)
	weightSum := 0

	for _, server := range servers {
		weightSum += server.DynamicWeight
		if float64(weightSum) > random*float64(totalWeight) {
			logger.Success("权重选择成功: %s [优先级=%d 连接率=%.1f%% 基础权重=%d 动态权重=%d 综合权重=%d 样本数=%d]",
				server.URL, server.Priority, server.ConnectionRate*100,
				server.BaseWeight, server.DynamicWeight, server.CombinedWeight, server.TotalRequests)
			return server
		}
	}

	// 如果概率选择失败，返回第一个服务器
	logger.Warn("权重选择失败，使用第一个服务器: %s [优先级=%d 连接率=%.1f%% 基础权重=%d 动态权重=%d 综合权重=%d]",
		servers[0].URL, servers[0].Priority, servers[0].ConnectionRate*100,
		servers[0].BaseWeight, servers[0].DynamicWeight, servers[0].CombinedWeight)
	return servers[0]
}

// makeRequest 发送HTTP请求
func (pm *ProxyManager) makeRequest(url string, headers http.Header) (*http.Response, error) {
	// 使用配置的超时时间，与proxy_app.go保持一致
	timeout := pm.config.RequestTimeout
	logger.Info("使用配置的超时时间: %v", timeout)

	// 所有请求都使用GET方法
	method := "GET"

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		logger.Error("创建HTTP请求失败: %v", err)
		return nil, err
	}

	// 转发重要的请求头
	if auth := headers.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if contentType := headers.Get("Content-Type"); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept := headers.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	if userAgent := headers.Get("User-Agent"); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	// 设置默认请求头（如果没有的话）
	if req.Header.Get("Accept") == "" {
		if pm.config.UpstreamType == "tmdb-image" {
			// 图片请求使用更通用的接受类型，与JavaScript版本保持一致
			req.Header.Set("Accept", "*/*")
		} else {
			// API请求使用JSON接受类型
			req.Header.Set("Accept", "application/json")
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "tmdb-go-proxy/1.0")
	}

	// 调试：显示请求头
	logger.Info("发送请求头 - Accept: %s, User-Agent: %s, 超时时间: %v", req.Header.Get("Accept"), req.Header.Get("User-Agent"), timeout)

	// 添加调试信息
	logger.Info("开始发送请求: %s", url)
	logger.Info("HTTP客户端配置 - Transport: %T, 超时: %v", pm.client.Transport, timeout)

	// 添加请求开始时间记录
	startTime := time.Now()

	// 添加更多调试信息
	logger.Info("HTTP客户端配置: %+v", pm.client)

	// 发送请求并添加详细的错误处理
	logger.Info("即将调用pm.client.Do...")
	resp, err := pm.client.Do(req)
	requestDuration := time.Since(startTime)

	logger.Info("pm.client.Do调用完成，耗时: %v", requestDuration)

	if err != nil {
		logger.Error("请求发送失败: %v (耗时: %v)", err, requestDuration)

		// 使用错误检测函数提供更详细的错误分类
		if isConnectionError(err) {
			logger.Error("连接错误: %s - %v", url, err)
		} else if isRetryableError(err) {
			logger.Warn("可重试错误: %s - %v", url, err)
		} else {
			logger.Error("不可重试错误: %s - %v", url, err)
		}

		return nil, err
	}

	logger.Info("请求发送成功，状态码: %d, Content-Type: %s, 耗时: %v", resp.StatusCode, resp.Header.Get("Content-Type"), requestDuration)

	// 检查响应状态码
	if resp.StatusCode >= 400 {
		logger.Warn("上游服务器返回错误状态码: %d, URL: %s", resp.StatusCode, url)
	}

	return resp, nil
}

// isConnectionError 检测是否是连接错误，基于proxy_app.go的经验
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	// 检查网络错误
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout() || netErr.Temporary()
	}

	// 检查具体的错误字符串
	errStr := err.Error()
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "broken pipe")
}

// isRetryableError 检测是否是可重试的错误，基于proxy_app.go的经验
func isRetryableError(err error) bool {
	if isConnectionError(err) {
		return true
	}

	// 检查其他可重试的错误
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "temporary") ||
		strings.Contains(errStr, "rate limit") ||
		strings.Contains(errStr, "too many requests")
}

// getEnvAsInt 获取环境变量作为整数
func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// isValidImage 验证图片数据，与proxy_app.go保持一致
func isValidImage(data []byte) bool {
	contentType := http.DetectContentType(data)
	return strings.HasPrefix(contentType, "image/")
}

// processResponse 处理响应
func (pm *ProxyManager) processResponse(resp *http.Response, serverURL string) (*ProxyResponse, error) {
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	// 检测上游服务器是否为TMDB代理
	pm.detectUpstreamProxy(resp, serverURL)

	// 根据上游类型处理响应
	var responseData interface{}
	var isImage bool

	switch pm.config.UpstreamType {
	case "tmdb-api":
		// API请求处理 - 保持原有逻辑不变
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("读取响应体失败: %w", err)
		}

		// 检查Content-Type是否为JSON
		if strings.Contains(contentType, "application/json") {
			// API响应解析为JSON
			var jsonData interface{}
			if err := json.Unmarshal(body, &jsonData); err != nil {
				// 添加调试信息，显示响应的前100个字符
				responsePreview := string(body)
				if len(responsePreview) > 100 {
					responsePreview = responsePreview[:100] + "..."
				}
				logger.Error("JSON解析失败 - Content-Type: %s, 响应预览: %s", contentType, responsePreview)
				return nil, fmt.Errorf("解析JSON失败: %w", err)
			}
			responseData = jsonData
		} else {
			// 如果不是JSON类型，直接返回原始数据
			responseData = string(body)
			logger.Warn("API响应不是JSON格式 - Content-Type: %s, 长度: %d", contentType, len(body))
		}

	case "tmdb-image":
		// 图片请求处理 - 使用与proxy_app.go一致的简单方式
		logger.Info("开始读取图片响应体，Content-Type: %s", contentType)

		// 直接读取响应体，与proxy_app.go保持一致
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Error("读取图片响应体失败: %v", err)
			return nil, fmt.Errorf("读取图片响应体失败: %w", err)
		}

		logger.Info("图片响应体读取成功，大小: %d字节", len(body))

		// 验证图片数据，与proxy_app.go保持一致
		if !isValidImage(body) {
			logger.Warn("响应内容不是有效的图片数据")
			// 不立即失败，尝试使用声明的Content-Type
		}

		// 确保返回的是[]byte类型，与JavaScript版本的Buffer处理一致
		responseData = body
		isImage = true

		logger.Info("tmdb-image处理完成，数据类型: %T, 大小: %d字节", responseData, len(body))

	case "custom":
		// 自定义类型处理 - 保持原有逻辑
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("读取响应体失败: %w", err)
		}

		if pm.config.CustomContentType != "" {
			contentType = pm.config.CustomContentType
		}

		if strings.Contains(contentType, "json") {
			var jsonData interface{}
			if err := json.Unmarshal(body, &jsonData); err != nil {
				return nil, fmt.Errorf("解析JSON失败: %w", err)
			}
			responseData = jsonData
		} else {
			responseData = body
			if strings.HasPrefix(contentType, "image/") {
				isImage = true
			}
		}

	default:
		return nil, fmt.Errorf("未知的上游类型: %s", pm.config.UpstreamType)
	}

	// 验证响应
	if !pm.ValidateResponse(responseData, contentType) {
		return nil, fmt.Errorf("响应验证失败")
	}

	return &ProxyResponse{
		Data:         responseData,
		ContentType:  contentType,
		ResponseTime: 0, // 响应时间在调用方计算
		IsImage:      isImage,
	}, nil
}

// ValidateResponse 验证响应
func (pm *ProxyManager) ValidateResponse(data interface{}, contentType string) bool {
	if data == nil || contentType == "" {
		logger.Error("无效的响应: 缺少数据或Content-Type")
		return false
	}

	// 通用的MIME类型检查
	mimeCategory := strings.Split(contentType, ";")[0]
	mimeCategory = strings.ToLower(strings.TrimSpace(mimeCategory))

	switch pm.config.UpstreamType {
	case "tmdb-api":
		// API响应验证 - 支持JSON和非JSON响应
		if strings.Contains(mimeCategory, "application/json") {
			// 验证JSON数据
			switch v := data.(type) {
			case map[string]interface{}, []interface{}:
				return true
			case string:
				var jsonData interface{}
				return json.Unmarshal([]byte(v), &jsonData) == nil
			default:
				logger.Error("API响应JSON数据格式错误: %T", data)
				return false
			}
		} else {
			// 非JSON响应验证 - 确保数据非空
			switch v := data.(type) {
			case string:
				return len(v) > 0
			case []byte:
				return len(v) > 0
			default:
				logger.Error("API响应非JSON数据格式错误: %T", data)
				return false
			}
		}

	case "tmdb-image":
		// 图片响应验证 - 与JavaScript版本保持一致
		if !strings.HasPrefix(mimeCategory, "image/") {
			logger.Error("图片响应类型错误: %s", contentType)
			return false
		}

		// 验证图片数据 - 支持多种数据类型，与JavaScript版本一致
		switch v := data.(type) {
		case []byte:
			// []byte类型直接通过，使用http.DetectContentType进行额外验证
			if len(v) > 0 {
				detectedType := http.DetectContentType(v)
				if strings.HasPrefix(detectedType, "image/") {
					logger.Info("图片数据验证通过 - 大小: %d字节, 检测类型: %s", len(v), detectedType)
					return true
				} else {
					logger.Warn("图片数据验证警告 - 检测类型: %s, 但声明类型: %s", detectedType, contentType)
					// 不立即失败，返回true让程序继续处理
					return true
				}
			}
			return false
		case string:
			// string类型也支持，会被转换为[]byte
			if len(v) > 0 {
				detectedType := http.DetectContentType([]byte(v))
				if strings.HasPrefix(detectedType, "image/") {
					logger.Info("图片数据验证通过 - 大小: %d字节, 检测类型: %s", len(v), detectedType)
					return true
				} else {
					logger.Warn("图片数据验证警告 - 检测类型: %s, 但声明类型: %s", detectedType, contentType)
					// 不立即失败，返回true让程序继续处理
					return true
				}
			}
			return false
		default:
			logger.Error("图片数据格式错误: %T", data)
			return false
		}

	case "custom":
		// 自定义类型验证：更灵活的验证规则
		if strings.Contains(mimeCategory, "application/json") {
			switch v := data.(type) {
			case map[string]interface{}, []interface{}:
				return true
			case string:
				var jsonData interface{}
				return json.Unmarshal([]byte(v), &jsonData) == nil
			default:
				logger.Error("自定义类型JSON解析失败: %T", data)
				return false
			}
		} else if strings.HasPrefix(mimeCategory, "image/") {
			switch data.(type) {
			case []byte, string:
				return true
			default:
				logger.Error("自定义类型图片数据格式错误: %T", data)
				return false
			}
		} else if strings.HasPrefix(mimeCategory, "text/") {
			switch data.(type) {
			case string, []byte:
				return true
			default:
				logger.Error("自定义类型文本数据格式错误: %T", data)
				return false
			}
		}
		// 其他类型确保数据非空
		return data != nil

	default:
		// 默认验证
		if strings.Contains(mimeCategory, "application/json") {
			switch v := data.(type) {
			case map[string]interface{}, []interface{}:
				return true
			case string:
				var jsonData interface{}
				return json.Unmarshal([]byte(v), &jsonData) == nil
			default:
				logger.Error("JSON解析失败")
				return false
			}
		} else if strings.HasPrefix(mimeCategory, "image/") {
			switch data.(type) {
			case []byte, string:
				return true
			default:
				return false
			}
		} else if strings.HasPrefix(mimeCategory, "text/") {
			switch data.(type) {
			case string, []byte:
				return true
			default:
				return false
			}
		}

		// 其他类型确保数据非空
		return data != nil
	}
}

// makeRequestWithTimeoutRetry 带超时重试的请求
func (pm *ProxyManager) makeRequestWithTimeoutRetry(url string, headers http.Header, primaryServer *health.Server) (*http.Response, error) {
	// 创建请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	// 转发重要的请求头
	if auth := headers.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if contentType := headers.Get("Content-Type"); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept := headers.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	if userAgent := headers.Get("User-Agent"); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	// 发送请求
	startTime := time.Now()
	resp, err := pm.client.Do(req)
	responseTime := time.Since(startTime)

	if err != nil {
		// 请求失败，尝试切换到其他健康上游
		logger.Warn("请求失败，尝试切换到其他健康上游: %s", url)
		return pm.retryWithOtherHealthyServer(url, headers, primaryServer)
	}

	// 请求成功，更新服务器性能指标
	pm.updateServerPerformance(primaryServer.URL, responseTime)

	logger.Info("请求成功: %s (响应时间: %v)", url, responseTime)
	return resp, nil
}

// retryWithOtherHealthyServer 使用其他健康上游重试
func (pm *ProxyManager) retryWithOtherHealthyServer(originalURL string, headers http.Header, excludeServer *health.Server) (*http.Response, error) {
	// 获取其他健康的服务器
	healthyServers := pm.healthManager.GetHealthyServers()
	var availableServers []*health.Server

	for _, server := range healthyServers {
		if server.URL != excludeServer.URL {
			availableServers = append(availableServers, server)
		}
	}

	if len(availableServers) == 0 {
		logger.Error("没有其他健康的服务器可用")
		return nil, fmt.Errorf("没有其他健康的服务器可用")
	}

	logger.Info("找到 %d 个其他健康的服务器，开始重试", len(availableServers))

	// 按优先级选择重试服务器
	selectedServer := pm.selectServerByPriority(availableServers)
	if selectedServer == nil {
		// 如果优先级选择失败，使用第一个可用服务器
		selectedServer = availableServers[0]
	}

	// 构建新的请求URL
	path := strings.TrimPrefix(originalURL, excludeServer.URL)
	retryURL := fmt.Sprintf("%s%s", selectedServer.URL, path)

	logger.Info("重试请求: %s (服务器: %s)", retryURL, selectedServer.URL)

	// 发送重试请求
	req, err := http.NewRequest("GET", retryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建重试请求失败: %v", err)
	}

	// 转发请求头
	if auth := headers.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if contentType := headers.Get("Content-Type"); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept := headers.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	if userAgent := headers.Get("User-Agent"); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	// 发送重试请求
	startTime := time.Now()
	resp, err := pm.client.Do(req)
	responseTime := time.Since(startTime)

	if err != nil {
		return nil, fmt.Errorf("重试请求失败: %v", err)
	}

	// 重试成功，更新服务器性能指标
	pm.updateServerPerformance(selectedServer.URL, responseTime)

	logger.Success("重试请求成功: %s (响应时间: %v)", retryURL, responseTime)
	return resp, nil
}

// updateServerPerformance 更新服务器性能指标
func (pm *ProxyManager) updateServerPerformance(serverURL string, responseTime time.Duration) {
	// 更新动态权重
	pm.healthManager.UpdateDynamicWeight(serverURL, int64(responseTime))

	// 更新连接率（成功）
	// 获取服务器对象并更新连接率
	if server := pm.healthManager.GetServerByURL(serverURL); server != nil {
		pm.healthManager.UpdateConnectionRate(server, true, responseTime) // 成功，包含响应时间
		logger.Debug("更新服务器 %s 性能指标: 响应时间=%v, 连接率更新", serverURL, responseTime)
	}
}

// detectUpstreamProxy 检测上游服务器是否为TMDB代理
func (pm *ProxyManager) detectUpstreamProxy(resp *http.Response, serverURL string) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	// 检查响应头是否包含TMDB代理标识符
	proxyHeader := resp.Header.Get("X-TMDB-Proxy")
	versionHeader := resp.Header.Get("X-TMDB-Proxy-Version")

	if proxyHeader != "" {
		// 找到匹配的上游代理
		info := pm.upstreamProxies[serverURL]
		if info == nil {
			info = &UpstreamProxyInfo{
				URL:         serverURL,
				IsTMDBProxy: true,
				Version:     versionHeader,
				LastChecked: time.Now(),
				CheckCount:  1,
			}
			logger.Info("检测到上游服务器 %s 也是TMDB代理程序 (版本: %s)", serverURL, versionHeader)
		} else {
			info.IsTMDBProxy = true
			info.Version = versionHeader
			info.LastChecked = time.Now()
			info.CheckCount++
		}
		pm.upstreamProxies[serverURL] = info
	} else {
		// 如果没有检测到标识符，也记录这个服务器（标记为非代理）
		if info := pm.upstreamProxies[serverURL]; info == nil {
			pm.upstreamProxies[serverURL] = &UpstreamProxyInfo{
				URL:         serverURL,
				IsTMDBProxy: false,
				Version:     "",
				LastChecked: time.Now(),
				CheckCount:  1,
			}
		}
	}
}

// IsUpstreamTMDBProxy 检查上游服务器是否为TMDB代理
func (pm *ProxyManager) IsUpstreamTMDBProxy(serverURL string) (bool, string) {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	info := pm.upstreamProxies[serverURL]
	if info != nil && info.IsTMDBProxy {
		return true, info.Version
	}
	return false, ""
}

// GetUpstreamProxyInfo 获取所有上游代理信息
func (pm *ProxyManager) GetUpstreamProxyInfo() map[string]*UpstreamProxyInfo {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()

	result := make(map[string]*UpstreamProxyInfo)
	for k, v := range pm.upstreamProxies {
		result[k] = v
	}
	return result
}

// performUpstreamCacheClear 执行上游代理缓存清理
func (pm *ProxyManager) performUpstreamCacheClear(cacheType string) {
	logger.Info("开始执行上游代理缓存清理联动 (类型: %s)", cacheType)

	upstreamInfo := pm.GetUpstreamProxyInfo()
	if len(upstreamInfo) == 0 {
		logger.Info("没有检测到上游代理服务器，跳过联动清理")
		return
	}

	for serverURL, info := range upstreamInfo {
		if !info.IsTMDBProxy {
			logger.Debug("上游服务器 %s 不是TMDB代理，跳过", serverURL)
			continue
		}

		// 异步清理每个上游代理的缓存
		go func(url string, cacheType string) {
			if err := pm.clearUpstreamCache(url, cacheType); err != nil {
				logger.Warn("清理上游代理 %s 缓存失败: %v", url, err)
			} else {
				logger.Info("成功清理上游代理 %s 的缓存 (类型: %s)", url, cacheType)
			}
		}(serverURL, cacheType)
	}
}

// clearUpstreamCache 清理上游代理的缓存
func (pm *ProxyManager) clearUpstreamCache(serverURL string, cacheType string) error {
	// 构建上游缓存清理API的URL
	clearURL := fmt.Sprintf("%s/cache/clear?type=%s", serverURL, cacheType)

	logger.Info("调用上游代理缓存清理API: %s", clearURL)

	// 创建请求
	req, err := http.NewRequest("POST", clearURL, nil)
	if err != nil {
		return fmt.Errorf("创建上游缓存清理请求失败: %v", err)
	}

	// 设置User-Agent标识这是一个联动清理请求
	req.Header.Set("User-Agent", "tmdb-go-proxy/1.0 (cache-clear-linkage)")
	req.Header.Set("X-Cache-Clear-Linkage", "true")

	// 发送请求
	resp, err := pm.client.Do(req)
	if err != nil {
		return fmt.Errorf("发送上游缓存清理请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("上游缓存清理请求失败，状态码: %d", resp.StatusCode)
	}

	logger.Info("上游代理 %s 缓存清理完成", serverURL)
	return nil
}
