package main

import (
    "net/http"
    "time"
    "os"
    "strings"
    "sync"
    "log"
)

type Upstream struct {
    URL     string
    Healthy bool
    Weight  float64
    mu      sync.Mutex
}

func InitUpstreams() []Upstream {
    upstreamURLs := strings.Split(os.Getenv("UPSTREAM_SERVERS"), ",")
    upstreams := make([]Upstream, len(upstreamURLs))
    for i, url := range upstreamURLs {
        upstreams[i] = Upstream{URL: url, Healthy: true, Weight: 1.0}
        go HealthCheck(&upstreams[i])
    }
    return upstreams
}

func (u *Upstream) SetHealthy(healthy bool) {
    u.mu.Lock()
    defer u.mu.Unlock()
    u.Healthy = healthy
}

func (u *Upstream) IsHealthy() bool {
    u.mu.Lock()
    defer u.mu.Unlock()
    return u.Healthy
}

func HealthCheck(upstream *Upstream) {
    for {
        resp, err := http.Get(upstream.URL)
        if err != nil || resp.StatusCode != http.StatusOK {
            upstream.SetHealthy(false)
            log.Printf("Upstream %s is unhealthy", upstream.URL)
        } else {
            upstream.SetHealthy(true)
            log.Printf("Upstream %s is healthy", upstream.URL)
        }
        time.Sleep(30 * time.Second)
    }
}