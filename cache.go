package main

import (
    "sync"
    "os"
    "io/ioutil"
    "path/filepath"
    "fmt"
)

type Cache struct {
    memoryCache sync.Map
    diskPath    string
}

func NewCache(diskPath string) *Cache {
    return &Cache{diskPath: diskPath}
}

func (c *Cache) Get(key string) (interface{}, bool) {
    // 尝试从内存缓存中获取
    if value, ok := c.memoryCache.Load(key); ok {
        return value, true
    }

    // 尝试从本地硬盘缓存中获取
    filePath := filepath.Join(c.diskPath, key)
    if data, err := ioutil.ReadFile(filePath); err == nil {
        return string(data), true
    }

    return nil, false
}

func (c *Cache) Set(key string, value interface{}) {
    // 更新内存缓存
    c.memoryCache.Store(key, value)

    // 更新本地硬盘缓存
    filePath := filepath.Join(c.diskPath, key)
    if err := ioutil.WriteFile(filePath, []byte(fmt.Sprintf("%v", value)), 0644); err != nil {
        fmt.Println("Error writing to disk cache:", err)
    }
}