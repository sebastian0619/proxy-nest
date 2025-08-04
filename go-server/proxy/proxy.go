package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	client := &http.Client{
		Timeout: cfg.RequestTimeout,
	}

	return &ProxyManager{
		config:        cfg,
		cacheManager:  cacheManager,
		healthManager: healthManager,
		client:        client,
	}
}

// HandleRequest 处理请求
func (pm *ProxyManager) HandleRequest(path string, headers http.Header) (*ProxyResponse, error) {
	// 选择上游服务器
	server := pm.selectUpstreamServer()
	if server == nil {
		return nil, fmt.Errorf("没有可用的上游服务器")
	}

	// 构建请求URL
	requestURL := fmt.Sprintf("%s%s", server.URL, path)
	// 不输出每个请求的详细信息，避免日志过于冗余

	// 发送请求
	startTime := time.Now()
	response, err := pm.makeRequest(requestURL, headers)
	responseTime := time.Since(startTime).Milliseconds()

	if err != nil {
		// 报告服务器不健康
		pm.healthManager.ReportUnhealthyServer(server.URL, "main", err.Error())
		return nil, fmt.Errorf("请求失败: %w", err)
	}

	// 更新响应时间
	pm.updateServerResponseTime(server, responseTime)

	// 处理响应
	return pm.processResponse(response, responseTime)
}

// selectUpstreamServer 选择上游服务器
func (pm *ProxyManager) selectUpstreamServer() *health.Server {
	healthyServers := pm.healthManager.GetHealthyServers()
	if len(healthyServers) == 0 {
		return nil
	}

	// 计算总权重
	var totalWeight int
	for _, server := range healthyServers {
		totalWeight += server.DynamicWeight
	}

	// 按权重概率选择
	random := float64(time.Now().UnixNano()) / float64(time.Second)
	weightSum := 0

	for _, server := range healthyServers {
		weightSum += server.DynamicWeight
		if float64(weightSum) > random*float64(totalWeight) {
			logger.Success("选择服务器 %s [状态=%s 基础权重=%d 动态权重=%d 综合权重=%d 实际权重=%d 概率=%.1f%% 最近响应=%dms]",
				server.URL, server.Status, server.BaseWeight, server.DynamicWeight, server.DynamicWeight, server.DynamicWeight,
				float64(server.DynamicWeight)/float64(totalWeight)*100, server.LastResponseTime)
			return server
		}
	}

	// 保底返回第一个服务器
	server := healthyServers[0]
	logger.Warn("选择服务器 %s [状态=%s 基础权重=%d 动态权重=%d 综合权重=%d 实际权重=%d 概率=%.1f%% 最近响应=%dms]",
		server.URL, server.Status, server.BaseWeight, server.DynamicWeight, server.DynamicWeight, server.DynamicWeight,
		float64(server.DynamicWeight)/float64(totalWeight)*100, server.LastResponseTime)
	return server
}

// makeRequest 发送HTTP请求
func (pm *ProxyManager) makeRequest(url string, headers http.Header) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pm.config.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
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

	return pm.client.Do(req)
}

// processResponse 处理响应
func (pm *ProxyManager) processResponse(resp *http.Response, responseTime int64) (*ProxyResponse, error) {
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")

	// 根据上游类型处理响应
	var responseData interface{}
	var isImage bool

	switch pm.config.UpstreamType {
	case "tmdb-api":
		// API响应解析为JSON
		var jsonData interface{}
		if err := json.Unmarshal(body, &jsonData); err != nil {
			return nil, fmt.Errorf("解析JSON失败: %w", err)
		}
		responseData = jsonData
		contentType = "application/json"

	case "tmdb-image":
		// 图片数据直接返回
		responseData = body
		isImage = true

	case "custom":
		// 自定义类型根据Content-Type判断
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
		// API响应验证
		if !strings.Contains(mimeCategory, "application/json") {
			logger.Error("API响应类型错误: %s", contentType)
			return false
		}

		// 验证JSON数据
		switch v := data.(type) {
		case map[string]interface{}, []interface{}:
			return true
		case string:
			var jsonData interface{}
			return json.Unmarshal([]byte(v), &jsonData) == nil
		default:
			logger.Error("API响应数据格式错误: %T", data)
			return false
		}

	case "tmdb-image":
		// 图片响应验证
		if !strings.HasPrefix(mimeCategory, "image/") {
			logger.Error("图片响应类型错误: %s", contentType)
			return false
		}

		// 验证图片数据
		switch data.(type) {
		case []byte:
			return true
		case string:
			return true
		default:
			logger.Error("图片数据格式错误: %T", data)
			return false
		}

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

// updateServerResponseTime 更新服务器响应时间
func (pm *ProxyManager) updateServerResponseTime(server *health.Server, responseTime int64) {
	// 这里可以添加更新服务器响应时间的逻辑
	// 由于健康管理器已经处理了响应时间更新，这里暂时不需要额外处理
}
