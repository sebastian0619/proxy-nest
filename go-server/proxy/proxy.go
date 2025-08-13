package proxy

import (
	"context"
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

	"proxy-nest-go/cache"
	"proxy-nest-go/config"
	"proxy-nest-go/health"
	"proxy-nest-go/logger"
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
		// 不设置Timeout，使用context控制超时
	}

	logger.Info("HTTP客户端配置完成 - MaxIdleConns: %d, MaxIdleConnsPerHost: %d, IdleConnTimeout: %v, DialTimeout: %v",
		transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.IdleConnTimeout, transport.DialContext)

	return &ProxyManager{
		config:        cfg,
		cacheManager:  cacheManager,
		healthManager: healthManager,
		client:        client,
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
	requestURL := fmt.Sprintf("%s%s", server.URL, path)
	logger.Info("发送上游请求: %s (服务器: %s)", requestURL, server.URL)

	// 发送请求（带重试机制，与JavaScript版本保持一致）
	startTime := time.Now()
	logger.Info("开始调用makeRequest...")

	// 实现3次重试机制，与JavaScript版本保持一致
	var response *http.Response
	var err error
	maxRetries := 3

	for retryCount := 0; retryCount < maxRetries; retryCount++ {
		if retryCount > 0 {
			logger.Info("重试请求 (%d/%d): %s", retryCount+1, maxRetries, requestURL)
			// 重试前等待1秒，与JavaScript版本保持一致
			time.Sleep(1 * time.Second)
		}

		response, err = pm.makeRequest(requestURL, headers)

		if err == nil {
			break // 请求成功，跳出重试循环
		}

		// 检查是否是可重试的错误
		if !isRetryableError(err) {
			logger.Error("遇到不可重试的错误，停止重试: %v", err)
			break
		}

		if retryCount == maxRetries-1 {
			logger.Error("达到最大重试次数 (%d)，请求最终失败: %s -> %v", maxRetries, requestURL, err)
		} else {
			logger.Warn("请求失败 (%d/%d): %s -> %v", retryCount+1, maxRetries, requestURL, err)
		}
	}

	responseTime := time.Since(startTime).Milliseconds()

	logger.Info("makeRequest调用完成，错误: %v, 响应: %v", err, response != nil)

	if err != nil {
		// 报告服务器不健康
		pm.healthManager.ReportUnhealthyServer(server.URL, "main", err.Error())
		logger.Error("上游请求失败: %s -> %v", requestURL, err)
		return nil, fmt.Errorf("请求失败: %w", err)
	}

	logger.Success("上游请求成功: %s (响应时间: %dms, 状态码: %d)", requestURL, responseTime, response.StatusCode)

	// 更新动态权重（基于实际请求）
	pm.healthManager.UpdateDynamicWeight(server.URL, responseTime)

	// 处理响应
	logger.Info("开始调用processResponse...")
	proxyResponse, err := pm.processResponse(response, responseTime)
	logger.Info("processResponse调用完成，错误: %v, 响应: %v", err, proxyResponse != nil)

	if err != nil {
		// 不立即标记为不健康，先记录错误信息
		logger.Error("响应处理失败: %s -> %v", requestURL, err)
		return nil, fmt.Errorf("响应处理失败: %w", err)
	}

	logger.Info("HandleRequest成功完成，返回响应")
	return proxyResponse, nil
}

// selectUpstreamServer 选择上游服务器
func (pm *ProxyManager) selectUpstreamServer() *health.Server {
	healthyServers := pm.healthManager.GetHealthyServers()
	allServers := pm.healthManager.GetAllServers()

	logger.Info("服务器状态检查: 总共%d个服务器, 健康%d个", len(allServers), len(healthyServers))

	// 详细输出所有服务器状态
	logger.Info("所有服务器状态详情:")
	for _, server := range allServers {
		logger.Info("  - %s: 状态=%s, 错误次数=%d, 最后检查=%v, 权重=%d",
			server.URL, server.Status, server.ErrorCount, server.LastCheckTime, server.CombinedWeight)
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

	// 按权重概率选择
	random := float64(time.Now().UnixNano()) / float64(time.Second)
	weightSum := 0

	for _, server := range availableServers {
		weightSum += server.CombinedWeight
		if float64(weightSum) > random*float64(totalWeight) {
			logger.Success("选择服务器 %s [状态=%s 基础权重=%d 动态权重=%d 综合权重=%d 实际权重=%d 概率=%.1f%% EWMA=%.0fms]",
				server.URL, server.Status, server.BaseWeight, server.DynamicWeight, server.CombinedWeight, server.CombinedWeight,
				float64(server.CombinedWeight)/float64(totalWeight)*100, server.LastEWMA)
			return server
		}
	}

	// 保底返回第一个服务器
	server := availableServers[0]
	logger.Warn("选择服务器 %s [状态=%s 基础权重=%d 动态权重=%d 综合权重=%d 实际权重=%d 概率=%.1f%% EWMA=%.0fms]",
		server.URL, server.Status, server.BaseWeight, server.DynamicWeight, server.CombinedWeight, server.CombinedWeight,
		float64(server.CombinedWeight)/float64(totalWeight)*100, server.LastEWMA)
	return server
}

// makeRequest 发送HTTP请求
func (pm *ProxyManager) makeRequest(url string, headers http.Header) (*http.Response, error) {
	// 使用配置的超时时间，与proxy_app.go保持一致
	timeout := pm.config.RequestTimeout
	logger.Info("使用配置的超时时间: %v", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 所有请求都使用GET方法
	method := "GET"

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
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
	logger.Info("请求上下文状态: %v", ctx.Err())

	// 发送请求并添加详细的错误处理
	logger.Info("即将调用pm.client.Do...")
	resp, err := pm.client.Do(req)
	requestDuration := time.Since(startTime)

	logger.Info("pm.client.Do调用完成，耗时: %v", requestDuration)

	if err != nil {
		logger.Error("请求发送失败: %v (耗时: %v)", err, requestDuration)

		// 检查是否是超时错误
		if ctx.Err() == context.DeadlineExceeded {
			logger.Error("请求超时: %s (超时设置: %v)", url, timeout)
		} else if ctx.Err() == context.Canceled {
			logger.Error("请求被取消: %s", url)
		}

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
func (pm *ProxyManager) processResponse(resp *http.Response, responseTime int64) (*ProxyResponse, error) {
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

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
		ResponseTime: responseTime,
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
