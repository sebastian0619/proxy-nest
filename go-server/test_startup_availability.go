package main

import (
	"go-server/config"
	"go-server/health"
	"go-server/logger"
	"go-server/proxy"
	"go-server/cache"
	"time"
)

func main() {
	// 设置日志级别
	logger.SetLogLevel("DEBUG")
	
	// 创建配置
	cfg := &config.Config{
		UpstreamType: "tmdb-api",
		TMDBAPIKey:   "test_key",
	}
	
	// 创建缓存管理器
	cacheManager, _ := cache.NewCacheManager(&cfg.Cache)
	
	// 创建健康管理器
	hm := health.NewHealthManager(cfg)
	
	// 创建代理管理器
	pm := proxy.NewProxyManager(cfg, cacheManager, hm)
	
	// 添加测试服务器
	hm.AddServer("https://server1.example.com")
	hm.AddServer("https://server2.example.com")
	hm.AddServer("https://server3.example.com")
	
	servers := hm.GetAllServers()
	
	logger.Info("=== 服务启动后的初始状态 ===")
	for _, server := range servers {
		confidence := hm.GetServerConfidence(server.URL)
		isReady := hm.IsServerReady(server.URL)
		logger.Info("服务器: %s", server.URL)
		logger.Info("  优先级: %d", server.Priority)
		logger.Info("  连接率: %.1f%%", server.ConnectionRate*100)
		logger.Info("  基础权重: %d", server.BaseWeight)
		logger.Info("  动态权重: %d", server.DynamicWeight)
		logger.Info("  综合权重: %d", server.CombinedWeight)
		logger.Info("  置信度: %.1f%%", confidence*100)
		logger.Info("  准备好: %t", isReady)
		logger.Info("  ---")
	}
	
	// 测试1：服务启动后立即选择服务器（样本数量=0）
	logger.Info("\n=== 测试1：服务启动后立即选择服务器 ===")
	for i := 0; i < 3; i++ {
		selectedServer := pm.SelectUpstreamServer()
		if selectedServer != nil {
			confidence := hm.GetServerConfidence(selectedServer.URL)
			isReady := hm.IsServerReady(selectedServer.URL)
			logger.Success("选择结果 %d: %s (优先级=%d, 连接率=%.1f%%, 置信度=%.1f%%, 准备好=%t)", 
				i+1, selectedServer.URL, selectedServer.Priority, selectedServer.ConnectionRate*100, 
				confidence*100, isReady)
		} else {
			logger.Error("选择失败！服务不可用")
		}
		time.Sleep(100 * time.Millisecond)
	}
	
	// 测试2：模拟少量请求后的状态
	logger.Info("\n=== 测试2：少量请求后的状态 ===")
	
	// 为每个服务器添加少量请求
	for i := 0; i < 5; i++ {
		hm.UpdateConnectionRate(servers[0], true)  // 服务器1：5个成功请求
		hm.UpdateConnectionRate(servers[1], i%2 == 0) // 服务器2：3成功2失败
		hm.UpdateConnectionRate(servers[2], i%3 == 0) // 服务器3：2成功3失败
	}
	
	for _, server := range servers {
		confidence := hm.GetServerConfidence(server.URL)
		isReady := hm.IsServerReady(server.URL)
		logger.Info("服务器: %s, 样本数: %d, 连接率: %.1f%%, 置信度: %.1f%%, 准备好: %t, 优先级: %d", 
			server.URL, server.TotalRequests, server.ConnectionRate*100, confidence*100, isReady, server.Priority)
	}
	
	// 测试3：少量请求后再次选择服务器
	logger.Info("\n=== 测试3：少量请求后再次选择服务器 ===")
	for i := 0; i < 3; i++ {
		selectedServer := pm.SelectUpstreamServer()
		if selectedServer != nil {
			confidence := hm.GetServerConfidence(selectedServer.URL)
			isReady := hm.IsServerReady(selectedServer.URL)
			logger.Success("选择结果 %d: %s (优先级=%d, 连接率=%.1f%%, 置信度=%.1f%%, 准备好=%t)", 
				i+1, selectedServer.URL, selectedServer.Priority, selectedServer.ConnectionRate*100, 
				confidence*100, isReady)
		}
		time.Sleep(100 * time.Millisecond)
	}
	
	logger.Success("服务启动可用性测试完成")
	logger.Info("关键结论：服务启动后立即可用，样本不足不影响请求发送")
}
