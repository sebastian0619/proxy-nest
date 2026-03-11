package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"main/cache"
	"main/config"
	"main/health"
	"main/logger"
	"main/proxy"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// 记录启动时间
	startTime := time.Now()

	// 加载.env文件（如果存在）
	// 注意：.env文件中的变量会覆盖系统环境变量
	// 尝试从多个位置加载.env文件
	envFiles := []string{".env", "go-server/.env", "../.env"}
	loaded := false
	for _, envFile := range envFiles {
		if err := godotenv.Load(envFile); err == nil {
			fmt.Printf("[INFO] 成功加载.env文件: %s\n", envFile)
			// 验证API_KEY是否被加载
			if apiKey := os.Getenv("API_KEY"); apiKey != "" {
				fmt.Printf("[INFO] API_KEY已从.env文件加载（长度: %d字符）\n", len(apiKey))
			} else {
				fmt.Printf("[WARN] .env文件已加载，但API_KEY未设置\n")
			}
			loaded = true
			break
		}
	}
	if !loaded {
		fmt.Printf("[DEBUG] 未找到.env文件，使用系统环境变量\n")
		// 检查系统环境变量中是否有API_KEY
		if apiKey := os.Getenv("API_KEY"); apiKey != "" {
			fmt.Printf("[INFO] API_KEY已从系统环境变量加载（长度: %d字符）\n", len(apiKey))
		} else {
			fmt.Printf("[WARN] 未设置API_KEY环境变量，管理API将不受保护\n")
		}
	}

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
	
	// 检查是否清除健康数据
	if os.Getenv("CLEAR_HEALTH_DATA") == "true" {
		logger.Info("检测到CLEAR_HEALTH_DATA=true，清除健康数据")
		healthManager.ClearHealthData()
		
		// 清除健康数据后，将环境变量重置为false，避免下次重启时再次清除
		logger.Info("健康数据已清除，环境变量已重置为false")
		os.Setenv("CLEAR_HEALTH_DATA", "false")
	}
	
	healthManager.StartHealthCheck()

	// 初始化代理管理器
	proxyManager := proxy.NewProxyManager(cfg, cacheManager, healthManager)

	// 启动主动探测：识别所有上游服务器中的 proxy-nest 节点
	// 启动时立即探测一次，之后每隔健康检查间隔探测一次
	proxyManager.StartUpstreamProxyProbing(cfg.HealthCheckInterval)

	// 设置Gin模式
	gin.SetMode(gin.ReleaseMode)

	// 创建Gin路由
	router := gin.New()

	// 添加中间件
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// 设置路由
	setupRoutes(router, proxyManager, cacheManager, healthManager, cfg, startTime)

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
	}

	for _, skipPath := range skipPaths {
		if path == skipPath || strings.HasPrefix(path, skipPath) {
			return true
		}
	}



	return false
}

// shouldSkipRequestWithQuery 判断是否应该跳过某些请求（包含查询参数）
func shouldSkipRequestWithQuery(path string, query string) bool {
	// 首先检查路径
	if shouldSkipRequest(path) {
		return true
	}

	// 不跳过任何其他请求，让它们进入代理处理流程
	// 健康检查请求会在 handleProxyRequest 中被识别和处理

	return false
}

// apiKeyAuth API密钥验证中间件
func apiKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 白名单端点 - 无需身份验证
		whitelist := []string{
			"/health",   // 健康检查
			"/status",   // 服务器状态
			"/stats",    // 统计信息
			"/upstream", // 上游代理状态
			// 注意：/cache/clear 已移除白名单，现在需要API Key验证
		}

		// 检查是否在白名单中
		requestPath := c.Request.URL.Path
		for _, path := range whitelist {
			if requestPath == path {
				c.Next()
				return
			}
		}

		// 检查API密钥
		apiKey := c.GetHeader("X-API-Key")
		expectedKey := os.Getenv("API_KEY")

		// 如果设置了API_KEY，则需要验证；如果未设置，则警告但允许访问
		if expectedKey != "" && apiKey != expectedKey {
			// 提供更友好的错误信息（中文）
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":     "需要API Key验证",
				"message":   "请设置API_KEY环境变量，并在请求头中提供X-API-Key",
				"endpoint":  requestPath,
				"timestamp": time.Now().Format(time.RFC3339),
				"note":      "API Key只能通过环境变量API_KEY配置，无法在UI中设置",
			})
			c.Abort()
			return
		}

		// 如果没有设置API_KEY，记录警告
		if expectedKey == "" {
			logger.Warn("API访问未受保护: %s (建议设置API_KEY环境变量)", requestPath)
		}

		c.Next()
	}
}

// validateCacheClearRequest 验证缓存清理请求
func validateCacheClearRequest(c *gin.Context) bool {
	cacheType := c.Query("type")
	validTypes := []string{"", "memory", "l2", "disk", "all"}

	// 验证缓存类型参数
	isValid := false
	for _, validType := range validTypes {
		if cacheType == validType {
			isValid = true
			break
		}
	}

	if !isValid {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":     "Invalid cache type",
			"message":   "Supported types: memory, l2, disk, all",
			"provided":  cacheType,
			"timestamp": time.Now().Format(time.RFC3339),
		})
		return false
	}

	// 清除所有缓存时需要确认
	if cacheType == "" || cacheType == "all" {
		confirm := c.Query("confirm")
		if confirm != "yes" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":     "Confirmation required",
				"message":   "Clearing all caches requires confirmation. Add ?confirm=yes",
				"cache_type": cacheType,
				"timestamp":  time.Now().Format(time.RFC3339),
			})
			return false
		}
	}

	return true
}

// clearLocalCache 执行本地缓存清理，返回结果和 HTTP 状态码
func clearLocalCache(cacheType string, cacheManager *cache.CacheManager) (gin.H, int) {
	switch cacheType {
	case "memory":
		cacheManager.GetMemoryCache().Clear()
		return gin.H{"message": "内存缓存已清除", "type": "memory"}, http.StatusOK

	case "l2":
		backendName := "磁盘"
		if cacheManager.GetConfig().UseRedis {
			backendName = "Redis"
		}
		if err := cacheManager.ClearL2Cache(); err != nil {
			return gin.H{"error": fmt.Sprintf("清除%s缓存失败", backendName), "message": err.Error()}, http.StatusInternalServerError
		}
		return gin.H{"message": fmt.Sprintf("%s缓存已清除", backendName), "type": "l2", "backend": backendName}, http.StatusOK

	case "disk":
		if cacheManager.GetConfig().UseRedis {
			return gin.H{"error": "当前使用Redis缓存，请使用 type=l2"}, http.StatusBadRequest
		}
		if err := cacheManager.ClearL2Cache(); err != nil {
			return gin.H{"error": "清除磁盘缓存失败", "message": err.Error()}, http.StatusInternalServerError
		}
		return gin.H{"message": "磁盘缓存已清除", "type": "disk"}, http.StatusOK

	default:
		cacheManager.GetMemoryCache().Clear()
		backendName := "磁盘"
		if cacheManager.GetConfig().UseRedis {
			backendName = "Redis"
		}
		if err := cacheManager.ClearL2Cache(); err != nil {
			return gin.H{"error": fmt.Sprintf("清除%s缓存失败", backendName), "message": err.Error()}, http.StatusInternalServerError
		}
		return gin.H{"message": "所有缓存已清除", "type": "all"}, http.StatusOK
	}
}

// setupRoutes 设置路由
func setupRoutes(router *gin.Engine, proxyManager *proxy.ProxyManager, cacheManager *cache.CacheManager, healthManager *health.HealthManager, cfg *config.Config, startTime time.Time) {
	// ===============================================
	// Web UI 路由 - 不需要API密钥验证
	// ===============================================
	router.GET("/ui", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, getWebUIHTML())
	})

	router.GET("/ui/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, getWebUIHTML())
	})

	// 🔒 API安全中间件 - 保护敏感端点
	// router.Use(apiKeyAuth()) // 暂时注释掉以测试路由

	// 创建API路由组 - 所有管理API都放在/mapi路径下
	apiGroup := router.Group("/mapi")
	// 为API组应用安全中间件
	apiGroup.Use(apiKeyAuth())

	// 创建公开路由组 - 不需要API Key的端点
	publicGroup := router.Group("/api")

	// API Key状态检查端点（不需要API Key验证）
	router.GET("/mapi/api-key-status", func(c *gin.Context) {
		expectedKey := os.Getenv("API_KEY")
		hasApiKey := expectedKey != ""
		var message string
		if hasApiKey {
			message = "API Key已从环境变量配置，管理API需要API Key验证"
		} else {
			message = "API Key未设置，管理API可直接访问"
		}
		c.JSON(http.StatusOK, gin.H{
			"api_key_required": hasApiKey,
			"api_key_set":      hasApiKey,
			"message":          message,
			"note":             "API Key只能通过环境变量API_KEY配置，无法在UI中设置",
			"timestamp":        time.Now().Format(time.RFC3339),
		})
	})

	// 健康检查端点 - 公开端点，不需要API Key
	publicGroup.GET("/health", func(c *gin.Context) {
		logger.Info("Health check endpoint called")
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})

	// 健康检查端点 - 管理端点（需要API Key）
	apiGroup.GET("/health", func(c *gin.Context) {
		logger.Info("Health check endpoint called")
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})

	// 统计信息端点 - 公开端点，不需要API Key
	publicGroup.GET("/stats", func(c *gin.Context) {
		// 获取查询参数
		serverURL := c.Query("server")

		if serverURL != "" {
			// 查看指定服务器的统计信息
			stats := healthManager.GetServerStatistics(serverURL)
			// 将connection_rate转换为百分比
			if connectionRate, exists := stats["connection_rate"]; exists {
				if rate, ok := connectionRate.(float64); ok {
					stats["connection_rate"] = fmt.Sprintf("%.2f%%", rate*100)
				}
			}
			c.JSON(http.StatusOK, stats)
		} else {
			// 查看所有服务器的统计信息
			// 同时输出到控制台和返回HTTP响应
			healthManager.PrintServerStatistics()

			// 获取所有服务器的统计信息并返回
			allStats := healthManager.GetAllServersStatistics()
			
			// 将connection_rate转换为百分比
			for _, stats := range allStats {
				if connectionRate, exists := stats["connection_rate"]; exists {
					if rate, ok := connectionRate.(float64); ok {
						stats["connection_rate"] = fmt.Sprintf("%.2f%%", rate*100)
					}
				}
			}
			
			c.JSON(http.StatusOK, gin.H{
				"message": "统计信息已输出到控制台",
				"servers": allStats,
				"endpoints": gin.H{
					"all_stats":    "/api/stats",
					"server_stats": "/api/stats?server=<server_url>",
					"beautify":     "/api/stats/beautify",
				},
			})
		}
	})

	// 统计信息端点 - 管理端点（需要API Key）
	apiGroup.GET("/stats", func(c *gin.Context) {
		// 获取查询参数
		serverURL := c.Query("server")

		if serverURL != "" {
			// 查看指定服务器的统计信息
			stats := healthManager.GetServerStatistics(serverURL)
			// 将connection_rate转换为百分比
			if connectionRate, exists := stats["connection_rate"]; exists {
				if rate, ok := connectionRate.(float64); ok {
					stats["connection_rate"] = fmt.Sprintf("%.2f%%", rate*100)
				}
			}
			c.JSON(http.StatusOK, stats)
		} else {
			// 查看所有服务器的统计信息
			// 同时输出到控制台和返回HTTP响应
			healthManager.PrintServerStatistics()

			// 获取所有服务器的统计信息并返回
			allStats := healthManager.GetAllServersStatistics()
			
			// 将connection_rate转换为百分比
			for _, stats := range allStats {
				if connectionRate, exists := stats["connection_rate"]; exists {
					if rate, ok := connectionRate.(float64); ok {
						stats["connection_rate"] = fmt.Sprintf("%.2f%%", rate*100)
					}
				}
			}
			
			c.JSON(http.StatusOK, gin.H{
				"message": "统计信息已输出到控制台",
				"servers": allStats,
				"endpoints": gin.H{
					"all_stats":    "/mapi/stats",
					"server_stats": "/mapi/stats?server=<server_url>",
					"beautify":     "/mapi/stats/beautify",
				},
			})
		}
	})

	// 美化统计信息端点（浏览器友好）
	apiGroup.GET("/stats/beautify", func(c *gin.Context) {
		// 获取查询参数
		serverURL := c.Query("server")

		// 通过内部调用 /stats 端点获取数据
		var statsData interface{}
		if serverURL != "" {
			// 获取指定服务器的统计信息
			statsData = healthManager.GetServerStatistics(serverURL)
			// 将connection_rate转换为百分比
			if stats, ok := statsData.(map[string]interface{}); ok {
				if connectionRate, exists := stats["connection_rate"]; exists {
					if rate, ok := connectionRate.(float64); ok {
						stats["connection_rate"] = fmt.Sprintf("%.2f%%", rate*100)
					}
				}
			}
		} else {
			// 获取所有服务器的统计信息
			allStats := healthManager.GetAllServersStatistics()
			logger.Info("获取到所有服务器统计，数量: %d", len(allStats))
			
			// 将connection_rate转换为百分比
			for _, stats := range allStats {
				if connectionRate, exists := stats["connection_rate"]; exists {
					if rate, ok := connectionRate.(float64); ok {
						stats["connection_rate"] = fmt.Sprintf("%.2f%%", rate*100)
					}
				}
			}
			statsData = allStats
		}

		// 生成美化HTML
		var html string
		if serverURL != "" {
			// 单服务器统计
			if stats, ok := statsData.(map[string]interface{}); ok {
				html = generateBeautifiedStatsHTML([]map[string]interface{}{stats}, true)
			}
		} else {
			// 所有服务器统计
			if allStats, ok := statsData.(map[string]map[string]interface{}); ok {
				logger.Info("类型断言成功，开始转换为切片格式")
				// 转换为切片格式
				statsSlice := make([]map[string]interface{}, 0, len(allStats))
				for _, stats := range allStats {
					statsSlice = append(statsSlice, stats)
				}
				logger.Info("转换完成，切片长度: %d", len(statsSlice))
				
				// 临时调试：显示原始数据
				if len(statsSlice) == 0 {
					html = `<html><body><h1>调试信息</h1><p>没有服务器数据</p><pre>` + 
						fmt.Sprintf("%+v", allStats) + `</pre></body></html>`
				} else {
					html = generateBeautifiedStatsHTML(statsSlice, false)
				}
			} else {
				logger.Error("类型断言失败，statsData类型: %T", statsData)
				html = `<html><body><h1>调试信息</h1><p>类型断言失败</p><pre>类型: %T\n数据: %+v</pre></body></html>`
				html = fmt.Sprintf(html, statsData, statsData)
			}
		}

		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, html)
	})

	// 缓存信息端点 - 公开版本（不需要API Key）
	publicGroup.GET("/cache/info", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		// 获取缓存信息
		memoryStats := cacheManager.GetMemoryCache().GetStats()
		l2Stats := cacheManager.GetL2CacheStats()
		
		var l2CacheInfo gin.H
		if cacheManager.GetConfig().UseRedis {
			l2CacheInfo = gin.H{
				"type":         "redis",
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"nodes":        cacheManager.GetConfig().RedisClusterNodes,
				"ttl":          cacheManager.GetConfig().DiskCacheTTL.String(),
				"current_size": l2Stats.CurrentSize,
				"total_files":  l2Stats.TotalFiles,
				"total_size":   l2Stats.TotalSize,
				"pool_size":    cacheManager.GetConfig().RedisPoolSize,
			}
		} else {
			l2CacheInfo = gin.H{
				"type":         "disk",
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"cache_dir":    cacheManager.GetConfig().CacheDir,
				"ttl":          cacheManager.GetConfig().DiskCacheTTL.String(),
				"max_size":     cacheManager.GetConfig().CacheMaxSize,
				"current_size": l2Stats.CurrentSize,
				"total_files":  l2Stats.TotalFiles,
				"total_size":   l2Stats.TotalSize,
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"cache_enabled": cacheManager.GetConfig().CacheEnabled,
			"architecture":  "L1 (Memory) + L2 (Redis/Disk)",
			"memory_cache": gin.H{
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"max_size":     cacheManager.GetConfig().MemoryCacheSize,
				"ttl":          cacheManager.GetConfig().MemoryCacheTTL.String(),
				"current_size": memoryStats.CurrentSize,
				"hits":         memoryStats.Hits,
				"misses":       memoryStats.Misses,
				"hit_rate":     memoryStats.HitRate,
			},
			"l2_cache": l2CacheInfo,
			"endpoints": gin.H{
				"cache_info":   "/api/cache/info",
				"clear_cache":  "/api/cache/clear",
				"clear_memory": "/api/cache/clear?type=memory",
				"clear_l2":     "/api/cache/clear?type=l2",
				"cache_keys":   "/mapi/cache/keys",
				"cache_search": "/mapi/cache/search?q=<query>",
			},
		})
	})

	// 缓存管理端点 - 管理版本（需要API Key，保持向后兼容）
	apiGroup.GET("/cache/info", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		// 获取缓存信息
		memoryStats := cacheManager.GetMemoryCache().GetStats()
		l2Stats := cacheManager.GetL2CacheStats()
		
		var l2CacheInfo gin.H
		if cacheManager.GetConfig().UseRedis {
			l2CacheInfo = gin.H{
				"type":         "redis",
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"nodes":        cacheManager.GetConfig().RedisClusterNodes,
				"ttl":          cacheManager.GetConfig().DiskCacheTTL.String(),
				"current_size": l2Stats.CurrentSize,
				"total_files":  l2Stats.TotalFiles,
				"total_size":   l2Stats.TotalSize,
				"pool_size":    cacheManager.GetConfig().RedisPoolSize,
			}
		} else {
			l2CacheInfo = gin.H{
				"type":         "disk",
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"cache_dir":    cacheManager.GetConfig().CacheDir,
				"ttl":          cacheManager.GetConfig().DiskCacheTTL.String(),
				"max_size":     cacheManager.GetConfig().CacheMaxSize,
				"current_size": l2Stats.CurrentSize,
				"total_files":  l2Stats.TotalFiles,
				"total_size":   l2Stats.TotalSize,
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"cache_enabled": cacheManager.GetConfig().CacheEnabled,
			"architecture":  "L1 (Memory) + L2 (Redis/Disk)",
			"memory_cache": gin.H{
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"max_size":     cacheManager.GetConfig().MemoryCacheSize,
				"ttl":          cacheManager.GetConfig().MemoryCacheTTL.String(),
				"current_size": memoryStats.CurrentSize,
				"hits":         memoryStats.Hits,
				"misses":       memoryStats.Misses,
				"hit_rate":     memoryStats.HitRate,
			},
			"l2_cache": l2CacheInfo,
			"endpoints": gin.H{
				"cache_info":   "/mapi/cache/info",
				"clear_cache":  "/mapi/cache/clear",
				"clear_memory": "/mapi/cache/clear?type=memory",
				"clear_l2":     "/mapi/cache/clear?type=l2",
				"cache_keys":   "/mapi/cache/keys",
				"cache_search": "/mapi/cache/search?q=<query>",
			},
		})
	})

	// 清除缓存端点（管理端点，需要 API Key）
	// 支持 cascade=true 参数，级联清理所有已检测到的上游 proxy-nest 实例
	apiGroup.POST("/cache/clear", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")

		if !validateCacheClearRequest(c) {
			return
		}

		cacheType := c.Query("type")
		cascade := c.Query("cascade") == "true"

		logger.Info("缓存清理操作 - 类型: %s, 级联: %v, IP: %s", cacheType, cascade, c.ClientIP())

		localResult, status := clearLocalCache(cacheType, cacheManager)
		localResult["timestamp"] = time.Now().Format(time.RFC3339)

		if !cascade || status != http.StatusOK {
			c.JSON(status, localResult)
			return
		}

		// 级联清理上游 proxy-nest 实例
		upstreamResults := proxyManager.PerformUpstreamCacheClear(cacheType)
		succeeded := 0
		for _, r := range upstreamResults {
			if r.Success {
				succeeded++
			}
		}
		logger.Info("上游缓存级联清理完成 - 成功: %d/%d", succeeded, len(upstreamResults))

		c.JSON(http.StatusOK, gin.H{
			"local":            localResult,
			"upstream":         upstreamResults,
			"upstream_total":   len(upstreamResults),
			"upstream_success": succeeded,
			"timestamp":        time.Now().Format(time.RFC3339),
		})
	})

	// 获取缓存键列表端点
	apiGroup.GET("/cache/keys", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		// 获取查询参数
		limit := c.DefaultQuery("limit", "100")
		offset := c.DefaultQuery("offset", "0")

		// 获取L2缓存的键列表
		keys, err := cacheManager.GetL2CacheKeys(limit, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":     "获取缓存键列表失败",
				"message":   err.Error(),
				"timestamp": time.Now().Format(time.RFC3339),
			})
			return
		}

		cacheType := "磁盘"
		if cacheManager.GetConfig().UseRedis {
			cacheType = "Redis"
		}

		c.JSON(http.StatusOK, gin.H{
			"keys":     keys,
			"total":    len(keys),
			"limit":    limit,
			"offset":   offset,
			"backend":  cacheType,
			"endpoints": gin.H{
				"cache_keys":   "/mapi/cache/keys",
				"cache_search": "/mapi/cache/search?q=<query>",
			},
		})
	})

	// 搜索缓存端点
	apiGroup.GET("/cache/search", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		query := c.Query("q")
		if query == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":     "缺少搜索查询参数",
				"message":   "请提供查询参数 'q'",
				"timestamp": time.Now().Format(time.RFC3339),
			})
			return
		}

		// 搜索缓存
		results, err := cacheManager.SearchL2Cache(query)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":     "搜索缓存失败",
				"message":   err.Error(),
				"timestamp": time.Now().Format(time.RFC3339),
			})
			return
		}

		cacheType := "磁盘"
		if cacheManager.GetConfig().UseRedis {
			cacheType = "Redis"
		}

		c.JSON(http.StatusOK, gin.H{
			"query":     query,
			"results":   results,
			"total":     len(results),
			"backend":   cacheType,
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})

	// 服务器状态端点 - 公开端点，不需要API Key
	publicGroup.GET("/status", func(c *gin.Context) {
		// 获取系统状态信息
		uptime := time.Since(startTime)

		c.JSON(http.StatusOK, gin.H{
			"status":     "running",
			"uptime":     uptime.String(),
			"start_time": startTime.Format(time.RFC3339),
			"timestamp":  time.Now().Format(time.RFC3339),
			"version":    "tmdb-go-proxy/1.0",
			"endpoints": gin.H{
				"health":      "/api/health",
				"status":      "/api/status",
				"stats":       "/api/stats",
				"cache_info":  "/mapi/cache/info",
				"cache_clear": "/mapi/cache/clear",
				"upstream":    "/api/upstream",
			},
		})
	})

	// 服务器状态端点 - 管理端点（需要API Key）
	apiGroup.GET("/status", func(c *gin.Context) {
		// 获取系统状态信息
		uptime := time.Since(startTime)

		c.JSON(http.StatusOK, gin.H{
			"status":     "running",
			"uptime":     uptime.String(),
			"start_time": startTime.Format(time.RFC3339),
			"timestamp":  time.Now().Format(time.RFC3339),
			"version":    "tmdb-go-proxy/1.0",
			"endpoints": gin.H{
				"health":      "/mapi/health",
				"status":      "/mapi/status",
				"stats":       "/mapi/stats",
				"cache_info":  "/mapi/cache/info",
				"cache_clear": "/mapi/cache/clear",
				"upstream":    "/mapi/upstream",
			},
		})
	})

	// 配置信息端点 - 公开版本（不需要API Key）
	publicGroup.GET("/config", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		c.JSON(http.StatusOK, gin.H{
			"port": cfg.Port,
			"cache": gin.H{
				"enabled":         cfg.Cache.CacheEnabled,
				"cache_dir":       cfg.Cache.CacheDir,
				"memory_ttl":      cfg.Cache.MemoryCacheTTL.String(),
				"disk_ttl":        cfg.Cache.DiskCacheTTL.String(),
				"memory_max_size": cfg.Cache.MemoryCacheSize,
				"disk_max_size":   cfg.Cache.CacheMaxSize,
			},
			"health_check": gin.H{
				"interval":      cfg.HealthCheckInterval.String(),
				"initial_delay": cfg.HealthCheckInitialDelay.String(),
			},
			"endpoints": gin.H{
				"config":     "/api/config",
				"health":     "/api/health",
				"status":     "/api/status",
				"stats":      "/api/stats",
				"cache_info": "/api/cache/info",
			},
		})
	})

	// 配置信息端点 - 管理版本（需要API Key，保持向后兼容）
	apiGroup.GET("/config", func(c *gin.Context) {
		// 返回当前配置信息（不包含敏感信息）
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		c.JSON(http.StatusOK, gin.H{
			"port": cfg.Port,
			"cache": gin.H{
				"enabled":         cfg.Cache.CacheEnabled,
				"cache_dir":       cfg.Cache.CacheDir,
				"memory_ttl":      cfg.Cache.MemoryCacheTTL.String(),
				"disk_ttl":        cfg.Cache.DiskCacheTTL.String(),
				"memory_max_size": cfg.Cache.MemoryCacheSize,
				"disk_max_size":   cfg.Cache.CacheMaxSize,
			},
			"health_check": gin.H{
				"interval":      cfg.HealthCheckInterval.String(),
				"initial_delay": cfg.HealthCheckInitialDelay.String(),
			},
			"endpoints": gin.H{
				"config":     "/mapi/config",
				"health":     "/mapi/health",
				"status":     "/mapi/status",
				"stats":      "/mapi/stats",
				"cache_info": "/mapi/cache/info",
			},
		})
	})

	// 上游代理检测状态端点 - 公开端点，不需要API Key
	publicGroup.GET("/upstream", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")

		// 获取自动检测到的上游服务器
		upstreamInfo := proxyManager.GetUpstreamProxyInfo()

		// 获取手动配置的上游服务器列表
		configuredServers := cfg.UpstreamProxyServers
		configuredServerMap := make(map[string]bool)
		for _, url := range configuredServers {
			configuredServerMap[url] = true
		}

		// 构建响应数据
		upstreamList := make([]gin.H, 0)
		autoDetectedCount := 0
		tmdbProxies := 0
		configuredCount := 0

		// 添加自动检测到的服务器
		for url, info := range upstreamInfo {
			upstreamList = append(upstreamList, gin.H{
				"url":           url,
				"is_tmdb_proxy": info.IsTMDBProxy,
				"version":      info.Version,
				"last_checked": info.LastChecked.Format(time.RFC3339),
				"check_count":  info.CheckCount,
				"source":       "auto_detected", // 标记为自动检测
			})

			autoDetectedCount++
			if info.IsTMDBProxy {
				tmdbProxies++
			}
		}

		// 添加手动配置的服务器（如果不在自动检测列表中）
		for _, url := range configuredServers {
			if _, exists := upstreamInfo[url]; !exists {
				// 手动配置的服务器，尝试检测是否为TMDB代理
				isTMDBProxy := false
				version := ""
				
				// 如果启用了嵌套代理检测，可以尝试快速检测
				if cfg.EnableNestedProxyDetection {
					// 这里可以添加一个快速检测逻辑，但为了不阻塞响应，暂时标记为未知
					// 实际检测会在后续请求中自动进行
				}

				upstreamList = append(upstreamList, gin.H{
					"url":           url,
					"is_tmdb_proxy": isTMDBProxy,
					"version":       version,
					"last_checked": time.Now().Format(time.RFC3339),
					"check_count":  0,
					"source":       "configured", // 标记为手动配置
				})

				configuredCount++
				if isTMDBProxy {
					tmdbProxies++
				}
			} else {
				// 如果已经在自动检测列表中，标记为同时存在
				configuredCount++
			}
		}

		totalProxies := len(upstreamList)

		c.JSON(http.StatusOK, gin.H{
			"enabled":                cfg.EnableNestedProxyDetection,
			"total_upstream_servers": totalProxies,
			"tmdb_proxy_servers":     tmdbProxies,
			"auto_detected_count":    autoDetectedCount,
			"configured_count":       len(configuredServers),
			"upstream_servers":       upstreamList,
			"timestamp":              time.Now().Format(time.RFC3339),
			"note":                   "此端点显示自动检测到的上游代理服务器（通过响应头X-TMDB-Proxy识别）和手动配置的服务器列表。自动检测需要上游服务器返回X-TMDB-Proxy响应头（需要上游服务器也更新到此版本）。",
			"config_note":            "手动配置上游服务器：export UPSTREAM_PROXY_SERVERS=http://server1:6635,http://server2:6635",
			"detection_note":          "自动检测需要上游服务器返回X-TMDB-Proxy响应头。如果上游服务器还没有更新到此版本，将无法自动检测，但可以通过UPSTREAM_PROXY_SERVERS环境变量手动配置。",
			"endpoints": gin.H{
				"upstream_status":    "/api/upstream",
				"upstream_aggregate": "/mapi/upstream/aggregate",
				"cache_clear":        "/api/cache/clear",
			},
		})
	})

	// 清除缓存端点 - 公开版本（不需要 API Key，供跨实例联动使用）
	// 支持 cascade=true 参数，级联清理所有已检测到的上游 proxy-nest 实例
	publicGroup.POST("/cache/clear", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")

		if !validateCacheClearRequest(c) {
			return
		}

		cacheType := c.Query("type")
		cascade := c.Query("cascade") == "true"

		logger.Info("缓存清理操作（公开端点）- 类型: %s, 级联: %v, IP: %s", cacheType, cascade, c.ClientIP())

		localResult, status := clearLocalCache(cacheType, cacheManager)
		localResult["timestamp"] = time.Now().Format(time.RFC3339)

		if !cascade || status != http.StatusOK {
			c.JSON(status, localResult)
			return
		}

		// 级联清理上游 proxy-nest 实例
		upstreamResults := proxyManager.PerformUpstreamCacheClear(cacheType)
		succeeded := 0
		for _, r := range upstreamResults {
			if r.Success {
				succeeded++
			}
		}
		logger.Info("上游缓存级联清理完成 - 成功: %d/%d", succeeded, len(upstreamResults))

		c.JSON(http.StatusOK, gin.H{
			"local":            localResult,
			"upstream":         upstreamResults,
			"upstream_total":   len(upstreamResults),
			"upstream_success": succeeded,
			"timestamp":        time.Now().Format(time.RFC3339),
		})
	})

	// 上游代理检测状态端点 - 管理端点（需要API Key）
	apiGroup.GET("/upstream", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")

		upstreamInfo := proxyManager.GetUpstreamProxyInfo()

		// 构建响应数据
		upstreamList := make([]gin.H, 0, len(upstreamInfo))
		totalProxies := 0
		tmdbProxies := 0

		for url, info := range upstreamInfo {
			upstreamList = append(upstreamList, gin.H{
				"url":          url,
				"is_tmdb_proxy": info.IsTMDBProxy,
				"version":      info.Version,
				"last_checked": info.LastChecked.Format(time.RFC3339),
				"check_count":  info.CheckCount,
			})

			totalProxies++
			if info.IsTMDBProxy {
				tmdbProxies++
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"enabled":              cfg.EnableNestedProxyDetection,
			"total_upstream_servers": totalProxies,
			"tmdb_proxy_servers":   tmdbProxies,
			"upstream_servers":     upstreamList,
			"timestamp":            time.Now().Format(time.RFC3339),
			"endpoints": gin.H{
				"upstream_status": "/mapi/upstream",
				"cache_clear":     "/mapi/cache/clear",
			},
		})
	})

	// 上游服务器聚合API端点 - 汇总所有上游服务器的状态和缓存信息
	apiGroup.GET("/upstream/aggregate", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")

		// 获取自动检测到的上游服务器
		upstreamInfo := proxyManager.GetUpstreamProxyInfo()
		
		// 获取手动配置的上游服务器列表
		configuredServers := cfg.UpstreamProxyServers
		
		// 合并自动检测和手动配置的服务器（去重）
		serverMap := make(map[string]bool)
		upstreamServers := make([]string, 0)
		
		// 添加自动检测到的TMDB代理服务器
		for url, info := range upstreamInfo {
			if info.IsTMDBProxy && !serverMap[url] {
				upstreamServers = append(upstreamServers, url)
				serverMap[url] = true
			}
		}
		
		// 添加手动配置的服务器（如果不在自动检测列表中）
		for _, url := range configuredServers {
			if !serverMap[url] {
				upstreamServers = append(upstreamServers, url)
				serverMap[url] = true
			}
		}
		
		if len(upstreamServers) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"message": "未找到上游服务器（自动检测和手动配置都为空）",
				"servers": []gin.H{},
				"total":   0,
				"auto_detected": len(upstreamInfo),
				"configured": len(configuredServers),
				"timestamp": time.Now().Format(time.RFC3339),
				"note": "提示：系统会自动检测上游TMDB代理服务器，或通过UPSTREAM_PROXY_SERVERS环境变量手动配置",
			})
			return
		}

		// 创建HTTP客户端
		client := &http.Client{
			Timeout: 10 * time.Second,
		}

		// 获取API Key（用于调用上游服务器）
		apiKey := os.Getenv("API_KEY")

		// 并发获取所有上游服务器的信息
		type ServerResult struct {
			URL    string
			Status gin.H
			Cache  gin.H
			Error  string
		}

		results := make([]ServerResult, 0, len(upstreamServers))
		var wg sync.WaitGroup
		var mu sync.Mutex

		for _, serverURL := range upstreamServers {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()

				result := ServerResult{
					URL: url,
				}

				// 获取状态信息
				statusURL := fmt.Sprintf("%s/api/status", url)
				req, err := http.NewRequest("GET", statusURL, nil)
				if err == nil {
					if apiKey != "" {
						req.Header.Set("X-API-Key", apiKey)
					}
					resp, err := client.Do(req)
					if err == nil {
						if resp.StatusCode == http.StatusOK {
							var statusData gin.H
							if err := json.NewDecoder(resp.Body).Decode(&statusData); err == nil {
								result.Status = statusData
							}
						}
						resp.Body.Close()
					}
				}

				// 获取缓存信息
				cacheURL := fmt.Sprintf("%s/mapi/cache/info", url)
				req, err = http.NewRequest("GET", cacheURL, nil)
				if err == nil {
					if apiKey != "" {
						req.Header.Set("X-API-Key", apiKey)
					}
					resp, err := client.Do(req)
					if err == nil {
						if resp.StatusCode == http.StatusOK {
							var cacheData gin.H
							if err := json.NewDecoder(resp.Body).Decode(&cacheData); err == nil {
								result.Cache = cacheData
							}
						}
						resp.Body.Close()
					} else {
						result.Error = err.Error()
					}
				} else {
					result.Error = err.Error()
				}

				mu.Lock()
				results = append(results, result)
				mu.Unlock()
			}(serverURL)
		}

		wg.Wait()

		// 构建响应
		serverList := make([]gin.H, 0, len(results))
		for _, result := range results {
			serverList = append(serverList, gin.H{
				"url":    result.URL,
				"status": result.Status,
				"cache":  result.Cache,
				"error":  result.Error,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"servers":   serverList,
			"total":     len(serverList),
			"timestamp": time.Now().Format(time.RFC3339),
			"endpoints": gin.H{
				"aggregate":         "/mapi/upstream/aggregate",
				"upstream":          "/mapi/upstream",
				"cache_clear":       "/mapi/cache/clear",
				"upstream_clear":    "/mapi/upstream/clear-cache",
			},
		})
	})

	// 上游服务器缓存清理端点 - 仅清理上游，不清理本机
	apiGroup.POST("/upstream/clear-cache", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")

		cacheType := c.DefaultQuery("type", "all")
		validTypes := map[string]bool{"all": true, "memory": true, "l2": true, "disk": true}
		if !validTypes[cacheType] {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":     "无效的缓存类型",
				"message":   "支持的缓存类型: all, memory, l2, disk",
				"timestamp": time.Now().Format(time.RFC3339),
			})
			return
		}

		logger.Info("上游缓存清理操作 - 类型: %s, IP: %s", cacheType, c.ClientIP())

		results := proxyManager.PerformUpstreamCacheClear(cacheType)
		succeeded := 0
		for _, r := range results {
			if r.Success {
				succeeded++
			}
		}

		logger.Info("上游缓存清理完成 - 成功: %d/%d", succeeded, len(results))

		c.JSON(http.StatusOK, gin.H{
			"cache_type":       cacheType,
			"upstream":         results,
			"upstream_total":   len(results),
			"upstream_success": succeeded,
			"timestamp":        time.Now().Format(time.RFC3339),
		})
	})

	// ===============================================
	// 向后兼容性路由 - 保持原有端点可用
	// ===============================================

	// 兼容性路由组 - 不需要API密钥验证
	compatGroup := router.Group("")

	// 健康检查兼容路由
	compatGroup.GET("/health", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"timestamp": time.Now().Format(time.RFC3339),
			"note":      "This is a compatibility endpoint. Please use /mapi/health for new integrations.",
		})
	})

	// 状态信息兼容路由
	compatGroup.GET("/status", func(c *gin.Context) {
		uptime := time.Since(startTime)

		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		c.JSON(http.StatusOK, gin.H{
			"status":     "running",
			"uptime":     uptime.String(),
			"start_time": startTime.Format(time.RFC3339),
			"timestamp":  time.Now().Format(time.RFC3339),
			"version":    "tmdb-go-proxy/1.0",
			"note":       "This is a compatibility endpoint. Please use /mapi/status for new integrations.",
			"endpoints": gin.H{
				"health":      "/mapi/health",
				"status":      "/mapi/status",
				"stats":       "/mapi/stats",
				"cache_info":  "/mapi/cache/info",
				"cache_clear": "/mapi/cache/clear",
				"upstream":    "/mapi/upstream",
			},
		})
	})

	// 配置信息兼容路由
	compatGroup.GET("/config", func(c *gin.Context) {
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")
		c.JSON(http.StatusOK, gin.H{
			"port": cfg.Port,
			"cache": gin.H{
				"enabled":         cfg.Cache.CacheEnabled,
				"cache_dir":       cfg.Cache.CacheDir,
				"memory_ttl":      cfg.Cache.MemoryCacheTTL.String(),
				"disk_ttl":        cfg.Cache.DiskCacheTTL.String(),
				"memory_max_size": cfg.Cache.MemoryCacheSize,
				"disk_max_size":   cfg.Cache.CacheMaxSize,
			},
			"health_check": gin.H{
				"interval":      cfg.HealthCheckInterval.String(),
				"initial_delay": cfg.HealthCheckInitialDelay.String(),
			},
			"note": "This is a compatibility endpoint. Please use /mapi/config for new integrations.",
			"endpoints": gin.H{
				"config":     "/mapi/config",
				"health":     "/mapi/health",
				"status":     "/mapi/status",
				"stats":      "/mapi/stats",
				"cache_info": "/mapi/cache/info",
			},
		})
	})

	// ===============================================
	// 代理请求处理 - 使用NoRoute捕获所有其他请求
	// ===============================================
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

	// 如果缓存被完全禁用，直接处理请求，不检查缓存也不保存缓存
	if !cacheManager.GetConfig().CacheEnabled {
		logger.Info("缓存已禁用，直接处理请求: %s", fullURL)
		response, err := proxyManager.HandleRequest(fullURL, c.Request.Header)
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
		c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
		c.Header("X-TMDB-Proxy-Version", "1.0")

		// 发送响应
		if response.IsImage {
			switch data := response.Data.(type) {
			case []byte:
				c.Data(http.StatusOK, response.ContentType, data)
			case string:
				c.Data(http.StatusOK, response.ContentType, []byte(data))
			default:
				logger.Error("图片响应数据类型错误: %T", response.Data)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "图片数据类型错误"})
				return
			}
		} else if strings.Contains(response.ContentType, "application/json") {
			c.JSON(http.StatusOK, response.Data)
		} else {
			switch data := response.Data.(type) {
			case string:
				c.Data(http.StatusOK, response.ContentType, []byte(data))
			case []byte:
				c.Data(http.StatusOK, response.ContentType, data)
			default:
				c.JSON(http.StatusOK, response.Data)
			}
		}
		logger.Success("响应已发送（无缓存）: %s (%dms)", fullURL, response.ResponseTime)
		return
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

	// 检查缓存（如果缓存被禁用，直接跳过缓存逻辑）
	if cacheManager.GetConfig().CacheEnabled {
		if cachedItem, err := cacheManager.GetFromL2Cache(cacheKey); err == nil && cachedItem != nil {
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
					cacheType := "磁盘"
					if cacheManager.GetConfig().UseRedis {
						cacheType = "Redis"
					}
					logger.CacheHit("%s缓存命中: %s (图片, IsImage: %t)", cacheType, fullURL, cachedItem.IsImage)
				} else if strings.Contains(cachedItem.ContentType, "application/json") {
					c.JSON(http.StatusOK, cachedItem.Data)
					cacheType := "磁盘"
					if cacheManager.GetConfig().UseRedis {
						cacheType = "Redis"
					}
					logger.CacheHit("%s缓存命中: %s (JSON)", cacheType, fullURL)
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
					cacheType := "磁盘"
					if cacheManager.GetConfig().UseRedis {
						cacheType = "Redis"
					}
					logger.CacheHit("%s缓存命中: %s (其他)", cacheType, fullURL)
				}
				return
			} else {
				cacheType := "磁盘"
				if cacheManager.GetConfig().UseRedis {
					cacheType = "Redis"
				}
				logger.Error("%s缓存验证失败: %s", cacheType, fullURL)
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

	// 添加程序标识符到代理响应
	c.Header("X-TMDB-Proxy", "tmdb-go-proxy/1.0")
	c.Header("X-TMDB-Proxy-Version", "1.0")

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

		// 保存到L2缓存（Redis或磁盘缓存）
		if err := cacheManager.SetToL2Cache(cacheKey, cacheItem, response.ContentType); err != nil {
			cacheType := "磁盘"
			if cacheManager.GetConfig().UseRedis {
				cacheType = "Redis"
			}
			logger.Error("保存%s缓存失败: %v", cacheType, err)
		} else {
			cacheType := "磁盘"
			if cacheManager.GetConfig().UseRedis {
				cacheType = "Redis"
			}
			logger.CacheInfo("%s缓存写入: %s (IsImage: %t)", cacheType, fullURL, isImage)
		}
	}
}

// generateBeautifiedStatsHTML 生成美化的统计信息HTML页面
func generateBeautifiedStatsHTML(servers []map[string]interface{}, singleServer bool) string {
	// 计算统计概览
	totalServers := len(servers)
	healthyCount := 0
	unhealthyCount := 0
	
	for _, server := range servers {
		if status, ok := server["status"].(string); ok {
			if status == "healthy" {
				healthyCount++
			} else {
				unhealthyCount++
			}
		}
	}
	
	overallHealthRate := 0.0
	if totalServers > 0 {
		overallHealthRate = float64(healthyCount) / float64(totalServers) * 100
	}

	// 分离健康和不健康的服务器
	var healthyServers []map[string]interface{}
	var unhealthyServers []map[string]interface{}
	
	// 调试信息：打印所有服务器的状态
	logger.Info("开始分类服务器，总数: %d", len(servers))
	
	for _, server := range servers {
		if status, ok := server["status"].(string); ok {
			logger.Info("服务器状态: %s", status)
			if status == "healthy" {
				healthyServers = append(healthyServers, server)
			} else {
				unhealthyServers = append(unhealthyServers, server)
			}
		} else {
			logger.Warn("服务器状态字段类型错误或缺失")
		}
	}
	
	logger.Info("分类完成 - 健康服务器: %d, 不健康服务器: %d", len(healthyServers), len(unhealthyServers))

	// 生成HTML页面
	html := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>TMDB代理服务器统计信息</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
        }
        
        .container {
            max-width: 1600px;
            margin: 0 auto;
            background: rgba(255, 255, 255, 0.95);
            border-radius: 20px;
            box-shadow: 0 20px 40px rgba(0, 0, 0, 0.1);
            overflow: hidden;
        }
        
        .header {
            background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
            color: white;
            padding: 30px;
            text-align: center;
        }
        
        .header h1 {
            font-size: 2.5em;
            margin-bottom: 10px;
            font-weight: 300;
        }
        
        .header p {
            font-size: 1.2em;
            opacity: 0.9;
        }
        
        .overview {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 20px;
            padding: 30px;
            background: #f8f9fa;
        }
        
        .stat-card {
            background: white;
            padding: 25px;
            border-radius: 15px;
            text-align: center;
            box-shadow: 0 5px 15px rgba(0, 0, 0, 0.08);
            transition: transform 0.3s ease;
        }
        
        .stat-card:hover {
            transform: translateY(-5px);
        }
        
        .stat-card.healthy {
            border-left: 5px solid #28a745;
        }
        
        .stat-card.unhealthy {
            border-left: 5px solid #dc3545;
        }
        
        .stat-number {
            font-size: 2.5em;
            font-weight: bold;
            margin-bottom: 10px;
        }
        
        .stat-number.healthy {
            color: #28a745;
        }
        
        .stat-number.unhealthy {
            color: #dc3545;
        }
        
        .stat-number.total {
            color: #007bff;
        }
        
        .stat-label {
            color: #6c757d;
            font-size: 1.1em;
        }
        
        .servers-section {
            padding: 30px;
        }
        
        .section-title {
            font-size: 1.8em;
            margin-bottom: 25px;
            color: #343a40;
            border-bottom: 2px solid #e9ecef;
            padding-bottom: 10px;
        }
        
        .server-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
            gap: 25px;
        }
        
        .server-card {
            background: white;
            border-radius: 15px;
            padding: 25px;
            box-shadow: 0 5px 15px rgba(0, 0, 0, 0.08);
            border-left: 5px solid;
            transition: all 0.3s ease;
        }
        
        .server-card:hover {
            transform: translateY(-3px);
            box-shadow: 0 10px 25px rgba(0, 0, 0, 0.15);
        }
        
        .server-card.healthy {
            border-left-color: #28a745;
        }
        
        .server-card.unhealthy {
            border-left-color: #dc3545;
        }
        
        .server-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 20px;
        }
        
        .server-url {
            font-size: 1.2em;
            font-weight: bold;
            color: #343a40;
            word-break: break-all;
        }
        
        .server-status {
            padding: 8px 16px;
            border-radius: 20px;
            font-size: 0.9em;
            font-weight: bold;
            text-transform: uppercase;
        }
        
        .server-status.healthy {
            background: #d4edda;
            color: #155724;
        }
        
        .server-status.unhealthy {
            background: #f8d7da;
            color: #721c24;
        }
        
        .server-metrics {
            display: grid;
            grid-template-columns: 1fr 1fr;
            gap: 15px;
            margin-bottom: 20px;
        }
        
        .metric {
            background: #f8f9fa;
            padding: 15px;
            border-radius: 10px;
            text-align: center;
        }
        
        .metric-label {
            font-size: 0.9em;
            color: #6c757d;
            margin-bottom: 5px;
        }
        
        .metric-value {
            font-size: 1.3em;
            font-weight: bold;
            color: #343a40;
        }
        
        .metric-value.percentage {
            color: #007bff;
        }
        
        .metric-value.success {
            color: #28a745;
        }
        
        .metric-value.warning {
            color: #ffc107;
        }
        
        .metric-value.danger {
            color: #dc3545;
        }
        
        .server-details {
            background: #f8f9fa;
            padding: 20px;
            border-radius: 10px;
            margin-top: 15px;
        }
        
        .detail-row {
            display: flex;
            justify-content: space-between;
            margin-bottom: 10px;
            padding: 8px 0;
            border-bottom: 1px solid #e9ecef;
        }
        
        .detail-row:last-child {
            border-bottom: none;
            margin-bottom: 0;
        }
        
        .detail-label {
            font-weight: 500;
            color: #495057;
        }
        
        .detail-value {
            color: #6c757d;
        }
        
        .footer {
            background: #343a40;
            color: white;
            text-align: center;
            padding: 20px;
            font-size: 0.9em;
        }
        
        .refresh-btn {
            position: fixed;
            bottom: 30px;
            right: 30px;
            background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
            color: white;
            border: none;
            padding: 15px 25px;
            border-radius: 25px;
            font-size: 1.1em;
            cursor: pointer;
            box-shadow: 0 5px 15px rgba(0, 0, 0, 0.2);
            transition: all 0.3s ease;
        }
        
        .refresh-btn:hover {
            transform: translateY(-2px);
            box-shadow: 0 8px 20px rgba(0, 0, 0, 0.3);
        }
        
        @media (max-width: 768px) {
            .server-grid {
                grid-template-columns: 1fr;
            }
            
            .overview {
                grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
            }
            
            .server-metrics {
                grid-template-columns: 1fr;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🎯 TMDB代理服务器统计信息</h1>
            <p>实时监控上游服务器健康状态和性能指标</p>
        </div>
        
        <div class="overview">
            <div class="stat-card total">
                <div class="stat-number total">` + fmt.Sprintf("%d", totalServers) + `</div>
                <div class="stat-label">总服务器数</div>
            </div>
            <div class="stat-card healthy">
                <div class="stat-number healthy">` + fmt.Sprintf("%d", healthyCount) + `</div>
                <div class="stat-label">健康服务器</div>
            </div>
            <div class="stat-card unhealthy">
                <div class="stat-number unhealthy">` + fmt.Sprintf("%d", unhealthyCount) + `</div>
                <div class="stat-label">不健康服务器</div>
            </div>
            <div class="stat-card">
                <div class="stat-number">` + fmt.Sprintf("%.1f", overallHealthRate) + `%</div>
                <div class="stat-label">整体健康率</div>
            </div>
        </div>`

	// 添加健康服务器部分
	if len(healthyServers) > 0 {
		html += `
        <div class="servers-section">
            <h2 class="section-title">✅ 健康服务器 (` + fmt.Sprintf("%d", len(healthyServers)) + `个)</h2>
            <div class="server-grid">`
		
		html += generateServerCards(healthyServers)
		
		html += `
            </div>
        </div>`
	}

	// 添加不健康服务器部分
	if len(unhealthyServers) > 0 {
		html += `
        <div class="servers-section">
            <h2 class="section-title">❌ 不健康服务器 (` + fmt.Sprintf("%d", len(unhealthyServers)) + `个)</h2>
            <div class="server-grid">`
		
		html += generateServerCards(unhealthyServers)
		
		html += `
            </div>
        </div>`
	}

	html += `
        <div class="footer">
            <p>📱 响应式设计，支持移动设备 | 🎨 美观的现代化界面 | 🔄 数据实时从 /stats 获取</p>
        </div>
    </div>
    
    <button class="refresh-btn" onclick="location.reload()">🔄 刷新数据</button>
    
    <script>
        // 添加一些交互效果
        document.querySelectorAll('.server-card').forEach(card => {
            card.addEventListener('click', function() {
                this.style.transform = 'scale(1.02)';
                setTimeout(() => {
                    this.style.transform = 'scale(1)';
                }, 200);
            });
        });
        
        // 手动刷新按钮功能
        document.querySelector('.refresh-btn').addEventListener('click', function() {
            location.reload();
        });
    </script>
</body>
</html>`

	return html
}

// generateServerCards 生成服务器卡片HTML
func generateServerCards(servers []map[string]interface{}) string {
	var html string
	
	for _, server := range servers {
		// 安全地获取所有字段，提供默认值
		url := getStringValue(server, "url", "未知")
		status := getStringValue(server, "status", "unknown")
		
		// 获取数值字段，提供默认值
		connectionRate := getFloatValue(server, "connection_rate", 0.0)
		confidence := getFloatValue(server, "confidence", 0.0)
		baseWeight := getIntValue(server, "base_weight", 0)
		dynamicWeight := getIntValue(server, "dynamic_weight", 0)
		combinedWeight := getIntValue(server, "combined_weight", 0)
		priority := getIntValue(server, "priority", 0)
		totalRequests := getInt64Value(server, "total_requests", 0)
		successRequests := getInt64Value(server, "success_requests", 0)
		sampleProgress := getStringValue(server, "sample_progress", "0/1000 (0.0%)")
		sampleAvgSpeed := getFloatValue(server, "sample_1000_avg_speed", 0.0)
		lastCheckTime := getStringValue(server, "last_check_time", "从未检查")
		isReady := getBoolValue(server, "is_ready", false)
		lastEWMA := getFloatValue(server, "last_ewma", 0.0)

		// 确定状态样式
		statusClass := "unhealthy"
		if status == "healthy" {
			statusClass = "healthy"
		}

		// 美化参数显示
		connectionRatePercent := fmt.Sprintf("%.2f%%", connectionRate*100)
		confidencePercent := fmt.Sprintf("%.0f%%", confidence*100)
		
		// 美化优先级显示
		priorityText := "低优先级"
		priorityColor := "warning"
		if priority == 2 {
			priorityText = "中优先级"
			priorityColor = "info"
		} else if priority == 3 {
			priorityText = "高优先级"
			priorityColor = "success"
		}
		
		// 美化就绪状态显示
		readyText := "未就绪"
		readyColor := "danger"
		if isReady {
			readyText = "已就绪"
			readyColor = "success"
		}
		
		// 美化连接率显示
		connectionRateClass := "danger"
		if connectionRate >= 0.8 {
			connectionRateClass = "success"
		} else if connectionRate >= 0.5 {
			connectionRateClass = "warning"
		}
		
		// 美化置信度显示
		confidenceClass := "danger"
		if confidence >= 0.8 {
			confidenceClass = "success"
		} else if confidence >= 0.5 {
			confidenceClass = "warning"
		}

		html += `
                <div class="server-card ` + statusClass + `">
                    <div class="server-header">
                        <div class="server-url">` + url + `</div>
                        <div class="server-status ` + statusClass + `">` + status + `</div>
                    </div>
                    
                    <div class="server-metrics">
                        <div class="metric">
                            <div class="metric-label">📊 连接率</div>
                            <div class="metric-value percentage ` + connectionRateClass + `">` + connectionRatePercent + `</div>
                        </div>
                        <div class="metric">
                            <div class="metric-label">🎯 置信度</div>
                            <div class="metric-value ` + confidenceClass + `">` + confidencePercent + `</div>
                        </div>
                        <div class="metric">
                            <div class="metric-label">⭐ 优先级</div>
                            <div class="metric-value ` + priorityColor + `">` + priorityText + `</div>
                        </div>
                        <div class="metric">
                            <div class="metric-label">🔧 就绪状态</div>
                            <div class="metric-value ` + readyColor + `">` + readyText + `</div>
                        </div>
                    </div>
                    
                    <div class="server-details">
                        <div class="detail-row">
                            <span class="detail-label">⚖️ 基础权重:</span>
                            <span class="detail-value">` + fmt.Sprintf("%d", baseWeight) + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">⚡ 动态权重:</span>
                            <span class="detail-value">` + fmt.Sprintf("%d", dynamicWeight) + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">🎯 综合权重:</span>
                            <span class="detail-value">` + fmt.Sprintf("%d", combinedWeight) + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">✅ 成功请求:</span>
                            <span class="detail-value success">` + fmt.Sprintf("%d", successRequests) + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">📈 总请求:</span>
                            <span class="detail-value">` + fmt.Sprintf("%d", totalRequests) + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">📊 样本进度:</span>
                            <span class="detail-value">` + sampleProgress + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">🚀 1000样本平均速度:</span>
                            <span class="detail-value">` + fmt.Sprintf("%.1fms", sampleAvgSpeed) + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">🕒 最后检查时间:</span>
                            <span class="detail-value">` + lastCheckTime + `</span>
                        </div>
                        <div class="detail-row">
                            <span class="detail-label">📈 最后EWMA:</span>
                            <span class="detail-value">` + fmt.Sprintf("%.2f", lastEWMA) + `</span>
                        </div>
                    </div>
                </div>`
	}
	
	return html
}

// 辅助函数：安全地获取字符串值
func getStringValue(data map[string]interface{}, key string, defaultValue string) string {
	if value, exists := data[key]; exists {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return defaultValue
}

// 辅助函数：安全地获取浮点数值
func getFloatValue(data map[string]interface{}, key string, defaultValue float64) float64 {
	if value, exists := data[key]; exists {
		if f, ok := value.(float64); ok {
			return f
		}
	}
	return defaultValue
}

// 辅助函数：安全地获取整数值
func getIntValue(data map[string]interface{}, key string, defaultValue int) int {
	if value, exists := data[key]; exists {
		if i, ok := value.(int); ok {
			return i
		}
	}
	return defaultValue
}

// 辅助函数：安全地获取int64值
func getInt64Value(data map[string]interface{}, key string, defaultValue int64) int64 {
	if value, exists := data[key]; exists {
		if i, ok := value.(int64); ok {
			return i
		}
	}
	return defaultValue
}

// 辅助函数：安全地获取布尔值
func getBoolValue(data map[string]interface{}, key string, defaultValue bool) bool {
	if value, exists := data[key]; exists {
		if b, ok := value.(bool); ok {
			return b
		}
	}
	return defaultValue
}

// getWebUIHTML 返回Web UI的HTML内容
func getWebUIHTML() string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>TMDB Proxy 管理控制台</title>
    <script src="https://unpkg.com/vue@3/dist/vue.global.js"></script>
    <script src="https://unpkg.com/element-plus@2.4.4/dist/index.full.js"></script>
    <link rel="stylesheet" href="https://unpkg.com/element-plus@2.4.4/dist/index.css">
    <style>
        body { margin: 0; background: #f0f2f5; }
        .app-container { max-width: 1300px; margin: 0 auto; padding: 20px; }
        .header {
            text-align: center; margin-bottom: 20px; padding: 18px 20px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white; border-radius: 10px;
        }
        .header h1 { margin: 0 0 6px; font-size: 22px; }
        .header p { margin: 0; opacity: 0.85; font-size: 13px; }
        .card-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 16px; }
        .stat-row { display: flex; justify-content: space-between; align-items: center; padding: 7px 0; border-bottom: 1px solid #f0f0f0; font-size: 13px; }
        .stat-row:last-child { border-bottom: none; }
        .stat-label { color: #888; flex-shrink: 0; margin-right: 12px; }
        .stat-val { font-family: monospace; word-break: break-all; text-align: right; }
        .result-json { background: #f8f9fa; border: 1px solid #e0e0e0; border-radius: 4px; padding: 10px; margin-top: 10px; font-family: monospace; font-size: 12px; white-space: pre-wrap; max-height: 220px; overflow-y: auto; }
        .refresh-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; flex-wrap: wrap; }
        .hint { font-size: 12px; color: #aaa; }
        .proxy-item { padding: 7px 0; border-bottom: 1px solid #f5f5f5; font-size: 13px; }
        .proxy-item:last-child { border-bottom: none; }
    </style>
</head>
<body>
    <div id="app" class="app-container">
        <div class="header">
            <h1>TMDB Proxy 管理控制台</h1>
            <p v-if="health">
                状态:
                <el-tag :type="health.status === 'healthy' ? 'success' : 'danger'" size="small" style="margin: 0 6px;">{{ health.status }}</el-tag>
                版本: {{ health.version || '-' }}
            </p>
            <p v-else>加载中...</p>
        </div>

        <el-tabs v-model="activeTab">
            <!-- 概览 -->
            <el-tab-pane label="概览" name="overview">
                <div class="card-grid">
                    <el-card>
                        <template #header>
                            <div style="display:flex; justify-content:space-between; align-items:center;">
                                <span>系统状态</span>
                                <el-button size="small" @click="loadHealth" :loading="loading.health">刷新</el-button>
                            </div>
                        </template>
                        <div v-if="health && !health.error">
                            <div class="stat-row" v-for="(v, k) in healthDisplay" :key="k">
                                <span class="stat-label">{{ k }}</span>
                                <span class="stat-val">{{ v }}</span>
                            </div>
                        </div>
                        <div v-else-if="health && health.error" style="color:#f56c6c; font-size:13px;">{{ health.error }}</div>
                        <div v-else style="color:#ccc; text-align:center; padding:20px;">加载中...</div>
                    </el-card>

                    <el-card>
                        <template #header>
                            <div style="display:flex; justify-content:space-between; align-items:center;">
                                <span>上游服务器健康状态</span>
                                <el-button size="small" @click="loadServers" :loading="loading.servers">刷新</el-button>
                            </div>
                        </template>
                        <div v-if="serverList.length === 0" style="color:#ccc; text-align:center; padding:20px; font-size:13px;">加载中...</div>
                        <div v-else>
                            <div style="display:flex; gap:16px; margin-bottom:12px; font-size:13px;">
                                <span>共 <b>{{ serverList.length }}</b> 台</span>
                                <span style="color:#67c23a;">健康 <b>{{ serverList.filter(function(s){ return s.status==='healthy'; }).length }}</b></span>
                                <span style="color:#f56c6c;">异常 <b>{{ serverList.filter(function(s){ return s.status!=='healthy'; }).length }}</b></span>
                            </div>
                            <div v-for="s in serverList" :key="s.url" class="proxy-item">
                                <div style="display:flex; align-items:center; gap:8px;">
                                    <el-tag :type="s.status === 'healthy' ? 'success' : 'danger'" size="small">{{ s.status }}</el-tag>
                                    <span style="font-family:monospace; font-size:12px; flex:1;">{{ s.url }}</span>
                                    <span style="color:#aaa; font-size:11px; white-space:nowrap;">P{{ s.priority }} | {{ s.connection_rate }}</span>
                                </div>
                            </div>
                        </div>
                    </el-card>

                    <el-card>
                        <template #header>
                            <div style="display:flex; justify-content:space-between; align-items:center;">
                                <span>检测到的 Proxy-Nest 节点</span>
                                <el-button size="small" @click="loadUpstreamProxies">刷新</el-button>
                            </div>
                        </template>
                        <div v-if="upstreamProxies.length === 0" style="color:#ccc; text-align:center; padding:20px; font-size:13px;">
                            暂未检测到 proxy-nest 节点
                        </div>
                        <div v-for="p in upstreamProxies" :key="p.url" class="proxy-item">
                            <div style="display:flex; align-items:center; gap:8px; margin-bottom:3px;">
                                <el-tag :type="p.is_tmdb_proxy ? 'success' : 'info'" size="small">
                                    {{ p.is_tmdb_proxy ? 'proxy-nest' : '普通上游' }}
                                </el-tag>
                                <span style="font-family:monospace; font-size:12px;">{{ p.url }}</span>
                                <span v-if="p.version" style="color:#aaa; font-size:11px;">v{{ p.version }}</span>
                            </div>
                            <div style="color:#bbb; font-size:11px;">
                                最后检测: {{ p.last_checked ? new Date(p.last_checked).toLocaleString('zh-CN') : '-' }}
                                &nbsp;|&nbsp; 来源: {{ p.source }}
                            </div>
                        </div>
                    </el-card>
                </div>
            </el-tab-pane>

            <!-- 服务器状态 -->
            <el-tab-pane label="服务器状态" name="servers">
                <el-card>
                    <div class="refresh-bar">
                        <el-button type="primary" size="small" @click="loadServers" :loading="loading.servers">刷新</el-button>
                        <el-switch v-model="autoRefresh" @change="toggleAutoRefresh" active-text="自动刷新 30s"></el-switch>
                        <span v-if="autoRefresh" class="hint">下次刷新: {{ refreshCountdown }}s</span>
                        <span v-if="lastUpdated !== '-'" class="hint">更新于: {{ lastUpdated }}</span>
                    </div>
                    <el-table :data="serverList" size="small" stripe border style="width:100%;">
                        <el-table-column label="服务器" min-width="200">
                            <template #default="{ row }">
                                <span style="font-family:monospace; font-size:12px;">{{ row.url }}</span>
                            </template>
                        </el-table-column>
                        <el-table-column label="状态" width="90" align="center">
                            <template #default="{ row }">
                                <el-tag :type="row.status === 'healthy' ? 'success' : 'danger'" size="small">{{ row.status }}</el-tag>
                            </template>
                        </el-table-column>
                        <el-table-column prop="priority" label="优先级" width="70" align="center"></el-table-column>
                        <el-table-column prop="connection_rate" label="连接率" width="90" align="right"></el-table-column>
                        <el-table-column label="响应(ms)" width="100" align="right">
                            <template #default="{ row }">
                                {{ row.last_ewma > 0 ? Math.round(row.last_ewma) : '-' }}
                            </template>
                        </el-table-column>
                        <el-table-column prop="combined_weight" label="权重" width="70" align="center"></el-table-column>
                        <el-table-column prop="sample_progress" label="样本进度" width="140" align="center"></el-table-column>
                    </el-table>
                </el-card>
            </el-tab-pane>

            <!-- 缓存管理 -->
            <el-tab-pane label="缓存管理" name="cache">
                <div class="card-grid">
                    <el-card>
                        <template #header>
                            <div style="display:flex; justify-content:space-between; align-items:center;">
                                <span>缓存信息</span>
                                <div style="display:flex; align-items:center; gap:8px;">
                                    <el-tag v-if="cacheInfo" :type="cacheInfo.cache_enabled ? 'success' : 'info'" size="small">
                                        {{ cacheInfo.cache_enabled ? '已启用' : '已禁用' }}
                                    </el-tag>
                                    <el-button size="small" @click="loadCacheInfo" :loading="loading.cache">刷新</el-button>
                                </div>
                            </div>
                        </template>
                        <div v-if="cacheInfo && !cacheInfo.error">
                            <div style="font-size:12px; color:#aaa; margin-bottom:10px;">{{ cacheInfo.architecture }}</div>

                            <div style="font-weight:600; font-size:13px; color:#555; margin-bottom:8px;">L1 内存缓存</div>
                            <div class="stat-row">
                                <span class="stat-label">命中率</span>
                                <div style="display:flex; align-items:center; gap:8px;">
                                    <el-progress :percentage="memHitRate" :stroke-width="8" style="width:100px;" />
                                    <span style="font-size:12px; min-width:36px; text-align:right;">{{ memHitRate }}%</span>
                                </div>
                            </div>
                            <div class="stat-row">
                                <span class="stat-label">命中 / 未命中</span>
                                <span class="stat-val">{{ (cacheInfo.memory_cache.hits||0).toLocaleString() }} / {{ (cacheInfo.memory_cache.misses||0).toLocaleString() }}</span>
                            </div>
                            <div class="stat-row">
                                <span class="stat-label">条目数 / 上限</span>
                                <span class="stat-val">{{ cacheInfo.memory_cache.current_size }} / {{ cacheInfo.memory_cache.max_size }}</span>
                            </div>
                            <div class="stat-row">
                                <span class="stat-label">TTL</span>
                                <span class="stat-val">{{ cacheInfo.memory_cache.ttl }}</span>
                            </div>

                            <div style="font-weight:600; font-size:13px; color:#555; margin:14px 0 8px; border-top:1px solid #f0f0f0; padding-top:12px;">
                                L2 {{ cacheInfo.l2_cache.type === 'redis' ? 'Redis' : '磁盘' }}缓存
                                <el-tag size="small" style="margin-left:6px;">{{ cacheInfo.l2_cache.type }}</el-tag>
                            </div>
                            <div class="stat-row">
                                <span class="stat-label">文件 / 条目数</span>
                                <span class="stat-val">{{ (cacheInfo.l2_cache.total_files||0).toLocaleString() }}</span>
                            </div>
                            <div class="stat-row">
                                <span class="stat-label">占用空间</span>
                                <span class="stat-val">{{ formatBytes(cacheInfo.l2_cache.total_size) }}</span>
                            </div>
                            <div class="stat-row" v-if="cacheInfo.l2_cache.max_size">
                                <span class="stat-label">最大条目数</span>
                                <span class="stat-val">{{ cacheInfo.l2_cache.max_size.toLocaleString() }}</span>
                            </div>
                            <div class="stat-row">
                                <span class="stat-label">TTL</span>
                                <span class="stat-val">{{ cacheInfo.l2_cache.ttl }}</span>
                            </div>
                            <div class="stat-row" v-if="cacheInfo.l2_cache.cache_dir">
                                <span class="stat-label">目录</span>
                                <span class="stat-val" style="font-family:monospace; font-size:11px;">{{ cacheInfo.l2_cache.cache_dir }}</span>
                            </div>
                            <div class="stat-row" v-if="cacheInfo.l2_cache.nodes">
                                <span class="stat-label">节点</span>
                                <span class="stat-val" style="font-size:11px;">{{ cacheInfo.l2_cache.nodes }}</span>
                            </div>
                        </div>
                        <div v-else-if="cacheInfo && cacheInfo.error" style="color:#f56c6c; font-size:13px;">{{ cacheInfo.error }}</div>
                        <div v-else style="color:#ccc; text-align:center; padding:20px;">加载中...</div>
                    </el-card>

                    <el-card>
                        <template #header>清除缓存</template>
                        <p style="font-size:12px; color:#888; margin:0 0 8px;">仅本机</p>
                        <div style="display:flex; gap:8px; flex-wrap:wrap;">
                            <el-button type="danger" size="small" @click="clearCache('all', false)" :loading="loading.clearCache">清除全部</el-button>
                            <el-button type="warning" size="small" @click="clearCache('memory', false)" :loading="loading.clearCache">内存</el-button>
                            <el-button type="warning" size="small" @click="clearCache('l2', false)" :loading="loading.clearCache">磁盘</el-button>
                        </div>
                        <p style="font-size:12px; color:#888; margin:14px 0 8px;">本机 + 级联上游</p>
                        <div style="display:flex; gap:8px; flex-wrap:wrap;">
                            <el-button type="danger" size="small" @click="clearCache('all', true)" :loading="loading.clearCache">清除全部</el-button>
                            <el-button type="warning" size="small" @click="clearCache('memory', true)" :loading="loading.clearCache">内存</el-button>
                            <el-button type="warning" size="small" @click="clearCache('l2', true)" :loading="loading.clearCache">磁盘</el-button>
                        </div>
                        <div v-if="clearCacheResult" class="result-json">{{ JSON.stringify(clearCacheResult, null, 2) }}</div>
                    </el-card>
                </div>
            </el-tab-pane>

            <!-- 系统配置 -->
            <el-tab-pane label="系统配置" name="config">
                <el-card>
                    <template #header>
                        <div style="display:flex; justify-content:space-between; align-items:center;">
                            <span>配置参数</span>
                            <el-button size="small" @click="loadSystemConfig" :loading="loading.config">刷新</el-button>
                        </div>
                    </template>
                    <div v-if="systemConfig && !systemConfig.error">
                        <div class="stat-row" v-for="(v, k) in systemConfig" :key="k">
                            <span class="stat-label">{{ k }}</span>
                            <span class="stat-val">{{ typeof v === 'object' ? JSON.stringify(v) : v }}</span>
                        </div>
                    </div>
                    <div v-else-if="systemConfig && systemConfig.error" style="color:#f56c6c; font-size:13px;">{{ systemConfig.error }}</div>
                    <div v-else style="color:#ccc; text-align:center; padding:20px;">点击刷新加载配置</div>
                </el-card>
            </el-tab-pane>
        </el-tabs>
    </div>

    <script>
        const { createApp } = Vue;
        const { ElMessage, ElMessageBox } = ElementPlus;

        createApp({
            data() {
                return {
                    activeTab: 'overview',
                    autoRefresh: false,
                    refreshCountdown: 0,
                    refreshTimer: null,
                    countdownTimer: null,
                    lastUpdated: '-',
                    serverList: [],
                    upstreamProxies: [],
                    health: null,
                    cacheInfo: null,
                    systemConfig: null,
                    clearCacheResult: null,
                    loading: { health: false, servers: false, cache: false, clearCache: false, config: false }
                };
            },
            computed: {
                healthDisplay() {
                    if (!this.health) return {};
                    const skip = ['servers', 'upstream_servers', 'endpoints'];
                    const out = {};
                    for (const k in this.health) {
                        if (skip.indexOf(k) === -1) out[k] = this.health[k];
                    }
                    return out;
                },
                memHitRate() {
                    if (!this.cacheInfo || !this.cacheInfo.memory_cache) return 0;
                    const r = this.cacheInfo.memory_cache.hit_rate;
                    return Math.round((typeof r === 'number' ? r : parseFloat(r) / 100) * 100);
                }
            },
            watch: {
                activeTab(val) {
                    if (val === 'servers') this.loadServers();
                    else if (val === 'cache') this.loadCacheInfo();
                    else if (val === 'config') this.loadSystemConfig();
                }
            },
            methods: {
                async apiRequest(path, opts) {
                    const res = await fetch('/mapi' + path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts || {}));
                    const data = await res.json();
                    if (!res.ok) throw new Error(data.message || data.error || '请求失败');
                    return data;
                },
                async publicRequest(path, opts) {
                    const res = await fetch('/api' + path, Object.assign({ headers: { 'Content-Type': 'application/json' } }, opts || {}));
                    const data = await res.json();
                    if (!res.ok) throw new Error(data.message || data.error || '请求失败');
                    return data;
                },
                async loadHealth() {
                    this.loading.health = true;
                    try {
                        this.health = await this.publicRequest('/health');
                    } catch(e) {
                        this.health = { status: 'error', error: e.message };
                    } finally { this.loading.health = false; }
                },
                async loadServers() {
                    this.loading.servers = true;
                    try {
                        const data = await this.publicRequest('/stats');
                        this.serverList = Object.values(data.servers || {});
                        this.lastUpdated = new Date().toLocaleTimeString('zh-CN');
                    } catch(e) {
                        ElMessage.error('获取服务器状态失败: ' + e.message);
                    } finally { this.loading.servers = false; }
                },
                async loadUpstreamProxies() {
                    try {
                        const data = await this.publicRequest('/upstream');
                        this.upstreamProxies = data.upstream_servers || [];
                    } catch(e) {}
                },
                toggleAutoRefresh(val) {
                    if (val) {
                        this.refreshCountdown = 30;
                        this.refreshTimer = setInterval(() => {
                            this.loadServers();
                            this.refreshCountdown = 30;
                        }, 30000);
                        this.countdownTimer = setInterval(() => {
                            if (this.refreshCountdown > 0) this.refreshCountdown--;
                        }, 1000);
                    } else {
                        clearInterval(this.refreshTimer);
                        clearInterval(this.countdownTimer);
                        this.refreshTimer = null;
                        this.countdownTimer = null;
                    }
                },
                async loadCacheInfo() {
                    this.loading.cache = true;
                    try {
                        this.cacheInfo = await this.apiRequest('/cache/info');
                    } catch(e) {
                        this.cacheInfo = { error: e.message };
                    } finally { this.loading.cache = false; }
                },
                async clearCache(type, cascade) {
                    const scope = cascade ? '本机及所有上游' : '本机';
                    const msg = type === 'all'
                        ? ('确定要清除' + scope + '的所有缓存吗?')
                        : ('确定要清除' + scope + '的 ' + type + ' 缓存吗?');
                    try {
                        await ElMessageBox.confirm(msg, '确认', { confirmButtonText: '确定', cancelButtonText: '取消', type: 'warning' });
                    } catch { return; }
                    this.loading.clearCache = true;
                    try {
                        let q = type === 'all' ? '?type=all&confirm=yes' : '?type=' + type;
                        if (cascade) q += '&cascade=true';
                        const data = await this.apiRequest('/cache/clear' + q, { method: 'POST' });
                        this.clearCacheResult = data;
                        if (cascade && data.upstream !== undefined) {
                            ElMessage.success('本机清除成功，上游 ' + (data.upstream_success || 0) + '/' + (data.upstream_total || 0) + ' 成功');
                        } else {
                            ElMessage.success('缓存清除成功');
                        }
                        setTimeout(() => { this.loadCacheInfo().catch(function() {}); }, 500);
                    } catch(e) {
                        this.clearCacheResult = { error: e.message };
                        ElMessage.error('清除失败: ' + e.message);
                    } finally { this.loading.clearCache = false; }
                },
                async loadSystemConfig() {
                    this.loading.config = true;
                    try {
                        this.systemConfig = await this.apiRequest('/config');
                    } catch(e) {
                        this.systemConfig = { error: e.message };
                    } finally { this.loading.config = false; }
                },
                formatBytes(bytes) {
                    if (!bytes || bytes === 0) return '0 B';
                    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
                    const i = Math.floor(Math.log(bytes) / Math.log(1024));
                    return (bytes / Math.pow(1024, i)).toFixed(1) + ' ' + units[i];
                }
            },
            mounted() {
                this.loadHealth();
                this.loadUpstreamProxies();
                this.loadServers();
            },
            beforeUnmount() {
                this.toggleAutoRefresh(false);
            }
        }).use(ElementPlus).mount('#app');
    </script>
</body>
</html>`
}
