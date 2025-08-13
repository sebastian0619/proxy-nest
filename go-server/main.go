package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"proxy-nest-go/cache"
	"proxy-nest-go/config"
	"proxy-nest-go/health"
	"proxy-nest-go/logger"
	"proxy-nest-go/proxy"

	"github.com/gin-gonic/gin"
)

func main() {
	// 加载配置
	cfg := config.LoadConfig()

	// 初始化日志系统
	logger.SetLogLevel(os.Getenv("LOG_LEVEL"))

	// 初始化缓存管理器
	cacheManager, err := cache.NewCacheManager(&cfg.Cache)
	if err != nil {
		logger.Error("初始化缓存管理器失败: %v", err)
		os.Exit(1)
	}

	// 初始化健康管理器
	healthManager := health.NewHealthManager(cfg)
	healthManager.StartHealthCheck()

	// 初始化代理管理器
	proxyManager := proxy.NewProxyManager(cfg, cacheManager, healthManager)

	// 设置Gin模式
	gin.SetMode(gin.ReleaseMode)

	// 创建Gin路由
	router := gin.New()

	// 添加中间件
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// 设置路由
	setupRoutes(router, proxyManager, cacheManager, healthManager)

	// 创建HTTP服务器
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}

	// 启动服务器
	go func() {
		logger.Success("服务器启动在端口 %d", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("服务器启动失败: %v", err)
			os.Exit(1)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("正在关闭服务器...")

	// 优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("服务器关闭失败: %v", err)
	}

	// 停止健康检查
	healthManager.StopHealthCheck()

	// 清理连接池
	healthManager.CloseIdleConnections()

	logger.Info("服务器已关闭")
}

// shouldSkipRequest 判断是否应该跳过某些请求
func shouldSkipRequest(path string) bool {
	// 过滤掉常见的非API请求
	skipPaths := []string{
		"/favicon.ico",
		"/robots.txt",
		"/sitemap.xml",
		"/.well-known/",
		"/health",
		"/status",
	}

	for _, skipPath := range skipPaths {
		if path == skipPath || strings.HasPrefix(path, skipPath) {
			return true
		}
	}

	// 特别处理健康检查相关的请求
	if strings.Contains(path, "/3/configuration") && strings.Contains(path, "api_key=") {
		return true
	}

	return false
}

// shouldSkipRequestWithQuery 判断是否应该跳过某些请求（包含查询参数）
func shouldSkipRequestWithQuery(path string, query string) bool {
	// 首先检查路径
	if shouldSkipRequest(path) {
		return true
	}

	// 检查查询参数中是否包含健康检查标识
	if strings.Contains(query, "_health_check=1") {
		return true
	}

	return false
}

// setupRoutes 设置路由
func setupRoutes(router *gin.Engine, proxyManager *proxy.ProxyManager, cacheManager *cache.CacheManager, healthManager *health.HealthManager) {
	// 健康检查端点
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})

	// 统计信息端点
	router.GET("/stats", func(c *gin.Context) {
		// 获取查询参数
		serverURL := c.Query("server")

		if serverURL != "" {
			// 查看指定服务器的统计信息
			stats := healthManager.GetServerStatistics(serverURL)
			c.JSON(http.StatusOK, stats)
		} else {
			// 查看所有服务器的统计信息
			// 同时输出到控制台和返回HTTP响应
			healthManager.PrintServerStatistics()

			// 获取所有服务器的统计信息并返回
			allStats := healthManager.GetAllServersStatistics()
			c.JSON(http.StatusOK, gin.H{
				"message": "统计信息已输出到控制台",
				"servers": allStats,
				"endpoints": gin.H{
					"all_stats":    "/stats",
					"server_stats": "/stats?server=<server_url>",
				},
			})
		}
	})

	// 代理请求处理 - 使用NoRoute捕获所有其他请求
	router.NoRoute(func(c *gin.Context) {
		handleProxyRequest(c, proxyManager, cacheManager)
	})
}

// handleProxyRequest 处理代理请求
func handleProxyRequest(c *gin.Context, proxyManager *proxy.ProxyManager, cacheManager *cache.CacheManager) {
	// 获取请求路径 - 由于使用NoRoute，直接从URL获取路径
	path := c.Request.URL.Path
	if path == "" {
		path = "/"
	}

	// 获取查询参数
	query := c.Request.URL.RawQuery

	// 过滤掉常见的非API请求和健康检查请求
	if shouldSkipRequestWithQuery(path, query) {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "Not Found",
			"message": "This endpoint is not supported",
		})
		return
	}

	// 获取完整的请求URL（包括查询参数）
	// 与JS版本保持一致，处理所有请求包括"/"
	fullURL := c.Request.URL.Path
	if c.Request.URL.RawQuery != "" {
		fullURL += "?" + c.Request.URL.RawQuery
	}

	// 生成缓存键 - 使用完整的URL（包括查询参数）
	cacheKey := cache.GetCacheKey(fullURL)

	// 不输出每个请求的详细信息，避免日志过于冗余

	// 检查是否为健康检查请求（不缓存）
	isHealthCheck := strings.Contains(query, "_health_check=1")

	// 健康检查请求不缓存，直接处理
	if isHealthCheck {
		logger.Info("健康检查请求，跳过缓存: %s", fullURL)
		// 直接调用代理管理器处理请求
		response, err := proxyManager.HandleRequest(fullURL, c.Request.Header)
		if err != nil {
			logger.Error("健康检查请求处理失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "健康检查请求处理失败"})
			return
		}

		// 返回健康检查响应
		c.Header("Content-Type", response.ContentType)
		if response.IsImage {
			// 图片数据需要类型断言
			switch data := response.Data.(type) {
			case []byte:
				c.Data(http.StatusOK, response.ContentType, data)
			case string:
				c.Data(http.StatusOK, response.ContentType, []byte(data))
			default:
				logger.Error("健康检查响应数据类型错误: %T", response.Data)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "健康检查响应数据类型错误"})
				return
			}
		} else {
			c.JSON(http.StatusOK, response.Data)
		}
		return
	}

	// 检查缓存
	if cacheManager.GetConfig().CacheEnabled {
		if cachedItem, err := cacheManager.GetDiskCache().Get(cacheKey); err == nil && cachedItem != nil {
			// 验证缓存内容
			if proxyManager.ValidateResponse(cachedItem.Data, cachedItem.ContentType) {
				c.Header("Content-Type", cachedItem.ContentType)
				// 根据内容类型处理数据
				if cachedItem.IsImage {
					// 图片数据需要确保是[]byte类型
					switch data := cachedItem.Data.(type) {
					case []byte:
						c.Data(http.StatusOK, cachedItem.ContentType, data)
					case string:
						c.Data(http.StatusOK, cachedItem.ContentType, []byte(data))
					default:
						logger.Error("图片缓存数据类型错误: %T", cachedItem.Data)
						c.JSON(http.StatusInternalServerError, gin.H{"error": "图片数据类型错误"})
						return
					}
					logger.CacheHit("磁盘缓存命中: %s (图片, IsImage: %t)", fullURL, cachedItem.IsImage)
				} else if strings.Contains(cachedItem.ContentType, "application/json") {
					c.JSON(http.StatusOK, cachedItem.Data)
					logger.CacheHit("磁盘缓存命中: %s (JSON)", fullURL)
				} else {
					// 非JSON响应，根据数据类型处理
					switch data := cachedItem.Data.(type) {
					case string:
						c.Data(http.StatusOK, cachedItem.ContentType, []byte(data))
					case []byte:
						c.Data(http.StatusOK, cachedItem.ContentType, data)
					default:
						c.JSON(http.StatusOK, cachedItem.Data)
					}
					logger.CacheHit("磁盘缓存命中: %s (其他)", fullURL)
				}
				return
			} else {
				logger.Error("磁盘缓存验证失败: %s", fullURL)
			}
		}

		if cachedItem, exists := cacheManager.GetMemoryCache().Get(cacheKey); exists {
			// 验证缓存内容
			if proxyManager.ValidateResponse(cachedItem.Data, cachedItem.ContentType) {
				c.Header("Content-Type", cachedItem.ContentType)
				// 根据内容类型处理数据
				if cachedItem.IsImage {
					// 图片数据需要确保是[]byte类型
					switch data := cachedItem.Data.(type) {
					case []byte:
						c.Data(http.StatusOK, cachedItem.ContentType, data)
					case string:
						c.Data(http.StatusOK, cachedItem.ContentType, []byte(data))
					default:
						logger.Error("图片缓存数据类型错误: %T", cachedItem.Data)
						c.JSON(http.StatusInternalServerError, gin.H{"error": "图片数据类型错误"})
						return
					}
					logger.CacheHit("内存缓存命中: %s (图片, IsImage: %t)", fullURL, cachedItem.IsImage)
				} else if strings.Contains(cachedItem.ContentType, "application/json") {
					c.JSON(http.StatusOK, cachedItem.Data)
					logger.CacheHit("内存缓存命中: %s (JSON)", fullURL)
				} else {
					// 非JSON响应，根据数据类型处理
					switch data := cachedItem.Data.(type) {
					case string:
						c.Data(http.StatusOK, cachedItem.ContentType, []byte(data))
					case []byte:
						c.Data(http.StatusOK, cachedItem.ContentType, data)
					default:
						c.JSON(http.StatusOK, cachedItem.Data)
					}
					logger.CacheHit("内存缓存命中: %s (其他)", fullURL)
				}
				return
			} else {
				logger.Error("内存缓存验证失败: %s", fullURL)
			}
		}

		logger.CacheMiss("缓存未命中: %s (key: %s)", fullURL, cacheKey)
	}

	// 处理新请求
	logger.Info("处理新请求: %s", fullURL)
	logger.Info("调用proxyManager.HandleRequest，路径: %s", fullURL)

	// 传递完整的请求路径（包括查询参数）给HandleRequest
	response, err := proxyManager.HandleRequest(fullURL, c.Request.Header)

	logger.Info("proxyManager.HandleRequest返回，错误: %v, 响应: %v", err, response != nil)
	if err != nil {
		logger.Error("请求处理失败: %s -> %v", fullURL, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":     err.Error(),
			"url":       fullURL,
			"timestamp": time.Now().Format(time.RFC3339),
		})
		return
	}

	logger.Info("响应处理成功，开始设置响应头...")
	// 设置响应头
	c.Header("Content-Type", response.ContentType)
	logger.Info("响应头设置完成，Content-Type: %s", response.ContentType)

	// 发送响应
	if response.IsImage {
		logger.Info("开始发送图片响应，数据类型: %T", response.Data)
		// 图片数据需要确保是[]byte类型
		switch data := response.Data.(type) {
		case []byte:
			logger.Info("发送[]byte类型图片数据，大小: %d字节", len(data))
			c.Data(http.StatusOK, response.ContentType, data)
			logger.Success("响应已发送: %s (图片, %d字节, %dms)", fullURL, len(data), response.ResponseTime)
		case string:
			logger.Info("发送string类型图片数据，大小: %d字节", len(data))
			imageData := []byte(data)
			c.Data(http.StatusOK, response.ContentType, imageData)
			logger.Success("响应已发送: %s (图片, %d字节, %dms)", fullURL, len(imageData), response.ResponseTime)
		default:
			logger.Error("图片响应数据类型错误: %T", response.Data)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "图片数据类型错误"})
			return
		}
	} else if strings.Contains(response.ContentType, "application/json") {
		c.JSON(http.StatusOK, response.Data)
		logger.Success("响应已发送: %s (JSON, %dms)", fullURL, response.ResponseTime)
	} else {
		// 非JSON响应，根据数据类型处理
		switch data := response.Data.(type) {
		case string:
			c.Data(http.StatusOK, response.ContentType, []byte(data))
		case []byte:
			c.Data(http.StatusOK, response.ContentType, data)
		default:
			// 尝试转换为JSON
			c.JSON(http.StatusOK, response.Data)
		}
		logger.Success("响应已发送: %s (非JSON, %dms)", fullURL, response.ResponseTime)
	}

	// 保存缓存
	if cacheManager.GetConfig().CacheEnabled {
		// 确定是否为图片类型
		isImage := strings.HasPrefix(response.ContentType, "image/")

		cacheItem := &cache.CacheItem{
			Data:         response.Data,
			ContentType:  response.ContentType,
			CreatedAt:    time.Now(),
			ExpireAt:     time.Now().Add(cacheManager.GetConfig().DiskCacheTTL),
			LastAccessed: time.Now(),
			IsImage:      isImage, // 根据ContentType正确设置IsImage字段
		}

		// 保存到内存缓存
		cacheManager.GetMemoryCache().Set(cacheKey, cacheItem, response.ContentType)
		logger.CacheInfo("内存缓存写入: %s (IsImage: %t)", fullURL, isImage)

		// 保存到磁盘缓存
		if err := cacheManager.GetDiskCache().Set(cacheKey, cacheItem, response.ContentType); err != nil {
			logger.Error("保存磁盘缓存失败: %v", err)
		} else {
			logger.CacheInfo("磁盘缓存写入: %s (IsImage: %t)", fullURL, isImage)
		}
	}
}
