// Package config 解析飞牛AT力场运行目录与监听地址。
// 约定：配置数据放 ATFF_HOME 下；持久化数据可经 ATFF_DATA_DIR 指向 NAS 共享目录。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// EnvHome 覆盖应用运行根目录（对应 FPK 的 TRIM_PKGVAR）
	EnvHome = "ATFF_HOME"
	// EnvSock 覆盖 unix socket 路径（fnOS 统一网关使用）
	EnvSock = "ATFF_SOCK"
	// EnvAddr 覆盖 TCP 监听地址
	EnvAddr = "ATFF_ADDR"
	// EnvBasePath 覆盖统一网关路径前缀
	EnvBasePath = "ATFF_BASE_PATH"
	// EnvDataDir 覆盖持久化数据目录，指向 NAS 共享目录
	EnvDataDir = "ATFF_DATA_DIR"

	// DefaultAddr 默认只监听回环，避免无认证接口暴露到局域网
	DefaultAddr = "127.0.0.1:9971"
)

var (
	rootOnce sync.Once
	rootDir  string
)

// Root 返回应用运行根目录。优先级：ATFF_HOME > 当前工作目录。
func Root() string {
	rootOnce.Do(func() { rootDir = resolveRoot() })
	return rootDir
}

func resolveRoot() string {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// DataDir 返回持久化数据目录。优先 ATFF_DATA_DIR，否则 Root/data
func DataDir() string {
	if v := strings.TrimSpace(os.Getenv(EnvDataDir)); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
	}
	return filepath.Join(Root(), "data")
}

// ConfigFile 返回运行偏好配置文件路径
func ConfigFile() string { return filepath.Join(DataDir(), "config.json") }

// SockPath 返回 unix socket 路径
func SockPath() string {
	if v := strings.TrimSpace(os.Getenv(EnvSock)); v != "" {
		return v
	}
	return filepath.Join(Root(), "AT-ForceField.sock")
}

// Addr 返回 TCP 监听地址
func Addr() string {
	if v := strings.TrimSpace(os.Getenv(EnvAddr)); v != "" {
		return v
	}
	return DefaultAddr
}

// BasePath 返回 fnOS 统一网关路径前缀，例如 /app/feiniu-atforcefield
func BasePath() string {
	v := strings.TrimSpace(os.Getenv(EnvBasePath))
	if v == "" {
		return ""
	}
	return "/" + strings.Trim(v, "/")
}

// EnsureDirs 创建运行所需目录
func EnsureDirs() error {
	return os.MkdirAll(DataDir(), 0o755)
}
