package logger

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// 颜色常量
const (
	ColorRed     = "\033[31m"
	ColorGreen   = "\033[32m"
	ColorYellow  = "\033[33m"
	ColorBlue    = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorCyan    = "\033[36m"
	ColorReset   = "\033[0m"
)

// 日志级别常量
const (
	LogLevelDebug = iota
	LogLevelInfo
	LogLevelWarning
	LogLevelError
)

// 全局变量
var (
	currentLogLevel = getLogLevelFromEnv()
	loggerMutex     sync.RWMutex
)

// 获取日志级别
func getLogLevelFromEnv() int {
	logLevel := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	switch logLevel {
	case "DEBUG":
		return LogLevelDebug
	case "WARNING":
		return LogLevelWarning
	case "ERROR":
		return LogLevelError
	default:
		return LogLevelInfo
	}
}

// 设置日志级别
func SetLogLevel(level string) {
	loggerMutex.Lock()
	defer loggerMutex.Unlock()
	
	switch strings.ToUpper(level) {
	case "DEBUG":
		currentLogLevel = LogLevelDebug
	case "INFO":
		currentLogLevel = LogLevelInfo
	case "WARNING":
		currentLogLevel = LogLevelWarning
	case "ERROR":
		currentLogLevel = LogLevelError
	default:
		currentLogLevel = LogLevelInfo
	}
	
	Info("日志级别设置为: %s", level)
}

// 通用日志函数
func logMessage(level, color, message string) {
	hostname, _ := os.Hostname()
	log.Printf("%s[%s]%s [%s] %s", color, level, ColorReset, hostname, message)
}

// Info 信息日志
func Info(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelInfo {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("信息", ColorBlue, formattedMessage)
	}
}

// Success 成功日志
func Success(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelInfo {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("成功", ColorGreen, formattedMessage)
	}
}

// Warn 警告日志
func Warn(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelWarning {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("警告", ColorYellow, formattedMessage)
	}
}

// Error 错误日志
func Error(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelError {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("错误", ColorRed, formattedMessage)
	}
}

// Debug 调试日志
func Debug(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelDebug {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("调试", ColorMagenta, formattedMessage)
	}
}

// CacheHit 缓存命中日志
func CacheHit(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelInfo {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("缓存命中", ColorGreen, formattedMessage)
	}
}

// CacheMiss 缓存未命中日志
func CacheMiss(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelInfo {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("缓存未命中", ColorYellow, formattedMessage)
	}
}

// CacheInfo 缓存信息日志
func CacheInfo(message string, args ...interface{}) {
	if currentLogLevel <= LogLevelInfo {
		formattedMessage := fmt.Sprintf(message, args...)
		logMessage("缓存信息", ColorCyan, formattedMessage)
	}
}
