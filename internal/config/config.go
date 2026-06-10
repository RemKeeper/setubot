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
	Agent         AgentConfig    `json:"agent"`
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

type AgentConfig struct {
	Enabled             bool          `json:"enabled"`
	BaseURL             string        `json:"baseURL"`
	APIKey              string        `json:"apiKey"`
	Model               string        `json:"model"`
	SystemPrompt        string        `json:"systemPrompt"`
	SkillDir            string        `json:"skillDir"`
	MemoryDir           string        `json:"memoryDir"`
	Timeout             int           `json:"timeout"`
	MaxToolRounds       int           `json:"maxToolRounds"`
	MaxContextTurns     int           `json:"maxContextTurns"`
	SummaryTriggerTurns int           `json:"summaryTriggerTurns"`
	SummaryKeepTurns    int           `json:"summaryKeepTurns"`
	ContextTTL          int           `json:"contextTTL"`
	MaxResponseChars    int           `json:"maxResponseChars"`
	Temperature         float64       `json:"temperature"`
	Debug               bool          `json:"debug"`
	DebugLogPath        string        `json:"debugLogPath"`
	Browser             BrowserConfig `json:"browser"`
	Exa                 ExaConfig     `json:"exa"`
	EHTag               EHTagConfig   `json:"ehTag"`
	EHReq               EHReqConfig   `json:"ehReq"`
}

type BrowserConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseURL"`
}

type ExaConfig struct {
	Enabled           bool   `json:"enabled"`
	APIKey            string `json:"apiKey"`
	BaseURL           string `json:"baseURL"`
	DefaultType       string `json:"defaultType"`
	DefaultNumResults int    `json:"defaultNumResults"`
}

type EHTagConfig struct {
	Enabled   bool   `json:"enabled"`
	SourceURL string `json:"sourceURL"`
	CachePath string `json:"cachePath"`
}

type EHReqConfig struct {
	Enabled      bool   `json:"enabled"`
	Cookie       string `json:"cookie"`
	CookieEnv    string `json:"cookieEnv"`
	CookiePath   string `json:"cookiePath"`
	ProxyURL     string `json:"proxyURL"`
	ProxyEnv     string `json:"proxyEnv"`
	UserAgent    string `json:"userAgent"`
	MaxBodyChars int    `json:"maxBodyChars"`
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
	cfg.Agent = cfg.Agent.withDefaults()

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

func (cfg AgentConfig) withDefaults() AgentConfig {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if cfg.SkillDir == "" {
		cfg.SkillDir = "skills"
	}
	if cfg.MemoryDir == "" {
		cfg.MemoryDir = "data/memory"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120
	}
	if cfg.MaxToolRounds <= 0 {
		cfg.MaxToolRounds = 5
	}
	if cfg.MaxContextTurns <= 0 {
		cfg.MaxContextTurns = 10
	}
	if cfg.SummaryTriggerTurns <= 0 {
		cfg.SummaryTriggerTurns = 8
	}
	if cfg.SummaryKeepTurns <= 0 {
		cfg.SummaryKeepTurns = 4
	}
	if cfg.ContextTTL <= 0 {
		cfg.ContextTTL = 3600
	}
	if cfg.MaxResponseChars <= 0 {
		cfg.MaxResponseChars = 3500
	}
	if cfg.DebugLogPath == "" {
		cfg.DebugLogPath = "data/agent_api_body.log"
	}
	if cfg.Browser.BaseURL == "" {
		cfg.Browser.BaseURL = "http://127.0.0.1:58000"
	}
	if cfg.Exa.BaseURL == "" {
		cfg.Exa.BaseURL = "https://api.exa.ai"
	}
	if cfg.Exa.DefaultType == "" {
		cfg.Exa.DefaultType = "auto"
	}
	if cfg.Exa.DefaultNumResults <= 0 {
		cfg.Exa.DefaultNumResults = 5
	}
	if cfg.EHTag.SourceURL == "" {
		cfg.EHTag.SourceURL = "https://fastly.jsdelivr.net/gh/EhTagTranslation/DatabaseReleases/db.html.json"
	}
	if cfg.EHTag.CachePath == "" {
		cfg.EHTag.CachePath = "data/eh_tag_db.html.json"
	}
	if cfg.EHReq.CookieEnv == "" {
		cfg.EHReq.CookieEnv = "EHENTAI_COOKIE"
	}
	if cfg.EHReq.CookiePath == "" {
		cfg.EHReq.CookiePath = ".secrets/ehentai.cookies"
	}
	if cfg.EHReq.ProxyEnv == "" {
		cfg.EHReq.ProxyEnv = "EHENTAI_PROXY"
	}
	if cfg.EHReq.UserAgent == "" {
		cfg.EHReq.UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36"
	}
	if cfg.EHReq.MaxBodyChars <= 0 {
		cfg.EHReq.MaxBodyChars = 200000
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
