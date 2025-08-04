package logger

import (
	"os"
	"time"

	"github.com/sirupsen/logrus"
)

// LogPrefix 日志前缀结构
type LogPrefix struct {
	Info    string
	Error   string
	Warn    string
	Success string
	Cache   CachePrefix
}

// CachePrefix 缓存相关日志前缀
type CachePrefix struct {
	Hit  string
	Miss string
	Info string
}

// Logger 全局日志实例
var Logger *logrus.Logger

// InitLogger 初始化日志系统
func InitLogger() *LogPrefix {
	Logger = logrus.New()
	
	// 设置日志格式
	Logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: time.RFC3339,
		ForceColors:     true,
	})
	
	// 设置输出
	Logger.SetOutput(os.Stdout)
	
	// 设置日志级别
	Logger.SetLevel(logrus.InfoLevel)
	
	// 创建日志前缀
	prefix := &LogPrefix{
		Info:    "[ 信息 ]",
		Error:   "[ 错误 ]",
		Warn:    "[ 警告 ]",
		Success: "[ 成功 ]",
		Cache: CachePrefix{
			Hit:  "[ 缓存命中 ]",
			Miss: "[ 缓存未命中 ]",
			Info: "[ 缓存信息 ]",
		},
	}
	
	Logger.Info(prefix.Info, "日志系统初始化成功")
	return prefix
}

// Info 信息日志
func Info(message string, args ...interface{}) {
	Logger.Infof(message, args...)
}

// Error 错误日志
func Error(message string, args ...interface{}) {
	Logger.Errorf(message, args...)
}

// Warn 警告日志
func Warn(message string, args ...interface{}) {
	Logger.Warnf(message, args...)
}

// Success 成功日志
func Success(message string, args ...interface{}) {
	Logger.Infof(message, args...)
}

// CacheHit 缓存命中日志
func CacheHit(message string, args ...interface{}) {
	Logger.Infof(message, args...)
}

// CacheMiss 缓存未命中日志
func CacheMiss(message string, args ...interface{}) {
	Logger.Infof(message, args...)
}

// CacheInfo 缓存信息日志
func CacheInfo(message string, args ...interface{}) {
	Logger.Infof(message, args...)
} 