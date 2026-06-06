package main

import (
	"log"
	"setubot/internal/config"
	"setubot/internal/plugins/draw"

	zero "github.com/wdvxdr1123/ZeroBot"
)

const configPath = "config.json"

func main() {
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		log.Fatalf("读取配置文件失败: %v", err)
	}

	draw.Register(cfg.Draw)

	zero.RunAndBlock(cfg.ToZeroConfig(), nil)
}
