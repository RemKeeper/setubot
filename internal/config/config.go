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
	Enabled             bool               `json:"enabled"`
	BaseURL             string             `json:"baseURL"`
	APIKey              string             `json:"apiKey"`
	Model               string             `json:"model"`
	SystemPrompt        string             `json:"systemPrompt"`
	SkillDir            string             `json:"skillDir"`
	MemoryDir           string             `json:"memoryDir"`
	Timeout             int                `json:"timeout"`
	MaxToolRounds       int                `json:"maxToolRounds"`
	MaxContextTurns     int                `json:"maxContextTurns"`
	MaxContextChars     int                `json:"maxContextChars"`
	MaxToolResultChars  int                `json:"maxToolResultChars"`
	SummaryTriggerTurns int                `json:"summaryTriggerTurns"`
	SummaryKeepTurns    int                `json:"summaryKeepTurns"`
	ContextTTL          int                `json:"contextTTL"`
	MaxResponseChars    int                `json:"maxResponseChars"`
	Temperature         float64            `json:"temperature"`
	Debug               bool               `json:"debug"`
	DebugLogPath        string             `json:"debugLogPath"`
	Vision              VisionConfig       `json:"vision"`
	TaskGuard           TaskGuardConfig    `json:"taskGuard"`
	ForwardImage        ForwardImageConfig `json:"forwardImage"`
	Browser             BrowserConfig      `json:"browser"`
	Exa                 ExaConfig          `json:"exa"`
	EHTag               EHTagConfig        `json:"ehTag"`
	EHReq               EHReqConfig        `json:"ehReq"`
}

type VisionConfig struct {
	Enabled bool   `json:"enabled"`
	Detail  string `json:"detail"`
}

type ForwardImageConfig struct {
	APNGSurfacePath   string `json:"apngSurfacePath"`
	APNGCacheMaxBytes int64  `json:"apngCacheMaxBytes"`
}

type TaskGuardConfig struct {
	Enabled          bool     `json:"enabled"`
	MaxSteps         int      `json:"maxSteps"`
	LongTaskKeywords []string `json:"longTaskKeywords"`
	CompletionPrompt string   `json:"completionPrompt"`
}

type BrowserConfig struct {
	Enabled          bool   `json:"enabled"`
	BaseURL          string `json:"baseURL"`
	MaxResponseBytes int64  `json:"maxResponseBytes"`
	MaxResultChars   int    `json:"maxResultChars"`
	MaxSubagentSteps int    `json:"maxSubagentSteps"`
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
	Enabled            bool   `json:"enabled"`
	Cookie             string `json:"cookie"`
	CookieEnv          string `json:"cookieEnv"`
	CookiePath         string `json:"cookiePath"`
	ProxyURL           string `json:"proxyURL"`
	ProxyEnv           string `json:"proxyEnv"`
	UserAgent          string `json:"userAgent"`
	MaxBodyChars       int    `json:"maxBodyChars"`
	ImageCacheMaxBytes int64  `json:"imageCacheMaxBytes"`
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
	if cfg.MaxContextChars <= 0 {
		cfg.MaxContextChars = 300000
	}
	if cfg.MaxToolResultChars <= 0 {
		cfg.MaxToolResultChars = 60000
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
	switch strings.ToLower(strings.TrimSpace(cfg.Vision.Detail)) {
	case "low", "high":
		cfg.Vision.Detail = strings.ToLower(strings.TrimSpace(cfg.Vision.Detail))
	default:
		cfg.Vision.Detail = "auto"
	}
	if cfg.TaskGuard.MaxSteps <= 0 {
		cfg.TaskGuard.MaxSteps = cfg.MaxToolRounds
	}
	if len(cfg.TaskGuard.LongTaskKeywords) == 0 {
		cfg.TaskGuard.LongTaskKeywords = []string{"分批", "全部", "继续", "直到完成", "所有图片"}
	}
	if cfg.TaskGuard.CompletionPrompt == "" {
		cfg.TaskGuard.CompletionPrompt = "当前请求疑似长流程任务。你必须维护任务进度：先判断总目标、已完成项和剩余项；只要仍有未完成项目，就继续调用合适工具推进，不要提前总结或声称完成。若存在可一次完成全部子任务的原子化工具，应优先调用该工具。"
	}
	if cfg.ForwardImage.APNGCacheMaxBytes <= 0 {
		cfg.ForwardImage.APNGCacheMaxBytes = 512 << 20
	}
	if cfg.Browser.BaseURL == "" {
		cfg.Browser.BaseURL = "http://127.0.0.1:58000"
	}
	if cfg.Browser.MaxResponseBytes <= 0 {
		cfg.Browser.MaxResponseBytes = 256 << 10
	}
	if cfg.Browser.MaxResultChars <= 0 {
		cfg.Browser.MaxResultChars = 40000
	}
	if cfg.Browser.MaxSubagentSteps <= 0 {
		cfg.Browser.MaxSubagentSteps = 12
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
	if cfg.EHReq.ImageCacheMaxBytes <= 0 {
		cfg.EHReq.ImageCacheMaxBytes = 2 << 30
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
