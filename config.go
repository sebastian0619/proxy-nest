package main

import (
    "os"
    "strconv"
    "log"
    "strings"
)

type Config struct {
    UpstreamType                string
    UpstreamServers             []string
    WeightUpdateIntervalMinutes int
    BaseWeightMultiplier        int
    DynamicWeightMultiplier     int
    AlphaInitial                float64
    AlphaAdjustmentStep         float64
    RecentRequestLimit          int
    RequestTimeoutMinutes       int
    CacheTTLMinutes             int
    LocalCacheSizeMB            int
    LocalCacheExpirationMinutes int
}

func LoadConfig() Config {
    weightUpdateInterval, err := strconv.Atoi(getEnv("WEIGHT_UPDATE_INTERVAL_MINUTES", "30"))
    if err != nil {
        log.Fatalf("Invalid WEIGHT_UPDATE_INTERVAL_MINUTES: %v", err)
    }

    baseWeightMultiplier, err := strconv.Atoi(getEnv("BASE_WEIGHT_MULTIPLIER", "50"))
    if err != nil {
        log.Fatalf("Invalid BASE_WEIGHT_MULTIPLIER: %v", err)
    }

    dynamicWeightMultiplier, err := strconv.Atoi(getEnv("DYNAMIC_WEIGHT_MULTIPLIER", "50"))
    if err != nil {
        log.Fatalf("Invalid DYNAMIC_WEIGHT_MULTIPLIER: %v", err)
    }

    alphaInitial, err := strconv.ParseFloat(getEnv("ALPHA_INITIAL", "0.5"), 64)
    if err != nil {
        log.Fatalf("Invalid ALPHA_INITIAL: %v", err)
    }

    alphaAdjustmentStep, err := strconv.ParseFloat(getEnv("ALPHA_ADJUSTMENT_STEP", "0.05"), 64)
    if err != nil {
        log.Fatalf("Invalid ALPHA_ADJUSTMENT_STEP: %v", err)
    }

    recentRequestLimit, err := strconv.Atoi(getEnv("RECENT_REQUEST_LIMIT", "10"))
    if err != nil {
        log.Fatalf("Invalid RECENT_REQUEST_LIMIT: %v", err)
    }

    requestTimeoutMinutes, err := strconv.Atoi(getEnv("REQUEST_TIMEOUT_MINUTES", "1"))
    if err != nil {
        log.Fatalf("Invalid REQUEST_TIMEOUT_MINUTES: %v", err)
    }

    cacheTTLMinutes, err := strconv.Atoi(getEnv("CACHE_TTL_MINUTES", "1440"))
    if err != nil {
        log.Fatalf("Invalid CACHE_TTL_MINUTES: %v", err)
    }

    localCacheSizeMB, err := strconv.Atoi(getEnv("LOCAL_CACHE_SIZE_MB", "50"))
    if err != nil {
        log.Fatalf("Invalid LOCAL_CACHE_SIZE_MB: %v", err)
    }

    localCacheExpirationMinutes, err := strconv.Atoi(getEnv("LOCAL_CACHE_EXPIRATION_MINUTES", "5"))
    if err != nil {
        log.Fatalf("Invalid LOCAL_CACHE_EXPIRATION_MINUTES: %v", err)
    }

    return Config{
        UpstreamType:                os.Getenv("UPSTREAM_TYPE"),
        UpstreamServers:             parseCommaSeparatedList(os.Getenv("UPSTREAM_SERVERS")),
        WeightUpdateIntervalMinutes: weightUpdateInterval,
        BaseWeightMultiplier:        baseWeightMultiplier,
        DynamicWeightMultiplier:     dynamicWeightMultiplier,
        AlphaInitial:                alphaInitial,
        AlphaAdjustmentStep:         alphaAdjustmentStep,
        RecentRequestLimit:          recentRequestLimit,
        RequestTimeoutMinutes:       requestTimeoutMinutes,
        CacheTTLMinutes:             cacheTTLMinutes,
        LocalCacheSizeMB:            localCacheSizeMB,
        LocalCacheExpirationMinutes: localCacheExpirationMinutes,
    }
}

func getEnv(key, defaultValue string) string {
    if value, exists := os.LookupEnv(key); exists {
        return value
    }
    return defaultValue
}

func parseCommaSeparatedList(value string) []string {
    if value == "" {
        return []string{}
    }
    return strings.Split(value, ",")
}