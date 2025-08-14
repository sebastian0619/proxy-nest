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
	// 记录启动时间
	startTime := time.Now()

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
		"/health",
		"/status",
		"/stats",
		"/config",
		"/cache",
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

// setupRoutes 设置路由
func setupRoutes(router *gin.Engine, proxyManager *proxy.ProxyManager, cacheManager *cache.CacheManager, healthManager *health.HealthManager, cfg *config.Config, startTime time.Time) {
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
					"all_stats":    "/stats",
					"server_stats": "/stats?server=<server_url>",
					"beautify":     "/stats/beautify",
				},
			})
		}
	})

	// 美化统计信息端点（浏览器友好）
	router.GET("/stats/beautify", func(c *gin.Context) {
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
				// 转换为切片格式
				statsSlice := make([]map[string]interface{}, 0, len(allStats))
				for _, stats := range allStats {
					statsSlice = append(statsSlice, stats)
				}
				html = generateBeautifiedStatsHTML(statsSlice, false)
			}
		}

		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, html)
	})

	// 缓存管理端点
	router.GET("/cache/info", func(c *gin.Context) {
		// 获取缓存信息
		memoryStats := cacheManager.GetMemoryCache().GetStats()
		diskStats := cacheManager.GetDiskCache().GetStats()

		c.JSON(http.StatusOK, gin.H{
			"cache_enabled": cacheManager.GetConfig().CacheEnabled,
			"memory_cache": gin.H{
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"max_size":     cacheManager.GetConfig().MemoryCacheSize,
				"ttl":          cacheManager.GetConfig().MemoryCacheTTL.String(),
				"current_size": memoryStats.CurrentSize,
				"hits":         memoryStats.Hits,
				"misses":       memoryStats.Misses,
				"hit_rate":     memoryStats.HitRate,
			},
			"disk_cache": gin.H{
				"enabled":      cacheManager.GetConfig().CacheEnabled,
				"cache_dir":    cacheManager.GetConfig().CacheDir,
				"ttl":          cacheManager.GetConfig().DiskCacheTTL.String(),
				"max_size":     cacheManager.GetConfig().CacheMaxSize,
				"current_size": diskStats.CurrentSize,
				"total_files":  diskStats.TotalFiles,
				"total_size":   diskStats.TotalSize,
			},
			"endpoints": gin.H{
				"cache_info":   "/cache/info",
				"clear_cache":  "/cache/clear",
				"clear_memory": "/cache/clear?type=memory",
				"clear_disk":   "/cache/clear?type=disk",
				"cache_keys":   "/cache/keys",
				"cache_search": "/cache/search?q=<query>",
			},
		})
	})

	// 清除缓存端点
	router.POST("/cache/clear", func(c *gin.Context) {
		// 获取查询参数，决定清除哪种类型的缓存
		cacheType := c.Query("type")

		var result gin.H
		var status int

		switch cacheType {
		case "memory":
			// 只清除内存缓存
			cacheManager.GetMemoryCache().Clear()
			result = gin.H{
				"message":   "内存缓存已清除",
				"type":      "memory",
				"timestamp": time.Now().Format(time.RFC3339),
			}
			status = http.StatusOK
			logger.Info("内存缓存已通过API清除")

		case "disk":
			// 只清除磁盘缓存
			if err := cacheManager.GetDiskCache().Clear(); err != nil {
				result = gin.H{
					"error":     "清除磁盘缓存失败",
					"message":   err.Error(),
					"timestamp": time.Now().Format(time.RFC3339),
				}
				status = http.StatusInternalServerError
				logger.Error("API清除磁盘缓存失败: %v", err)
			} else {
				result = gin.H{
					"message":   "磁盘缓存已清除",
					"type":      "disk",
					"timestamp": time.Now().Format(time.RFC3339),
				}
				status = http.StatusOK
				logger.Info("磁盘缓存已通过API清除")
			}

		default:
			// 清除所有缓存
			cacheManager.GetMemoryCache().Clear()
			if err := cacheManager.GetDiskCache().Clear(); err != nil {
				result = gin.H{
					"error":     "清除缓存失败",
					"message":   err.Error(),
					"timestamp": time.Now().Format(time.RFC3339),
				}
				status = http.StatusInternalServerError
				logger.Error("API清除所有缓存失败: %v", err)
			} else {
				result = gin.H{
					"message":   "所有缓存已清除",
					"type":      "all",
					"timestamp": time.Now().Format(time.RFC3339),
				}
				status = http.StatusOK
				logger.Info("所有缓存已通过API清除")
			}
		}

		c.JSON(status, result)
	})

	// 获取缓存键列表端点
	router.GET("/cache/keys", func(c *gin.Context) {
		// 获取查询参数
		limit := c.DefaultQuery("limit", "100")
		offset := c.DefaultQuery("offset", "0")

		// 获取磁盘缓存的键列表
		keys, err := cacheManager.GetDiskCache().GetKeys(limit, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":     "获取缓存键列表失败",
				"message":   err.Error(),
				"timestamp": time.Now().Format(time.RFC3339),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"keys":   keys,
			"total":  len(keys),
			"limit":  limit,
			"offset": offset,
			"endpoints": gin.H{
				"cache_keys":   "/cache/keys",
				"cache_search": "/cache/search?q=<query>",
			},
		})
	})

	// 搜索缓存端点
	router.GET("/cache/search", func(c *gin.Context) {
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
		results, err := cacheManager.GetDiskCache().Search(query)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":     "搜索缓存失败",
				"message":   err.Error(),
				"timestamp": time.Now().Format(time.RFC3339),
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"query":     query,
			"results":   results,
			"total":     len(results),
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})

	// 服务器状态端点
	router.GET("/status", func(c *gin.Context) {
		// 获取系统状态信息
		uptime := time.Since(startTime)

		c.JSON(http.StatusOK, gin.H{
			"status":     "running",
			"uptime":     uptime.String(),
			"start_time": startTime.Format(time.RFC3339),
			"timestamp":  time.Now().Format(time.RFC3339),
			"version":    "tmdb-go-proxy/1.0",
			"endpoints": gin.H{
				"health":      "/health",
				"status":      "/status",
				"stats":       "/stats",
				"cache_info":  "/cache/info",
				"cache_clear": "/cache/clear",
			},
		})
	})

	// 配置信息端点
	router.GET("/config", func(c *gin.Context) {
		// 返回当前配置信息（不包含敏感信息）
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
				"config":     "/config",
				"health":     "/health",
				"status":     "/status",
				"stats":      "/stats",
				"cache_info": "/cache/info",
			},
		})
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
	
	for _, server := range servers {
		if status, ok := server["status"].(string); ok {
			if status == "healthy" {
				healthyServers = append(healthyServers, server)
			} else {
				unhealthyServers = append(unhealthyServers, server)
			}
		}
	}

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
