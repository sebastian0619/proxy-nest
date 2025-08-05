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

	// 初始化日志
	logger.InitLogger()

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
	setupRoutes(router, proxyManager, cacheManager)

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

	return false
}

// setupRoutes 设置路由
func setupRoutes(router *gin.Engine, proxyManager *proxy.ProxyManager, cacheManager *cache.CacheManager) {
	// 所有路径都走代理处理，包括根路径"/"
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

	// 过滤掉常见的非API请求
	if shouldSkipRequest(path) {
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

	// 生成缓存键
	cacheKey := cache.GetCacheKey(fullURL)

	// 不输出每个请求的详细信息，避免日志过于冗余

	// 检查缓存
	if cacheManager.GetConfig().CacheEnabled {
		if cachedItem, err := cacheManager.GetDiskCache().Get(cacheKey); err == nil && cachedItem != nil {
					// 验证缓存内容
		if proxyManager.ValidateResponse(cachedItem.Data, cachedItem.ContentType) {
			c.Header("Content-Type", cachedItem.ContentType)
			// 根据内容类型处理数据
			if strings.HasPrefix(cachedItem.ContentType, "image/") {
				c.Data(http.StatusOK, cachedItem.ContentType, cachedItem.Data.([]byte))
			} else if strings.Contains(cachedItem.ContentType, "application/json") {
				c.JSON(http.StatusOK, cachedItem.Data)
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
			}
			logger.CacheHit("磁盘缓存命中: %s", fullURL)
			return
		}
		}

		if cachedItem, exists := cacheManager.GetMemoryCache().Get(cacheKey); exists {
			// 验证缓存内容
			if proxyManager.ValidateResponse(cachedItem.Data, cachedItem.ContentType) {
				c.Header("Content-Type", cachedItem.ContentType)
				// 根据内容类型处理数据
				if strings.HasPrefix(cachedItem.ContentType, "image/") {
					c.Data(http.StatusOK, cachedItem.ContentType, cachedItem.Data.([]byte))
				} else if strings.Contains(cachedItem.ContentType, "application/json") {
					c.JSON(http.StatusOK, cachedItem.Data)
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
				}
				logger.CacheHit("内存缓存命中: %s", fullURL)
				return
			}
		}

		logger.CacheMiss("缓存未命中: %s (key: %s)", fullURL, cacheKey)
	}

	// 处理新请求
	logger.Info("处理新请求: %s", fullURL)
	logger.Info("调用proxyManager.HandleRequest，路径: %s", path)

	// 构建完整的请求路径（包括查询参数）
	requestPath := path
	if c.Request.URL.RawQuery != "" {
		requestPath += "?" + c.Request.URL.RawQuery
	}

	response, err := proxyManager.HandleRequest(requestPath, c.Request.Header)

	logger.Info("proxyManager.HandleRequest返回，错误: %v", err)
	if err != nil {
		logger.Error("请求处理失败: %s -> %v", fullURL, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":     err.Error(),
			"url":       fullURL,
			"timestamp": time.Now().Format(time.RFC3339),
		})
		return
	}

	// 设置响应头
	c.Header("Content-Type", response.ContentType)

	// 发送响应
	if response.IsImage {
		c.Data(http.StatusOK, response.ContentType, response.Data.([]byte))
		logger.Success("响应已发送: %s (图片, %d字节, %dms)", fullURL, len(response.Data.([]byte)), response.ResponseTime)
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
		cacheItem := &cache.CacheItem{
			Data:         response.Data,
			ContentType:  response.ContentType,
			CreatedAt:    time.Now(),
			ExpireAt:     time.Now().Add(cacheManager.GetConfig().DiskCacheTTL),
			LastAccessed: time.Now(),
		}

		// 保存到内存缓存
		cacheManager.GetMemoryCache().Set(cacheKey, cacheItem, response.ContentType)
		logger.CacheInfo("内存缓存写入: %s", fullURL)

		// 保存到磁盘缓存
		if err := cacheManager.GetDiskCache().Set(cacheKey, cacheItem, response.ContentType); err != nil {
			logger.Error("保存磁盘缓存失败: %v", err)
		} else {
			logger.CacheInfo("磁盘缓存写入: %s", fullURL)
		}
	}
}
