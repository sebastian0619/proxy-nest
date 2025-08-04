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
	logger.Info("进入HandleRequest，路径: %s", path)

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
	logger.Info("发送上游请求: %s", requestURL)

	// 发送请求
	startTime := time.Now()
	response, err := pm.makeRequest(requestURL, headers)
	responseTime := time.Since(startTime).Milliseconds()

	if err != nil {
		// 报告服务器不健康
		pm.healthManager.ReportUnhealthyServer(server.URL, "main", err.Error())
		logger.Error("上游请求失败: %s -> %v", requestURL, err)
		return nil, fmt.Errorf("请求失败: %w", err)
	}

	logger.Success("上游请求成功: %s (响应时间: %dms)", requestURL, responseTime)

	// 更新响应时间
	pm.updateServerResponseTime(server, responseTime)

	// 处理响应
	return pm.processResponse(response, responseTime)
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

	if len(healthyServers) == 0 {
		logger.Error("没有健康的上游服务器可用:")
		for _, server := range allServers {
			logger.Error("  - %s: 状态=%s, 错误次数=%d, 最后检查=%v",
				server.URL, server.Status, server.ErrorCount, server.LastCheckTime)
		}
		return nil
	}

	logger.Info("健康服务器列表:")
	for _, server := range healthyServers {
		logger.Info("  - %s: 权重=%d, EWMA=%.0fms", server.URL, server.CombinedWeight, server.LastEWMA)
	}

	// 计算总权重（使用综合权重）
	var totalWeight int
	for _, server := range healthyServers {
		totalWeight += server.CombinedWeight
	}

	// 按权重概率选择
	random := float64(time.Now().UnixNano()) / float64(time.Second)
	weightSum := 0

	for _, server := range healthyServers {
		weightSum += server.CombinedWeight
		if float64(weightSum) > random*float64(totalWeight) {
			logger.Success("选择服务器 %s [状态=%s 基础权重=%d 动态权重=%d 综合权重=%d 实际权重=%d 概率=%.1f%% EWMA=%.0fms]",
				server.URL, server.Status, server.BaseWeight, server.DynamicWeight, server.CombinedWeight, server.CombinedWeight,
				float64(server.CombinedWeight)/float64(totalWeight)*100, server.LastEWMA)
			return server
		}
	}

	// 保底返回第一个服务器
	server := healthyServers[0]
	logger.Warn("选择服务器 %s [状态=%s 基础权重=%d 动态权重=%d 综合权重=%d 实际权重=%d 概率=%.1f%% EWMA=%.0fms]",
		server.URL, server.Status, server.BaseWeight, server.DynamicWeight, server.CombinedWeight, server.CombinedWeight,
		float64(server.CombinedWeight)/float64(totalWeight)*100, server.LastEWMA)
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

// updateServerResponseTime 更新服务器响应时间
func (pm *ProxyManager) updateServerResponseTime(server *health.Server, responseTime int64) {
	// 这里可以添加更新服务器响应时间的逻辑
	// 由于健康管理器已经处理了响应时间更新，这里暂时不需要额外处理
}
