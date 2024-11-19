package main

import (
    "fmt"
    "io"
    "net/http"
    "os"
    "time"
    "log"
)

func ValidateContentType(response *http.Response, upstreamType string) bool {
    contentType := response.Header.Get("Content-Type")
    switch upstreamType {
    case "tmdb-api":
        return contentType == "application/json"
    case "tmdb-image":
        return contentType == "image/jpeg" || contentType == "image/png"
    default:
        return true
    }
}

func main() {
    config := LoadConfig()
    upstreams := InitUpstreams()
    lb := NewLoadBalancer(upstreams, config.AlphaInitial)

    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        upstream := lb.SelectUpstream()
        if upstream == nil {
            http.Error(w, "No healthy upstreams available", http.StatusServiceUnavailable)
            return
        }

        start := time.Now()
        resp, err := http.Get(upstream.URL)
        if err != nil {
            http.Error(w, "Error contacting upstream", http.StatusBadGateway)
            log.Printf("Error contacting upstream %s: %v", upstream.URL, err)
            return
        }
        defer resp.Body.Close()

        if !ValidateContentType(resp, config.UpstreamType) {
            http.Error(w, "Invalid content type", http.StatusBadRequest)
            log.Printf("Invalid content type from upstream %s: %s", upstream.URL, resp.Header.Get("Content-Type"))
            return
        }

        // Update EWMA with the response time
        responseTime := time.Since(start)
        lb.UpdateEWMA(upstream, responseTime)

        // Copy response to client
        w.WriteHeader(resp.StatusCode)
        w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
        if _, err := io.Copy(w, resp.Body); err != nil {
            log.Printf("Error copying response body: %v", err)
        }
    })

    fmt.Println("Server is running on port 6637...")
    if err := http.ListenAndServe(":6637", nil); err != nil {
        log.Fatalf("Server failed to start: %v", err)
    }
}