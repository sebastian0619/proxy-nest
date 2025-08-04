package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"proxy-nest-go/cache"
	"proxy-nest-go/config"
	"proxy-nest-go/health"
	"proxy-nest-go/logger"
	"proxy-nest-go/proxy"
)

func main() {
	// 加载配置
	cfg := config.LoadConfig()
	
	// 初始化日志
	logPrefix := logger.InitLogger()
	
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

// setupRoutes 设置路由
func setupRoutes(router *gin.Engine, proxyManager *proxy.ProxyManager, cacheManager *cache.CacheManager) {
	// 通用代理路由
	router.Any("/*path", func(c *gin.Context) {
		handleProxyRequest(c, proxyManager, cacheManager)
	})
}

// handleProxyRequest 处理代理请求
func handleProxyRequest(c *gin.Context, proxyManager *proxy.ProxyManager, cacheManager *cache.CacheManager) {
	// 获取请求路径
	path := c.Param("path")
	if path == "" {
		path = "/"
	}
	
	// 生成缓存键
	cacheKey := cache.GetCacheKey(path)
	
	// 检查缓存
	if cacheManager.config.CacheEnabled {
		if cachedItem, err := cacheManager.diskCache.Get(cacheKey); err == nil && cachedItem != nil {
			// 验证缓存内容
			if proxyManager.ValidateResponse(cachedItem.Data, cachedItem.ContentType) {
				c.Header("Content-Type", cachedItem.ContentType)
				c.Data(http.StatusOK, cachedItem.ContentType, cachedItem.Data.([]byte))
				logger.CacheHit("磁盘缓存命中: %s", path)
				return
			}
		}
		
		if cachedItem, exists := cacheManager.memoryCache.Get(cacheKey); exists {
			// 验证缓存内容
			if proxyManager.ValidateResponse(cachedItem.Data, cachedItem.ContentType) {
				c.Header("Content-Type", cachedItem.ContentType)
				c.Data(http.StatusOK, cachedItem.ContentType, cachedItem.Data.([]byte))
				logger.CacheHit("内存缓存命中: %s", path)
				return
			}
		}
		
		logger.CacheMiss("缓存未命中: %s", cacheKey)
	}
	
	// 处理新请求
	response, err := proxyManager.HandleRequest(path, c.Request.Header)
	if err != nil {
		logger.Error("请求处理失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	
	// 设置响应头
	c.Header("Content-Type", response.ContentType)
	
	// 发送响应
	if response.IsImage {
		c.Data(http.StatusOK, response.ContentType, response.Data.([]byte))
	} else {
		c.JSON(http.StatusOK, response.Data)
	}
	
	// 保存缓存
	if cacheManager.config.CacheEnabled {
		cacheItem := &cache.CacheItem{
			Data:        response.Data,
			ContentType: response.ContentType,
			CreatedAt:   time.Now(),
			ExpireAt:    time.Now().Add(cacheManager.config.DiskCacheTTL),
			LastAccessed: time.Now(),
		}
		
		// 保存到内存缓存
		cacheManager.memoryCache.Set(cacheKey, cacheItem, response.ContentType)
		
		// 保存到磁盘缓存
		if err := cacheManager.diskCache.Set(cacheKey, cacheItem, response.ContentType); err != nil {
			logger.Error("保存磁盘缓存失败: %v", err)
		}
	}
} 