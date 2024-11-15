package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"tmdb-go-proxy-nest/cache"
	"tmdb-go-proxy-nest/config"
	"tmdb-go-proxy-nest/metrics"
	"tmdb-go-proxy-nest/proxy"
	"tmdb-go-proxy-nest/upstream"
	"time"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 初始化配置
	options := &redis.Options{
		Addr: "localhost:6379", // 替换为你的 Redis 地址
	}
	cache.InitRedisClient(options)
	cache.InitLocalCache(5*time.Minute, 50*1024*1024)

	// 从环境变量获取上游服务器列表
	upstreamServersEnv := os.Getenv("UPSTREAM_SERVERS")
	if upstreamServersEnv == "" {
		log.Fatal("UPSTREAM_SERVERS 环境变量未设置")
	}

	// 将上游服务器字符串解析为切片
	upstreamServers := strings.Split(upstreamServersEnv, ",")

	// 从环境变量获取上游类型
	upstreamType := os.Getenv("UPSTREAM_TYPE")
	if upstreamType == "" {
		log.Fatal("UPSTREAM_TYPE 环境变量未设置")
	}

	// 初始化上游服务器
	upstream.InitUpstreamServers(upstreamServers, upstreamType, "")

	// 启动定期健康检查
	upstream.StartHealthCheck(5 * time.Minute)

	// 启动HTTP服务
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		metrics.IncrementRequestCount() // 增加请求计数
		proxy.HandleProxyRequest(w, r)
	})
	port := config.GetEnv("PORT", "6637")
	log.Printf("HTTP 服务启动在端口 %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}