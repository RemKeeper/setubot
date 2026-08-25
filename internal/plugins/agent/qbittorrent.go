package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"setubot/internal/config"

	zero "github.com/wdvxdr1123/ZeroBot"
)

// qbClient 封装 qBittorrent WebUI API（v2）。
// 参考: https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-4.1)
type qbClient struct {
	cfg     config.QBittorrentConfig
	baseURL string
	http    *http.Client
	sid     string
	logged  bool
}

func newQBClient(cfg config.QBittorrentConfig) *qbClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15
	}
	// qBittorrent WebUI 常用自签 HTTPS 证书，内网环境放宽 TLS 校验。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	return &qbClient{
		cfg:     cfg,
		baseURL: cfg.BaseURL(),
		http:    &http.Client{Timeout: time.Duration(timeout) * time.Second, Transport: transport},
	}
}

func (c *qbClient) login(ctx context.Context) error {
	if c.logged && c.sid != "" {
		return nil
	}
	form := url.Values{}
	form.Set("username", c.cfg.Username)
	form.Set("password", c.cfg.Password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接 qBittorrent 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(string(body)), "Ok.") {
		return fmt.Errorf("qBittorrent 登录失败（HTTP %d）：%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	c.sid = ""
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "SID" {
			c.sid = cookie.Value
		}
	}
	if c.sid == "" {
		return fmt.Errorf("qBittorrent 登录响应缺少 SID Cookie")
	}
	c.logged = true
	return nil
}

func (c *qbClient) do(ctx context.Context, method, path string, form url.Values, query url.Values) ([]byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c.sid != "" {
		req.Header.Set("Cookie", "SID="+c.sid)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	// 会话过期（403）时重新登录并重试一次。
	if resp.StatusCode == http.StatusForbidden && c.logged {
		c.logged = false
		if err := c.login(ctx); err != nil {
			return nil, err
		}
		return c.do(ctx, method, path, form, query)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("qBittorrent %s 返回 %d：%s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *qbClient) setShareLimits(ctx context.Context, hashes []string, ratioLimit, seedingTimeLimit float64) error {
	form := url.Values{}
	form.Set("hashes", strings.Join(hashes, "|"))
	form.Set("ratioLimit", formatFloat(ratioLimit))
	form.Set("seedingTimeLimit", formatFloat(seedingTimeLimit))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/setShareLimits", form, nil)
	return err
}

func (c *qbClient) hashesForm(hashes []string) url.Values {
	form := url.Values{}
	form.Set("hashes", strings.Join(hashes, "|"))
	return form
}

// qbTorrent 对应 /api/v2/torrents/info 的元素。
type qbTorrent struct {
	Hash          string  `json:"hash"`
	Name          string  `json:"name"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	Category      string  `json:"category"`
	Tags          string  `json:"tags"`
	Size          int64   `json:"size"`
	Downloaded    int64   `json:"downloaded"`
	Uploaded      int64   `json:"uploaded"`
	Ratio         float64 `json:"ratio"`
	SavePath      string  `json:"save_path"`
	ETA           int64   `json:"eta"`
	DownloadSpeed int64   `json:"dlspeed"`
	UploadSpeed   int64   `json:"upspeed"`
	NumSeeds      int     `json:"num_seeds"`
	NumLeechs     int     `json:"num_leechs"`
	AddedOn       int64   `json:"added_on"`
	CompletedOn   int64   `json:"completion_on"`
	Tracker       string  `json:"tracker"`
	SeedingTime   int64   `json:"seeding_time"`
}

type qbTracker struct {
	URL      string `json:"url"`
	Tier     int    `json:"tier"`
	Status   int    `json:"status"`
	NumSeeds int    `json:"num_seeds"`
	NumPeers int    `json:"num_peers"`
	Msg      string `json:"msg"`
}

type qbTorrentProperties struct {
	SavePath     string  `json:"save_path"`
	CreationDate int64   `json:"creation_date"`
	TotalSize    int64   `json:"total_size"`
	Downloaded   int64   `json:"downloaded"`
	Uploaded     int64   `json:"uploaded"`
	ShareRatio   float64 `json:"share_ratio"`
	SeedingTime  int64   `json:"seeding_time"`
	TimeElapsed  int64   `json:"time_elapsed"`
	Tracker      string  `json:"tracker"`
	Comment      string  `json:"comment"`
}

// callQBittorrent 统一 qBittorrent 工具入口。
// 鉴权在工具层完成：配置 ownerOnly=true 时仅主人（superUsers）可调用，不依赖模型判断。
func (p *plugin) callQBittorrent(zc *zero.Ctx, args map[string]interface{}) (string, error) {
	if !p.cfg.QBittorrent.Enabled {
		return "", fmt.Errorf("qBittorrent 工具未启用，请在 agent.qbittorrent.enabled 中开启")
	}
	if p.cfg.QBittorrent.OwnerOnly && !p.isSuperUser(zc.Event.UserID) {
		return "", fmt.Errorf("qBittorrent 工具仅主人可用")
	}
	if strings.TrimSpace(p.cfg.QBittorrent.Username) == "" || strings.TrimSpace(p.cfg.QBittorrent.Password) == "" {
		return "", fmt.Errorf("qBittorrent 用户名或密码未配置")
	}

	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
	if action == "" {
		return "", fmt.Errorf("action 不能为空")
	}

	client := newQBClient(p.cfg.QBittorrent)
	timeout := p.cfg.QBittorrent.Timeout
	if timeout <= 0 {
		timeout = 15
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	switch action {
	case "add":
		return p.qbAdd(ctx, client, args)
	case "list":
		return p.qbList(ctx, client, args)
	case "status":
		return p.qbStatus(ctx, client, args)
	case "pause":
		return p.qbPauseResume(ctx, client, args, true)
	case "resume":
		return p.qbPauseResume(ctx, client, args, false)
	case "delete":
		return p.qbDelete(ctx, client, args)
	case "share_limit":
		return p.qbShareLimit(ctx, client, args)
	case "reannounce":
		return p.qbReannounce(ctx, client, args)
	default:
		return "", fmt.Errorf("未知 qbittorrent 动作：%s（支持 add/list/status/pause/resume/delete/share_limit/reannounce）", action)
	}
}

func (p *plugin) qbAdd(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	urls := stringSliceArg(args, "urls")
	if len(urls) == 0 {
		if u := stringArg(args, "url"); u != "" {
			urls = []string{u}
		}
	}
	seen := make(map[string]bool, len(urls))
	clean := make([]string, 0, len(urls))
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		clean = append(clean, u)
	}
	if len(clean) == 0 {
		return "", fmt.Errorf("至少提供一个磁力链接或种子 URL（urls）")
	}

	if err := client.login(ctx); err != nil {
		return "", err
	}

	// 记录添加前已有的 hash，用于识别新增任务。
	before := map[string]bool{}
	if data, err := client.do(ctx, http.MethodGet, "/api/v2/torrents/info", nil, nil); err == nil {
		var existing []qbTorrent
		_ = json.Unmarshal(data, &existing)
		for _, t := range existing {
			before[t.Hash] = true
		}
	}

	form := url.Values{}
	form.Set("urls", strings.Join(clean, "\n"))
	if savePath := stringArg(args, "save_path"); savePath != "" {
		form.Set("savepath", savePath)
	} else if p.cfg.QBittorrent.DefaultSavePath != "" {
		form.Set("savepath", p.cfg.QBittorrent.DefaultSavePath)
	}
	if category := stringArg(args, "category"); category != "" {
		form.Set("category", category)
	}
	if tags := stringSliceArg(args, "tags"); len(tags) > 0 {
		form.Set("tags", strings.Join(tags, ","))
	}
	if boolArg(args, "paused", false) {
		form.Set("paused", "true")
	}
	if rename := stringArg(args, "rename"); rename != "" {
		form.Set("rename", rename)
	}
	if _, err := client.do(ctx, http.MethodPost, "/api/v2/torrents/add", form, nil); err != nil {
		return "", err
	}

	// 识别本次新增的任务。
	var added []qbTorrent
	if data, err := client.do(ctx, http.MethodGet, "/api/v2/torrents/info", nil, nil); err == nil {
		var all []qbTorrent
		_ = json.Unmarshal(data, &all)
		for _, t := range all {
			if !before[t.Hash] {
				added = append(added, t)
			}
		}
	}
	if len(added) == 0 {
		return fmt.Sprintf("已提交 %d 个链接到 qBittorrent（可能已在任务列表中）", len(clean)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "已添加 %d 个下载任务：\n", len(added))
	for _, t := range added {
		isPT := p.qbIsPT(ctx, client, t)
		ratio := p.cfg.QBittorrent.DefaultShareRatio
		seedLimit := -2.0 // 做种时间用全局默认
		label := fmt.Sprintf("默认·分享率 %s", formatRatioLimit(ratio))
		if isPT {
			ratio = -1 // 不限分享率
			seedLimit = -1
			label = "PT 白名单·不限上传"
		}
		if err := client.setShareLimits(ctx, []string{t.Hash}, ratio, seedLimit); err != nil {
			fmt.Fprintf(&b, "- %s\n  分享率设置失败：%v\n", t.Name, err)
			continue
		}
		fmt.Fprintf(&b, "- %s\n  hash=%s 判定=%s\n", t.Name, shortHash(t.Hash), label)
	}
	return b.String(), nil
}

// qbIsPT 根据任务名与 tracker URL 是否命中配置白名单关键词，判定是否为 PT 资源。
func (p *plugin) qbIsPT(ctx context.Context, client *qbClient, t qbTorrent) bool {
	if len(p.cfg.QBittorrent.PTKeywords) == 0 {
		return false
	}
	name := strings.ToLower(t.Name)
	// 磁力链接解析 tracker 可能需要一点时间，轮询最多 6 秒。
	var trackerURLs []string
	for i := 0; i < 12; i++ {
		if data, err := client.do(ctx, http.MethodGet, "/api/v2/torrents/trackers", nil, url.Values{"hash": []string{t.Hash}}); err == nil {
			var trackers []qbTracker
			_ = json.Unmarshal(data, &trackers)
			for _, tr := range trackers {
				u := strings.TrimSpace(tr.URL)
				// 过滤 DHT/PeX 等伪 tracker（URL 形如 "** [DHT] **"）。
				if u != "" && !strings.HasPrefix(u, "**") {
					trackerURLs = append(trackerURLs, u)
				}
			}
			if len(trackerURLs) > 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	for _, kw := range p.cfg.QBittorrent.PTKeywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		if strings.Contains(name, kw) {
			return true
		}
		for _, u := range trackerURLs {
			if strings.Contains(strings.ToLower(u), kw) {
				return true
			}
		}
	}
	return false
}

func (p *plugin) qbList(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	if err := client.login(ctx); err != nil {
		return "", err
	}
	query := url.Values{}
	if filter := stringArg(args, "filter"); filter != "" {
		query.Set("filter", filter)
	}
	if category := stringArg(args, "category"); category != "" {
		query.Set("category", category)
	}
	if tag := stringArg(args, "tag"); tag != "" {
		query.Set("tag", tag)
	}
	if limit := numberArg(args, "limit", 50); limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	data, err := client.do(ctx, http.MethodGet, "/api/v2/torrents/info", nil, query)
	if err != nil {
		return "", err
	}
	var list []qbTorrent
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("解析任务列表失败: %w", err)
	}
	if len(list) == 0 {
		return "qBittorrent 当前没有匹配的任务", nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].AddedOn < list[j].AddedOn })

	var b strings.Builder
	fmt.Fprintf(&b, "qBittorrent 任务（%d 个）：\n", len(list))
	maxShow := 30
	if len(list) > maxShow {
		fmt.Fprintf(&b, "（仅显示前 %d 个，可用 filter/limit 缩小范围）\n", maxShow)
		list = list[:maxShow]
	}
	for i, t := range list {
		fmt.Fprintf(&b, "%d. %s\n", i+1, t.Name)
		fmt.Fprintf(&b, "   hash=%s 状态=%s 进度=%.1f%% 分享率=%.2f\n", shortHash(t.Hash), t.State, t.Progress*100, t.Ratio)
		fmt.Fprintf(&b, "   大小=%s 已下载=%s 已上传=%s 做种时间=%s\n",
			formatBytes(t.Size), formatBytes(t.Downloaded), formatBytes(t.Uploaded), formatDuration(t.SeedingTime))
		if t.Tracker != "" {
			fmt.Fprintf(&b, "   tracker=%s\n", t.Tracker)
		}
	}
	return b.String(), nil
}

func (p *plugin) qbStatus(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hash := strings.TrimSpace(stringArg(args, "hash"))
	if hash == "" {
		return "", fmt.Errorf("status 需要提供 hash")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	data, err := client.do(ctx, http.MethodGet, "/api/v2/torrents/info", nil, url.Values{"hashes": []string{hash}})
	if err != nil {
		return "", err
	}
	var list []qbTorrent
	_ = json.Unmarshal(data, &list)
	if len(list) == 0 {
		return fmt.Sprintf("未找到任务 %s", hash), nil
	}
	t := list[0]

	var b strings.Builder
	fmt.Fprintf(&b, "任务：%s\n", t.Name)
	fmt.Fprintf(&b, "hash：%s\n", t.Hash)
	fmt.Fprintf(&b, "状态：%s（进度 %.1f%%）\n", t.State, t.Progress*100)
	fmt.Fprintf(&b, "大小：%s 已下载：%s 已上传：%s 分享率：%.2f\n",
		formatBytes(t.Size), formatBytes(t.Downloaded), formatBytes(t.Uploaded), t.Ratio)
	fmt.Fprintf(&b, "下载速度：%s/s 上传速度：%s/s 做种时间：%s\n",
		formatBytes(t.DownloadSpeed), formatBytes(t.UploadSpeed), formatDuration(t.SeedingTime))
	fmt.Fprintf(&b, "做种数：%d 下载数：%d 保存路径：%s\n", t.NumSeeds, t.NumLeechs, t.SavePath)
	if t.Category != "" {
		fmt.Fprintf(&b, "分类：%s", t.Category)
	}
	if t.Tags != "" {
		fmt.Fprintf(&b, " 标签：%s", t.Tags)
	}
	b.WriteString("\n")

	// 附加 tracker 信息。
	if data, err := client.do(ctx, http.MethodGet, "/api/v2/torrents/trackers", nil, url.Values{"hash": []string{hash}}); err == nil {
		var trackers []qbTracker
		_ = json.Unmarshal(data, &trackers)
		for _, tr := range trackers {
			u := strings.TrimSpace(tr.URL)
			if u == "" || strings.HasPrefix(u, "**") {
				continue
			}
			b.WriteString("tracker: " + u)
			if tr.NumSeeds > 0 || tr.NumPeers > 0 {
				fmt.Fprintf(&b, "（S%d/P%d）", tr.NumSeeds, tr.NumPeers)
			}
			if strings.TrimSpace(tr.Msg) != "" {
				fmt.Fprintf(&b, " %s", strings.TrimSpace(tr.Msg))
			}
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func (p *plugin) qbPauseResume(ctx context.Context, client *qbClient, args map[string]interface{}, pause bool) (string, error) {
	hashes := p.qbHashesArg(args)
	if len(hashes) == 0 {
		return "", fmt.Errorf("hashes 必填（至少一个）")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	path := "/api/v2/torrents/start"
	verb := "已恢复"
	if pause {
		path = "/api/v2/torrents/stop"
		verb = "已暂停"
	}
	if _, err := client.do(ctx, http.MethodPost, path, client.hashesForm(hashes), nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %d 个任务：%s", verb, len(hashes), strings.Join(hashes, ", ")), nil
}

func (p *plugin) qbDelete(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hashes := p.qbHashesArg(args)
	if len(hashes) == 0 {
		return "", fmt.Errorf("hashes 必填（至少一个）")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	form := client.hashesForm(hashes)
	if boolArg(args, "delete_files", false) {
		form.Set("deleteFiles", "true")
	}
	if _, err := client.do(ctx, http.MethodPost, "/api/v2/torrents/delete", form, nil); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("已删除 %d 个任务：%s", len(hashes), strings.Join(hashes, ", "))
	if boolArg(args, "delete_files", false) {
		msg += "（已同时删除本地文件）"
	}
	return msg, nil
}

func (p *plugin) qbShareLimit(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hashes := p.qbHashesArg(args)
	if len(hashes) == 0 {
		return "", fmt.Errorf("hashes 必填（至少一个）")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	ratio := floatArg(args, "ratio_limit", -1)            // -1 不限
	seedLimit := floatArg(args, "seeding_time_limit", -2) // -2 用全局
	if err := client.setShareLimits(ctx, hashes, ratio, seedLimit); err != nil {
		return "", err
	}
	return fmt.Sprintf("已设置 %d 个任务分享率限制=%s、做种时间限制=%s：%s",
		len(hashes), formatRatioLimit(ratio), formatFloat(seedLimit), strings.Join(hashes, ", ")), nil
}

func (p *plugin) qbReannounce(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hashes := p.qbHashesArg(args)
	if len(hashes) == 0 {
		return "", fmt.Errorf("hashes 必填（至少一个）")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	if _, err := client.do(ctx, http.MethodPost, "/api/v2/torrents/reannounce", client.hashesForm(hashes), nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("已重新宣告 %d 个任务：%s", len(hashes), strings.Join(hashes, ", ")), nil
}

func (p *plugin) qbHashesArg(args map[string]interface{}) []string {
	hashes := stringSliceArg(args, "hashes")
	if len(hashes) == 0 {
		if h := stringArg(args, "hash"); h != "" {
			hashes = []string{h}
		}
	}
	return hashes
}

func floatArg(args map[string]interface{}, key string, fallback float64) float64 {
	if args == nil || args[key] == nil {
		return fallback
	}
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(v, "%g", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func formatRatioLimit(v float64) string {
	if v < 0 {
		return "不限"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

func formatBytes(v int64) string {
	if v < 0 {
		v = 0
	}
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := int64(unit), 0
	for n := v / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
}

func formatDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%dh%dm", seconds/3600, (seconds%3600)/60)
	}
	return fmt.Sprintf("%dd%dh", seconds/86400, (seconds%86400)/3600)
}
