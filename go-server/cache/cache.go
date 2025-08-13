package cache

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"main/config"
	"main/logger"

	lru "github.com/hashicorp/golang-lru/v2"
)

// CacheItem 缓存项
type CacheItem struct {
	Data         interface{} `json:"data"`
	ContentType  string      `json:"contentType"`
	CreatedAt    time.Time   `json:"createdAt"`
	ExpireAt     time.Time   `json:"expireAt"`
	LastAccessed time.Time   `json:"lastAccessed"`
	IsImage      bool        `json:"isImage"` // 新增字段标识是否为图片
}

// CacheMeta 缓存元数据
type CacheMeta struct {
	Key          string    `json:"key"`
	FilePath     string    `json:"filePath"`
	Size         int       `json:"size"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpireAt     time.Time `json:"expireAt"`
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
		// 确保即使加载失败，索引也不为nil
		if dc.index == nil {
			dc.index = make(map[string]*CacheMeta)
		}
	}

	// 启动清理任务
	go dc.startCleanup()

	logger.Info("磁盘缓存初始化完成")
	return nil
}

// Get 获取缓存
func (dc *DiskCache) Get(key string) (*CacheItem, error) {
	// 如果缓存被禁用，直接返回nil
	if dc.config != nil && !dc.config.CacheEnabled {
		return nil, nil
	}

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

	// 读取元数据文件
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

	// 如果是图片类型，需要读取图片数据
	if item.IsImage {
		imageFilePath := filepath.Join(dc.cacheDir, key+".img")
		imageData, err := os.ReadFile(imageFilePath)
		if err != nil {
			logger.Error("读取图片文件失败: %v", err)
			dc.Delete(key)
			return nil, nil
		}
		item.Data = imageData
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
	// 如果缓存被禁用，直接返回
	if dc.config != nil && !dc.config.CacheEnabled {
		return nil
	}

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

	// 检查是否为图片类型
	isImage := strings.HasPrefix(contentType, "image/")

	// 创建缓存项，确保IsImage字段正确设置
	item := &CacheItem{
		Data:         value.Data,
		ContentType:  value.ContentType,
		CreatedAt:    time.Now(),
		ExpireAt:     time.Now().Add(expireTime),
		LastAccessed: time.Now(),
		IsImage:      isImage, // 根据contentType正确设置IsImage字段
	}

	var filePath string
	var dataSize int

	if isImage {
		// 图片数据直接存储为二进制文件
		var imageData []byte
		switch data := value.Data.(type) {
		case []byte:
			imageData = data
		case string:
			imageData = []byte(data)
		default:
			return fmt.Errorf("图片数据类型错误: %T", value.Data)
		}

		// 存储图片数据
		imageFilePath := filepath.Join(dc.cacheDir, key+".img")
		if err := os.WriteFile(imageFilePath, imageData, 0644); err != nil {
			return fmt.Errorf("写入图片文件失败: %w", err)
		}

		// 存储元数据（不包含图片数据）
		metaItem := &CacheItem{
			Data:         nil, // 图片数据单独存储
			ContentType:  value.ContentType,
			CreatedAt:    item.CreatedAt,
			ExpireAt:     item.ExpireAt,
			LastAccessed: item.LastAccessed,
			IsImage:      true, // 确保元数据中IsImage为true
		}

		metaData, err := json.Marshal(metaItem)
		if err != nil {
			os.Remove(imageFilePath) // 清理已创建的图片文件
			return fmt.Errorf("序列化图片元数据失败: %w", err)
		}

		filePath = filepath.Join(dc.cacheDir, key+dc.config.CacheFileExt)
		if err := os.WriteFile(filePath, metaData, 0644); err != nil {
			os.Remove(imageFilePath) // 清理已创建的图片文件
			return fmt.Errorf("写入图片元数据文件失败: %w", err)
		}

		dataSize = len(imageData)
	} else {
		// 非图片数据使用JSON序列化
		data, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("序列化缓存数据失败: %w", err)
		}

		filePath = filepath.Join(dc.cacheDir, key+dc.config.CacheFileExt)
		if err := os.WriteFile(filePath, data, 0644); err != nil {
			return fmt.Errorf("写入缓存文件失败: %w", err)
		}

		dataSize = len(data)
	}

	// 更新索引
	meta := &CacheMeta{
		Key:          key,
		FilePath:     filePath,
		Size:         dataSize,
		CreatedAt:    item.CreatedAt,
		ExpireAt:     item.ExpireAt,
		LastAccessed: item.LastAccessed,
	}

	dc.indexMutex.Lock()
	dc.index[key] = meta
	dc.indexMutex.Unlock()

	// 保存索引
	if err := dc.saveIndex(); err != nil {
		logger.Error("保存缓存索引失败: %v", err)
	}

	// 不在这里输出日志，由调用方处理
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

	// 删除元数据文件
	if err := os.Remove(meta.FilePath); err != nil && !os.IsNotExist(err) {
		logger.Error("删除缓存文件失败: %v", err)
	}

	// 如果是图片类型，还需要删除图片文件
	imageFilePath := filepath.Join(dc.cacheDir, key+".img")
	if err := os.Remove(imageFilePath); err != nil && !os.IsNotExist(err) {
		logger.Error("删除图片文件失败: %v", err)
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
		// 如果反序列化失败（可能是格式不兼容），删除损坏的索引文件重新开始
		logger.Error("缓存索引文件格式不兼容，将重新构建: %v", err)
		if removeErr := os.Remove(indexPath); removeErr != nil {
			logger.Error("删除损坏的索引文件失败: %v", removeErr)
		} else {
			logger.Info("已删除损坏的缓存索引文件，将重新构建")
		}
		// 初始化空索引
		dc.indexMutex.Lock()
		dc.index = make(map[string]*CacheMeta)
		dc.indexMutex.Unlock()
		return nil
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
	// 如果缓存为nil（禁用状态），直接返回
	if mc.cache == nil {
		return
	}

	// 检查内容类型配置
	if contentType != "" {
		mimeCategory := strings.Split(contentType, ";")[0]
		if typeConfig, exists := config.LoadConfig().Cache.ContentTypeConfig[mimeCategory]; exists {
			if typeConfig.SkipMemory {
				return
			}
		}
	}

	// 确保图片数据在内存中也是[]byte类型，并正确设置IsImage字段
	if strings.HasPrefix(contentType, "image/") {
		// 创建新的缓存项，确保图片数据是[]byte类型
		imageItem := &CacheItem{
			Data:         value.Data,
			ContentType:  value.ContentType,
			CreatedAt:    value.CreatedAt,
			ExpireAt:     value.ExpireAt,
			LastAccessed: value.LastAccessed,
			IsImage:      true, // 确保图片类型正确设置
		}

		// 确保图片数据是[]byte类型
		switch data := value.Data.(type) {
		case []byte:
			imageItem.Data = data
		case string:
			imageItem.Data = []byte(data)
		default:
			logger.Error("图片数据类型错误，跳过内存缓存: %T", value.Data)
			return
		}

		mc.cache.Add(key, imageItem)
		logger.Info("内存缓存写入图片: %s (大小: %d字节, IsImage: %t)", key, len(imageItem.Data.([]byte)), imageItem.IsImage)
	} else {
		// 非图片数据，确保IsImage字段正确
		nonImageItem := &CacheItem{
			Data:         value.Data,
			ContentType:  value.ContentType,
			CreatedAt:    value.CreatedAt,
			ExpireAt:     value.ExpireAt,
			LastAccessed: value.LastAccessed,
			IsImage:      false, // 确保非图片类型正确设置
		}
		mc.cache.Add(key, nonImageItem)
	}
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
		// 返回空操作缓存对象
		return &CacheManager{
			diskCache:   &DiskCache{config: cfg},
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
func GetCacheKey(urlStr string) string {
	// 与JavaScript版本保持一致的缓存键生成逻辑
	// JavaScript版本使用: url.parse(req.originalUrl, true) + URLSearchParams排序 + MD5

	// 解析URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		// 如果解析失败，使用原始URL作为降级处理
		logger.Error("URL解析失败，使用原始URL: %v", err)
		hash := md5.Sum([]byte(urlStr))
		return fmt.Sprintf("%x", hash)
	}

	// 获取路径部分
	pathname := parsedURL.Path
	if pathname == "" {
		pathname = "/"
	}

	// 处理查询参数
	var queryString string
	if len(parsedURL.Query()) > 0 {
		// 获取所有查询参数
		params := make([]string, 0, len(parsedURL.Query()))
		for key, values := range parsedURL.Query() {
			for _, value := range values {
				params = append(params, fmt.Sprintf("%s=%s", key, value))
			}
		}

		// 排序参数（与JavaScript的URLSearchParams.sort()保持一致）
		sort.Strings(params)
		queryString = strings.Join(params, "&")
	}

	// 构建缓存键字符串
	var cacheKeyStr string
	if queryString != "" {
		cacheKeyStr = fmt.Sprintf("%s?%s", pathname, queryString)
	} else {
		cacheKeyStr = pathname
	}

	// 生成MD5哈希
	hash := md5.Sum([]byte(cacheKeyStr))
	return fmt.Sprintf("%x", hash)
}

// GetConfig 获取配置
func (cm *CacheManager) GetConfig() *config.CacheConfig {
	return cm.config
}

// GetDiskCache 获取磁盘缓存
func (cm *CacheManager) GetDiskCache() *DiskCache {
	return cm.diskCache
}

// GetMemoryCache 获取内存缓存
func (cm *CacheManager) GetMemoryCache() *MemoryCache {
	return cm.memoryCache
}
