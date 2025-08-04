package cache

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2"
	"proxy-nest-go/config"
	"proxy-nest-go/logger"
)

// CacheItem 缓存项
type CacheItem struct {
	Data        interface{} `json:"data"`
	ContentType string      `json:"contentType"`
	CreatedAt   time.Time   `json:"createdAt"`
	ExpireAt    time.Time   `json:"expireAt"`
	LastAccessed time.Time  `json:"lastAccessed"`
}

// CacheMeta 缓存元数据
type CacheMeta struct {
	Key         string    `json:"key"`
	FilePath    string    `json:"filePath"`
	Size        int       `json:"size"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpireAt    time.Time `json:"expireAt"`
	LastAccessed time.Time `json:"lastAccessed"`
}

// DiskCache 磁盘缓存
type DiskCache struct {
	config     *config.CacheConfig
	cacheDir   string
	index      map[string]*CacheMeta
	indexMutex sync.RWMutex
	lock       map[string]bool
	lockMutex  sync.Mutex
}

// MemoryCache 内存缓存
type MemoryCache struct {
	cache *lru.Cache[string, *CacheItem]
}

// CacheManager 缓存管理器
type CacheManager struct {
	diskCache   *DiskCache
	memoryCache *MemoryCache
	config      *config.CacheConfig
}

// NewDiskCache 创建磁盘缓存
func NewDiskCache(cfg *config.CacheConfig) *DiskCache {
	return &DiskCache{
		config:   cfg,
		cacheDir: cfg.CacheDir,
		index:    make(map[string]*CacheMeta),
		lock:     make(map[string]bool),
	}
}

// Init 初始化磁盘缓存
func (dc *DiskCache) Init() error {
	// 创建缓存目录
	if err := os.MkdirAll(dc.cacheDir, 0755); err != nil {
		return fmt.Errorf("创建缓存目录失败: %w", err)
	}

	// 加载索引
	if err := dc.loadIndex(); err != nil {
		logger.Error("加载缓存索引失败: %v", err)
	}

	// 启动清理任务
	go dc.startCleanup()

	logger.Info("磁盘缓存初始化完成")
	return nil
}

// Get 获取缓存
func (dc *DiskCache) Get(key string) (*CacheItem, error) {
	dc.indexMutex.RLock()
	meta, exists := dc.index[key]
	dc.indexMutex.RUnlock()

	if !exists {
		return nil, nil
	}

	// 检查是否过期
	if time.Now().After(meta.ExpireAt) {
		dc.Delete(key)
		return nil, nil
	}

	// 读取文件
	filePath := filepath.Join(dc.cacheDir, key+dc.config.CacheFileExt)
	data, err := os.ReadFile(filePath)
	if err != nil {
		logger.Error("读取缓存文件失败: %v", err)
		dc.Delete(key)
		return nil, nil
	}

	// 解析缓存项
	var item CacheItem
	if err := json.Unmarshal(data, &item); err != nil {
		logger.Error("解析缓存数据失败: %v", err)
		dc.Delete(key)
		return nil, nil
	}

	// 更新最后访问时间
	meta.LastAccessed = time.Now()
	dc.indexMutex.Lock()
	dc.index[key] = meta
	dc.indexMutex.Unlock()

	return &item, nil
}

// Set 设置缓存
func (dc *DiskCache) Set(key string, value *CacheItem, contentType string) error {
	// 等待锁释放
	for {
		dc.lockMutex.Lock()
		if !dc.lock[key] {
			dc.lock[key] = true
			dc.lockMutex.Unlock()
			break
		}
		dc.lockMutex.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	defer func() {
		dc.lockMutex.Lock()
		delete(dc.lock, key)
		dc.lockMutex.Unlock()
	}()

	// 获取过期时间
	expireTime := dc.config.DiskCacheTTL
	if contentType != "" {
		mimeCategory := strings.Split(contentType, ";")[0]
		if typeConfig, exists := dc.config.ContentTypeConfig[mimeCategory]; exists {
			expireTime = typeConfig.DiskTTL
		}
	}

	// 创建缓存项
	item := &CacheItem{
		Data:        value.Data,
		ContentType: value.ContentType,
		CreatedAt:   time.Now(),
		ExpireAt:    time.Now().Add(expireTime),
		LastAccessed: time.Now(),
	}

	// 序列化数据
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("序列化缓存数据失败: %w", err)
	}

	// 写入文件
	filePath := filepath.Join(dc.cacheDir, key+dc.config.CacheFileExt)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("写入缓存文件失败: %w", err)
	}

	// 更新索引
	meta := &CacheMeta{
		Key:         key,
		FilePath:    filePath,
		Size:        len(data),
		CreatedAt:   item.CreatedAt,
		ExpireAt:    item.ExpireAt,
		LastAccessed: item.LastAccessed,
	}

	dc.indexMutex.Lock()
	dc.index[key] = meta
	dc.indexMutex.Unlock()

	// 保存索引
	if err := dc.saveIndex(); err != nil {
		logger.Error("保存缓存索引失败: %v", err)
	}

	logger.Info("缓存写入成功: %s", key)
	return nil
}

// Delete 删除缓存
func (dc *DiskCache) Delete(key string) error {
	dc.indexMutex.Lock()
	meta, exists := dc.index[key]
	if exists {
		delete(dc.index, key)
	}
	dc.indexMutex.Unlock()

	if !exists {
		return nil
	}

	// 删除文件
	if err := os.Remove(meta.FilePath); err != nil && !os.IsNotExist(err) {
		logger.Error("删除缓存文件失败: %v", err)
	}

	// 保存索引
	if err := dc.saveIndex(); err != nil {
		logger.Error("保存缓存索引失败: %v", err)
	}

	return nil
}

// loadIndex 加载索引
func (dc *DiskCache) loadIndex() error {
	indexPath := filepath.Join(dc.cacheDir, dc.config.CacheIndexFile)
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var index map[string]*CacheMeta
	if err := json.Unmarshal(data, &index); err != nil {
		return err
	}

	dc.indexMutex.Lock()
	dc.index = index
	dc.indexMutex.Unlock()

	logger.Info("已加载 %d 个缓存索引", len(index))
	return nil
}

// saveIndex 保存索引
func (dc *DiskCache) saveIndex() error {
	dc.indexMutex.RLock()
	index := make(map[string]*CacheMeta)
	for k, v := range dc.index {
		index[k] = v
	}
	dc.indexMutex.RUnlock()

	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}

	indexPath := filepath.Join(dc.cacheDir, dc.config.CacheIndexFile)
	return os.WriteFile(indexPath, data, 0644)
}

// startCleanup 启动清理任务
func (dc *DiskCache) startCleanup() {
	ticker := time.NewTicker(dc.config.DiskCacheCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		dc.cleanup()
	}
}

// cleanup 清理过期缓存
func (dc *DiskCache) cleanup() {
	logger.Info("开始清理过期缓存...")
	now := time.Now()

	dc.indexMutex.Lock()
	var keysToDelete []string
	for key, meta := range dc.index {
		if now.After(meta.ExpireAt) {
			keysToDelete = append(keysToDelete, key)
		}
	}
	dc.indexMutex.Unlock()

	for _, key := range keysToDelete {
		dc.Delete(key)
	}

	// 检查缓存大小限制
	dc.indexMutex.RLock()
	if len(dc.index) > dc.config.CacheMaxSize {
		// 按最后访问时间排序，删除最旧的
		type entry struct {
			key  string
			meta *CacheMeta
		}
		var entries []entry
		for k, v := range dc.index {
			entries = append(entries, entry{k, v})
		}
		dc.indexMutex.RUnlock()

		// 这里应该按LastAccessed排序，简化处理
		entriesToDelete := len(entries) - dc.config.CacheMaxSize
		for i := 0; i < entriesToDelete && i < len(entries); i++ {
			dc.Delete(entries[i].key)
		}
	} else {
		dc.indexMutex.RUnlock()
	}
}

// NewMemoryCache 创建内存缓存
func NewMemoryCache(cfg *config.CacheConfig) (*MemoryCache, error) {
	cache, err := lru.New[string, *CacheItem](cfg.MemoryCacheSize)
	if err != nil {
		return nil, err
	}

	mc := &MemoryCache{cache: cache}
	go mc.startCleanup(cfg.MemoryCacheCleanupInterval)
	return mc, nil
}

// Get 获取内存缓存
func (mc *MemoryCache) Get(key string) (*CacheItem, bool) {
	return mc.cache.Get(key)
}

// Set 设置内存缓存
func (mc *MemoryCache) Set(key string, value *CacheItem, contentType string) {
	// 检查内容类型配置
	if contentType != "" {
		mimeCategory := strings.Split(contentType, ";")[0]
		if typeConfig, exists := config.LoadConfig().Cache.ContentTypeConfig[mimeCategory]; exists {
			if typeConfig.SkipMemory {
				return
			}
		}
	}

	mc.cache.Add(key, value)
}

// Delete 删除内存缓存
func (mc *MemoryCache) Delete(key string) {
	mc.cache.Remove(key)
}

// startCleanup 启动内存缓存清理
func (mc *MemoryCache) startCleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		// LRU缓存会自动清理，这里可以添加额外的清理逻辑
	}
}

// NewCacheManager 创建缓存管理器
func NewCacheManager(cfg *config.CacheConfig) (*CacheManager, error) {
	if !cfg.CacheEnabled {
		logger.Info("本地缓存已禁用")
		return &CacheManager{
			diskCache:   &DiskCache{},
			memoryCache: &MemoryCache{},
			config:      cfg,
		}, nil
	}

	diskCache := NewDiskCache(cfg)
	if err := diskCache.Init(); err != nil {
		return nil, err
	}

	memoryCache, err := NewMemoryCache(cfg)
	if err != nil {
		return nil, err
	}

	logger.Info("本地缓存已启用")
	return &CacheManager{
		diskCache:   diskCache,
		memoryCache: memoryCache,
		config:      cfg,
	}, nil
}

// GetCacheKey 生成缓存键
func GetCacheKey(url string) string {
	hash := md5.Sum([]byte(url))
	return fmt.Sprintf("%x", hash)
} 