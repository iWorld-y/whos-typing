// Package whostyping 提供应用的构建信息。
package whostyping

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed mygo.json
var appConfig []byte

// Version 返回 MyGo 配置中的应用版本。
func Version() string {
	var config struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(appConfig, &config); err != nil {
		panic(fmt.Errorf("读取应用版本失败: %w", err))
	}
	if config.Version == "" {
		panic("应用版本为空")
	}
	return config.Version
}
