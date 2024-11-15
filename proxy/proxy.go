package proxy

import (
	"io"
	"net/http"
	"time"
	"tmdb-go-proxy-nest/metrics"
	"tmdb-go-proxy-nest/upstream"
)

func HandleProxyRequest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// 获取服务器列表和权重列表
	servers, weights := upstream.GetServersAndWeights()

	// 获取随机加权的服务器URL
	serverURL, err := upstream.GetWeightedRandomServer(servers, weights)
	if err != nil {
		metrics.IncrementErrorCount() // 增加错误计数
		http.Error(w, "没有可用的上游服务器", http.StatusServiceUnavailable)
		return
	}

	resp, err := http.Get(serverURL + r.URL.Path)
	if err != nil {
		metrics.IncrementErrorCount() // 增加错误计数
		http.Error(w, "上游服务器请求失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 记录响应时间
	duration := time.Since(start)
	metrics.RecordResponseTime(serverURL, duration)

	// 复制响应
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
