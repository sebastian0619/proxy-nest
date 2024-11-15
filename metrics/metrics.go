package metrics

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type Metrics struct {
	RequestCount   atomic.Int64
	ErrorCount     atomic.Int64
	CacheHits      atomic.Int64
	CacheMisses    atomic.Int64
	LocalCacheHits atomic.Int64
	RedisCacheHits atomic.Int64
	ResponseTimes  sync.Map // URL -> []time.Duration
}

var metrics = &Metrics{}

func LogInfo(message string) {
	log.Printf("[信息] %s", message)
}

func LogError(message string) {
	log.Printf("[错误] %s", message)
}

func LogDebug(message string) {
	log.Printf("[调试] %s", message)
}

// 更新请求计数
func IncrementRequestCount() {
	metrics.RequestCount.Add(1)
}

// 更新错误计数
func IncrementErrorCount() {
	metrics.ErrorCount.Add(1)
}

// 更新缓存命中计数
func IncrementCacheHit() {
	metrics.CacheHits.Add(1)
}

// 更新缓存未命中计数
func IncrementCacheMiss() {
	metrics.CacheMisses.Add(1)
}

// 更新本地缓存命中计数
func IncrementLocalCacheHit() {
	metrics.LocalCacheHits.Add(1)
}

// 更新Redis缓存命中计数
func IncrementRedisCacheHit() {
	metrics.RedisCacheHits.Add(1)
}

// 记录响应时间
func RecordResponseTime(url string, duration time.Duration) {
	if durations, ok := metrics.ResponseTimes.Load(url); ok {
		metrics.ResponseTimes.Store(url, append(durations.([]time.Duration), duration))
	} else {
		metrics.ResponseTimes.Store(url, []time.Duration{duration})
	}
}

// 计算平均响应时间
func CalculateAverageResponseTime(url string) time.Duration {
	if durations, ok := metrics.ResponseTimes.Load(url); ok {
		total := time.Duration(0)
		for _, d := range durations.([]time.Duration) {
			total += d
		}
		return total / time.Duration(len(durations.([]time.Duration)))
	}
	return 0
}

// 打印当前指标
func PrintMetrics() {
	log.Printf("请求总数: %d, 错误数: %d, 缓存命中: %d, 缓存未命中: %d, 本地缓存命中: %d, Redis缓存命中: %d",
		metrics.RequestCount.Load(),
		metrics.ErrorCount.Load(),
		metrics.CacheHits.Load(),
		metrics.CacheMisses.Load(),
		metrics.LocalCacheHits.Load(),
		metrics.RedisCacheHits.Load(),
	)
}
