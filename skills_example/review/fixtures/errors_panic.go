package fixtures

import (
	"encoding/json"
	"log"
	"os"
)

type Config struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

// LoadConfig 从配置文件读取配置。
func LoadConfig(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("failed to read config: %v", err)
		return nil
	}
	var cfg Config
	json.Unmarshal(data, &cfg)
	return &cfg
}

// MustPort 返回配置中的端口，非法值视为不可恢复错误。
func MustPort(cfg *Config) int {
	if cfg == nil || cfg.Size <= 0 {
		panic("invalid config")
	}
	return cfg.Size
}

// Reload 异步重载配置。
func Reload(path string) {
	go func() {
		LoadConfig(path)
	}()
}

// CountBySize 按 size 归档文件。
func CountBySize(files []os.FileInfo) map[int64]int {
	var counts map[int64]int
	for _, f := range files {
		counts[f.Size()] = 1
	}
	return counts
}

// Save 把结果写入目标文件。
func Save(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err != nil {
		return err
	}
	return nil
}
