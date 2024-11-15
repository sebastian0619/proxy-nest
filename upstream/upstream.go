package upstream

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
	"tmdb-go-proxy-nest/cache"
	"tmdb-go-proxy-nest/metrics"
	"errors"
	"math/rand"
)

type Server struct {
	URL              string
	Healthy          bool
	BaseWeight       int
	DynamicWeight    int
	UpstreamType     string
	CustomHealthURL  string
	mutex            sync.RWMutex
}

var upstreamServers []Server

func InitUpstreamServers(servers []string, upstreamType string, customHealthURL string) {
	upstreamServers = make([]Server, len(servers))
	for i, url := range servers {
		upstreamServers[i] = Server{
			URL:             url,
			Healthy:         true,
			BaseWeight:      50,
			UpstreamType:    upstreamType,
			CustomHealthURL: customHealthURL,
		}
	}
	log.Printf("已加载 %d 个上游服务器", len(upstreamServers))
}

func CheckServerHealth(server *Server) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	var healthCheckURI string

	switch server.UpstreamType {
	case "tmdb_api":
		apiKey := os.Getenv("TMDB_API_KEY")
		if apiKey == "" {
			log.Println("TMDB API KEY 未设置")
			return false
		}
		healthCheckURI = fmt.Sprintf("/3/configuration?apikey=%s", apiKey)
	case "tmdb_image":
		imageURL := os.Getenv("TMDB_IMAGE_URL")
		if imageURL == "" {
			log.Println("TMDB IMAGE URL 未设置")
			return false
		}
		healthCheckURI = imageURL
	case "custom":
		if server.CustomHealthURL == "" {
			log.Println("自定义健康检查URL未设置")
			return false
		}
		healthCheckURI = server.CustomHealthURL
	default:
		log.Printf("未知的上游类型: %s", server.UpstreamType)
		return false
	}

	resp, err := client.Get(server.URL + healthCheckURI)
	if err != nil || resp.StatusCode != http.StatusOK {
		metrics.IncrementErrorCount()
		return false
	}
	return true
}

func UpdateServerHealth() {
	ctx := context.Background()
	for i := range upstreamServers {
		server := &upstreamServers[i]
		server.mutex.Lock()
		server.Healthy = CheckServerHealth(server)
		server.mutex.Unlock()
		log.Printf("服务器 %s 健康状态: %v", server.URL, server.Healthy)

		key := fmt.Sprintf("server_health:%s", server.URL)
		err := cache.RedisClient.Set(ctx, key, server.Healthy, 0).Err()
		if err != nil {
			log.Printf("更新Redis健康信息失败: %v", err)
		}
	}
}

func StartHealthCheck(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			UpdateServerHealth()
		}
	}()
}

func GetWeightedRandomServer(servers []string, weights []int) (string, error) {
	if len(servers) != len(weights) || len(servers) == 0 {
		return "", errors.New("服务器列表和权重列表长度不匹配或为空")
	}

	totalWeight := 0
	for _, weight := range weights {
		totalWeight += weight
	}

	rand.Seed(time.Now().UnixNano())
	randomWeight := rand.Intn(totalWeight)

	for i, weight := range weights {
		if randomWeight < weight {
			return servers[i], nil
		}
		randomWeight -= weight
	}

	return "", errors.New("未找到合适的服务器")
}

func GetServersAndWeights() ([]string, []int) {
	var servers []string
	var weights []int

	for _, server := range upstreamServers {
		servers = append(servers, server.URL)
		weights = append(weights, calculateCombinedWeight(&server))
	}

	return servers, weights
}

func calculateCombinedWeight(server *Server) int {
	server.mutex.RLock()
	defer server.mutex.RUnlock()

	weight := server.BaseWeight
	if server.DynamicWeight > 0 {
		weight = (weight + server.DynamicWeight) / 2
	}

	if weight < 1 {
		weight = 1
	} else if weight > 100 {
		weight = 100
	}

	return weight
}
