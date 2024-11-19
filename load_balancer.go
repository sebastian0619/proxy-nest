package main

import (
    "math/rand"
    "sync"
    "time"
)

type Upstream struct {
    URL     string
    Healthy bool
    Weight  float64
    EWMA    float64
    LastResponseTime time.Duration
}

type LoadBalancer struct {
    Upstreams []Upstream
    mu        sync.Mutex
    alpha     float64
}

func NewLoadBalancer(upstreams []Upstream, alpha float64) *LoadBalancer {
    return &LoadBalancer{Upstreams: upstreams, alpha: alpha}
}

func (lb *LoadBalancer) UpdateEWMA(upstream *Upstream, responseTime time.Duration) {
    lb.mu.Lock()
    defer lb.mu.Unlock()

    if upstream.EWMA == 0 {
        upstream.EWMA = float64(responseTime)
    } else {
        upstream.EWMA = lb.alpha*float64(responseTime) + (1-lb.alpha)*upstream.EWMA
    }
    upstream.LastResponseTime = responseTime
}

func (lb *LoadBalancer) SelectUpstream() *Upstream {
    lb.mu.Lock()
    defer lb.mu.Unlock()

    var selected *Upstream
    minEWMA := float64(1<<63 - 1) // Max float64 value

    for i := range lb.Upstreams {
        u := &lb.Upstreams[i]
        if u.Healthy && u.EWMA < minEWMA {
            selected = u
            minEWMA = u.EWMA
        }
    }

    if selected == nil {
        return nil
    }

    return selected
}