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

func (c *qbClient) setShareLimits(ctx context.Context, hashes []string, ratioLimit, seedingTimeLimit, inactiveSeedingTimeLimit float64) error {
	form := url.Values{}
	form.Set("hashes", strings.Join(hashes, "|"))
	form.Set("ratioLimit", formatFloat(ratioLimit))
	form.Set("seedingTimeLimit", formatFloat(seedingTimeLimit))
	form.Set("inactiveSeedingTimeLimit", formatFloat(inactiveSeedingTimeLimit))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/setShareLimits", form, nil)
	return err
}

func (c *qbClient) hashesForm(hashes []string) url.Values {
	form := url.Values{}
	form.Set("hashes", strings.Join(hashes, "|"))
	return form
}

func (c *qbClient) listFiles(ctx context.Context, hash string) ([]qbFile, error) {
	data, err := c.do(ctx, http.MethodGet, "/api/v2/torrents/files", nil, url.Values{"hash": []string{hash}})
	if err != nil {
		return nil, err
	}
	var files []qbFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("解析文件列表失败: %w", err)
	}
	return files, nil
}

func (c *qbClient) setFilePriorities(ctx context.Context, hash string, ids []int, priority int) error {
	if len(ids) == 0 {
		return nil
	}
	form := url.Values{}
	form.Set("hash", hash)
	idStrs := make([]string, 0, len(ids))
	for _, id := range ids {
		idStrs = append(idStrs, strconv.Itoa(id))
	}
	form.Set("id", strings.Join(idStrs, ","))
	form.Set("priority", strconv.Itoa(priority))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/filePrio", form, nil)
	return err
}

func (c *qbClient) setLocation(ctx context.Context, hashes []string, location string) error {
	form := url.Values{}
	form.Set("hashes", strings.Join(hashes, "|"))
	form.Set("location", location)
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/setLocation", form, nil)
	return err
}

func (c *qbClient) setCategory(ctx context.Context, hashes []string, category string) error {
	form := url.Values{}
	form.Set("hashes", strings.Join(hashes, "|"))
	form.Set("category", category)
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/setCategory", form, nil)
	return err
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

// qbFile 对应 /api/v2/torrents/files 的元素。
type qbFile struct {
	Index        int     `json:"index"`
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	Progress     float64 `json:"progress"`
	Priority     int     `json:"priority"`
	Availability float64 `json:"availability"`
	IsSeed       bool    `json:"is_seed"`
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
	case "files":
		return p.qbFiles(ctx, client, args)
	case "file_prio":
		return p.qbFilePrio(ctx, client, args)
	case "move":
		return p.qbMove(ctx, client, args)
	case "set_category":
		return p.qbSetCategory(ctx, client, args)
	default:
		return "", fmt.Errorf("未知 qbittorrent 动作：%s（支持 add/list/status/pause/resume/delete/share_limit/reannounce/files/file_prio/move/set_category）", action)
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
	if savePath := p.qbResolveSavePath(args); savePath != "" {
		form.Set("savepath", savePath)
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
		inactiveSeedLimit := -2.0 // 非活跃做种时间用全局默认
		label := fmt.Sprintf("默认·分享率 %s", formatRatioLimit(ratio))
		if isPT {
			ratio = -1 // 不限分享率
			seedLimit = -1
			inactiveSeedLimit = -1
			label = "PT 白名单·不限上传"
		}
		if err := client.setShareLimits(ctx, []string{t.Hash}, ratio, seedLimit, inactiveSeedLimit); err != nil {
			fmt.Fprintf(&b, "- %s\n  分享率设置失败：%v\n", t.Name, err)
			continue
		}
		fmt.Fprintf(&b, "- %s\n  hash=%s 判定=%s\n", t.Name, t.Hash, label)
		if !isPT {
			fmt.Fprintf(&b, "  提示：非 PT 公开种子，建议用 files 查看文件列表，用 file_prio 剔除广告/推广文件（如 *.url、广告*.txt、*推广*.exe 等）后，再 resume 开始下载。\n")
		}
		if boolArg(args, "list_files", false) {
			p.qbAppendFileList(ctx, client, t.Hash, &b)
		}
	}
	return b.String(), nil
}

// qbResolveSavePath 解析保存路径：优先 save_path（完整路径），其次 subdirectory（默认目录下的子目录），最后默认目录。
func (p *plugin) qbResolveSavePath(args map[string]interface{}) string {
	if savePath := strings.TrimSpace(stringArg(args, "save_path")); savePath != "" {
		return savePath
	}
	base := strings.TrimSpace(p.cfg.QBittorrent.DefaultSavePath)
	if sub := strings.TrimSpace(stringArg(args, "subdirectory")); sub != "" {
		return joinPath(base, sub)
	}
	return base
}

// qbAppendFileList 把任务文件列表追加到输出，供 Agent 决定文件取舍。
func (p *plugin) qbAppendFileList(ctx context.Context, client *qbClient, hash string, b *strings.Builder) {
	files, err := client.listFiles(ctx, hash)
	if err != nil || len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "  文件列表（%d 个）：\n", len(files))
	maxShow := 40
	if len(files) > maxShow {
		fmt.Fprintf(b, "  （仅显示前 %d 个，可用 files 查看完整列表）\n", maxShow)
		files = files[:maxShow]
	}
	for _, f := range files {
		fmt.Fprintf(b, "    [%d] %s（%s）\n", f.Index, f.Name, formatBytes(f.Size))
	}
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
	inactiveSeedLimit := floatArg(args, "inactive_seeding_time_limit", -2) // -2 用全局
	if err := client.setShareLimits(ctx, hashes, ratio, seedLimit, inactiveSeedLimit); err != nil {
		return "", err
	}
	return fmt.Sprintf("已设置 %d 个任务分享率限制=%s、做种时间限制=%s、非活跃做种时间限制=%s：%s",
		len(hashes), formatRatioLimit(ratio), formatFloat(seedLimit), formatFloat(inactiveSeedLimit), strings.Join(hashes, ", ")), nil
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

// qbFiles 列出任务文件，供 Agent 根据文件名/大小决定取舍。
func (p *plugin) qbFiles(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hash := strings.TrimSpace(stringArg(args, "hash"))
	if hash == "" {
		return "", fmt.Errorf("files 需要提供 hash")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	files, err := client.listFiles(ctx, hash)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return fmt.Sprintf("任务 %s 暂无文件信息（种子元数据可能尚未就绪，可稍后重试）", shortHash(hash)), nil
	}
	limit := numberArg(args, "limit", 200)
	var b strings.Builder
	fmt.Fprintf(&b, "任务 %s 文件列表（%d 个）：\n", shortHash(hash), len(files))
	shown := 0
	for _, f := range files {
		if limit > 0 && shown >= limit {
			fmt.Fprintf(&b, "（已省略剩余 %d 个文件）\n", len(files)-shown)
			break
		}
		fmt.Fprintf(&b, "  [%d] %s（%s）优先级=%d\n", f.Index, f.Name, formatBytes(f.Size), f.Priority)
		shown++
	}
	return b.String(), nil
}

// qbFilePrio 设置文件下载优先级：exclude 表示不下载的文件索引，include 表示仅下载的文件索引。
func (p *plugin) qbFilePrio(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hash := strings.TrimSpace(stringArg(args, "hash"))
	if hash == "" {
		return "", fmt.Errorf("file_prio 需要提供 hash")
	}
	exclude := intSliceArg(args, "exclude")
	include := intSliceArg(args, "include")
	if len(exclude) == 0 && len(include) == 0 {
		return "", fmt.Errorf("file_prio 需要提供 exclude（不下载的文件索引）或 include（仅下载的文件索引）")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	files, err := client.listFiles(ctx, hash)
	if err != nil {
		return "", err
	}
	exists := make(map[int]bool, len(files))
	all := make([]int, 0, len(files))
	for _, f := range files {
		exists[f.Index] = true
		all = append(all, f.Index)
	}
	valid := func(ids []int) []int {
		out := make([]int, 0, len(ids))
		for _, id := range ids {
			if exists[id] {
				out = append(out, id)
			}
		}
		return out
	}

	if len(exclude) > 0 {
		exclude = valid(exclude)
		if len(exclude) == 0 {
			return "", fmt.Errorf("exclude 中的文件索引不存在")
		}
		if err := client.setFilePriorities(ctx, hash, exclude, 0); err != nil {
			return "", err
		}
		return fmt.Sprintf("已设置任务 %s 的 %d 个文件不下载：%v", shortHash(hash), len(exclude), exclude), nil
	}

	include = valid(include)
	if len(include) == 0 {
		return "", fmt.Errorf("include 中的文件索引不存在")
	}
	// 先全部置为不下载，再仅保留 include 中的文件。
	if err := client.setFilePriorities(ctx, hash, all, 0); err != nil {
		return "", err
	}
	if err := client.setFilePriorities(ctx, hash, include, 1); err != nil {
		return "", err
	}
	return fmt.Sprintf("已设置任务 %s 仅下载 %d 个文件：%v（其余 %d 个不下载）",
		shortHash(hash), len(include), include, len(all)-len(include)), nil
}

// qbMove 移动任务到指定子目录（可同时设置分类）。
func (p *plugin) qbMove(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hashes := p.qbHashesArg(args)
	if len(hashes) == 0 {
		return "", fmt.Errorf("hashes 必填（至少一个）")
	}
	savePath := p.qbResolveSavePath(args)
	if savePath == "" {
		return "", fmt.Errorf("move 需要提供 save_path 或 subdirectory（或在配置中设置 defaultSavePath）")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	if err := client.setLocation(ctx, hashes, savePath); err != nil {
		return "", err
	}
	if category := stringArg(args, "category"); category != "" {
		if err := client.setCategory(ctx, hashes, category); err != nil {
			return "", fmt.Errorf("移动成功但设置分类失败：%w", err)
		}
	}
	return fmt.Sprintf("已将 %d 个任务移动到 %s", len(hashes), savePath), nil
}

// qbSetCategory 设置任务分类。
func (p *plugin) qbSetCategory(ctx context.Context, client *qbClient, args map[string]interface{}) (string, error) {
	hashes := p.qbHashesArg(args)
	if len(hashes) == 0 {
		return "", fmt.Errorf("hashes 必填（至少一个）")
	}
	category := stringArg(args, "category")
	if category == "" {
		return "", fmt.Errorf("set_category 需要提供 category")
	}
	if err := client.login(ctx); err != nil {
		return "", err
	}
	if err := client.setCategory(ctx, hashes, category); err != nil {
		return "", err
	}
	return fmt.Sprintf("已将 %d 个任务设置为分类 %s", len(hashes), category), nil
}

// intSliceArg 解析 JSON 数字/字符串数组为 int 切片。
func intSliceArg(args map[string]interface{}, key string) []int {
	if args == nil || args[key] == nil {
		return nil
	}
	values, ok := args[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		switch v := value.(type) {
		case float64:
			result = append(result, int(v))
		case int:
			result = append(result, v)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				result = append(result, n)
			}
		}
	}
	return result
}

// joinPath 用正斜杠拼接路径，避免 Windows/Linux 分隔符差异（qBittorrent 侧统一为斜杠）。
func joinPath(base, sub string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/\\")
	sub = strings.Trim(strings.TrimSpace(sub), "/\\")
	if base == "" {
		return sub
	}
	if sub == "" {
		return base
	}
	return base + "/" + sub
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
