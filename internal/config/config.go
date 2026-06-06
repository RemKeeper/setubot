package config

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
)

type File struct {
	NickName      []string       `json:"nickName"`
	CommandPrefix string         `json:"commandPrefix"`
	SuperUsers    []int64        `json:"superUsers"`
	Drivers       []DriverConfig `json:"drivers"`
	Draw          DrawConfig     `json:"draw"`
}

type DrawConfig struct {
	Enabled     bool   `json:"enabled"`
	BaseURL     string `json:"baseURL"`
	APIKey      string `json:"apiKey"`
	Model       string `json:"model"`
	MaxImages   int    `json:"maxImages"`
	DefaultSize string `json:"defaultSize"`
	Timeout     int    `json:"timeout"`
}

type DriverConfig struct {
	Type        string `json:"type"`
	URL         string `json:"url"`
	AccessToken string `json:"accessToken"`
	PostURL     string `json:"postURL"`
	PostToken   string `json:"postToken"`
	MaxConn     int    `json:"maxConn"`
}

func Load(path string) (*zero.Config, error) {
	file, err := LoadFile(path)
	if err != nil {
		return nil, err
	}

	return file.ToZeroConfig(), nil
}

func LoadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg File
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	cfg.Draw = cfg.Draw.withDefaults()

	return &cfg, nil
}

func (cfg DrawConfig) withDefaults() DrawConfig {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-image-1"
	}
	if cfg.MaxImages <= 0 {
		cfg.MaxImages = 3
	}
	if cfg.DefaultSize == "" {
		cfg.DefaultSize = "1024x1024"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120
	}

	return cfg
}

func (cfg File) ToZeroConfig() *zero.Config {
	return &zero.Config{
		NickName:      cfg.NickName,
		CommandPrefix: cfg.CommandPrefix,
		SuperUsers:    cfg.SuperUsers,
		Driver:        buildDrivers(cfg.Drivers),
	}
}

func buildDrivers(configs []DriverConfig) []zero.Driver {
	drivers := make([]zero.Driver, 0, len(configs))
	for _, cfg := range configs {
		driver, ok := buildDriver(cfg)
		if !ok {
			continue
		}

		drivers = append(drivers, driver)
	}

	return drivers
}

func buildDriver(cfg DriverConfig) (zero.Driver, bool) {
	switch strings.ToLower(cfg.Type) {
	case "websocket-client", "ws-client":
		return driver.NewWebSocketClient(cfg.URL, cfg.AccessToken), true
	case "websocket-server", "ws-server":
		return driver.NewWebSocketServer(maxConnOrDefault(cfg.MaxConn), cfg.URL, cfg.AccessToken), true
	case "http-client", "http":
		return driver.NewHTTPClient(cfg.URL, cfg.AccessToken, cfg.PostURL, cfg.PostToken), true
	default:
		log.Printf("跳过未知驱动类型: %s", cfg.Type)
		return nil, false
	}
}

func maxConnOrDefault(maxConn int) int {
	if maxConn <= 0 {
		return 16
	}

	return maxConn
}
