package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type ehReqClient struct {
	plugin *plugin
}

type ehReqResult struct {
	OK           bool              `json:"ok"`
	Site         string            `json:"site"`
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	StatusCode   int               `json:"statusCode"`
	ContentType  string            `json:"contentType,omitempty"`
	CookieLoaded bool              `json:"cookieLoaded"`
	ProxyEnabled bool              `json:"proxyEnabled"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         string            `json:"body,omitempty"`
	Truncated    bool              `json:"truncated,omitempty"`
}

func (p *plugin) callEHReqSearch(args map[string]interface{}) (string, error) {
	client := ehReqClient{plugin: p}
	site := stringArg(args, "site")
	keyword := stringArg(args, "keyword")
	page := numberArg(args, "page", 0)
	advanced := boolArg(args, "advanced", false)
	query := url.Values{}
	if keyword != "" {
		query.Set("f_search", keyword)
	}
	if page > 0 {
		query.Set("page", strconv.Itoa(page))
		query.Set("p", strconv.Itoa(page))
	}
	if advanced {
		query.Set("s_act", "advanced")
	}
	for key, value := range stringMapArg(args, "query") {
		if isForbiddenEHQueryKey(key) {
			continue
		}
		query.Set(key, value)
	}
	return client.doJSON(context.Background(), site, http.MethodGet, "/", query, nil, "")
}

func (p *plugin) callEHReqGallery(args map[string]interface{}) (string, error) {
	client := ehReqClient{plugin: p}
	site := stringArg(args, "site")
	gid := numberArg(args, "gid", 0)
	token := stringArg(args, "token")
	if gid <= 0 || token == "" {
		return "", fmt.Errorf("gid 和 token 不能为空")
	}
	query := url.Values{}
	page := numberArg(args, "page", 0)
	if page > 0 {
		query.Set("p", strconv.Itoa(page))
	}
	if boolArg(args, "show_comments", true) {
		query.Set("hc", "1")
	}
	path := fmt.Sprintf("/g/%d/%s/", gid, url.PathEscape(token))
	return client.doJSON(context.Background(), site, http.MethodGet, path, query, nil, "")
}

func (p *plugin) callEHReqAPI(args map[string]interface{}) (string, error) {
	client := ehReqClient{plugin: p}
	site := stringArg(args, "site")
	payload := mapArg(args, "payload")
	if len(payload) == 0 {
		method := stringArg(args, "method")
		if method == "" {
			return "", fmt.Errorf("payload 或 method 不能为空")
		}
		payload = map[string]interface{}{"method": method}
		if text := stringArg(args, "text"); text != "" {
			payload["text"] = text
		}
		if gidlist, ok := args["gidlist"]; ok && gidlist != nil {
			payload["gidlist"] = gidlist
		}
		if namespace := numberArg(args, "namespace", 0); namespace > 0 {
			payload["namespace"] = namespace
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return client.doJSON(context.Background(), site, http.MethodPost, "/api.php", nil, bytes.NewReader(body), "application/json")
}

func (p *plugin) callEHReqImagePage(args map[string]interface{}) (string, error) {
	client := ehReqClient{plugin: p}
	site := stringArg(args, "site")
	imageHash := stringArg(args, "image_hash")
	gid := numberArg(args, "gid", 0)
	pageNo := numberArg(args, "page_no", 0)
	if imageHash == "" || gid <= 0 || pageNo <= 0 {
		return "", fmt.Errorf("image_hash、gid 和 page_no 不能为空")
	}
	query := url.Values{}
	if reloadKey := stringArg(args, "reload_key"); reloadKey != "" {
		query.Set("nl", reloadKey)
	}
	path := fmt.Sprintf("/s/%s/%d-%d", url.PathEscape(imageHash), gid, pageNo)
	return client.doJSON(context.Background(), site, http.MethodGet, path, query, nil, "")
}

func (c ehReqClient) doJSON(ctx context.Context, site string, method string, path string, query url.Values, body io.Reader, contentType string) (string, error) {
	result, err := c.do(ctx, site, method, path, query, body, contentType)
	if err != nil {
		return "", err
	}
	return renderJSON(result)
}

func (c ehReqClient) do(ctx context.Context, site string, method string, path string, query url.Values, body io.Reader, contentType string) (ehReqResult, error) {
	if !c.plugin.cfg.EHReq.Enabled {
		return ehReqResult{}, fmt.Errorf("EH 请求工具未启用")
	}
	baseURL, normalizedSite, err := ehSiteBaseURL(site)
	if err != nil {
		return ehReqResult{}, err
	}
	requestURL, err := url.Parse(baseURL + path)
	if err != nil {
		return ehReqResult{}, err
	}
	if query != nil {
		requestURL.RawQuery = query.Encode()
	}
	if err := validateEHURL(requestURL); err != nil {
		return ehReqResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return ehReqResult{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.applyHeaders(req)
	cookie := c.loadCookie()
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	httpClient, proxyEnabled, err := c.httpClient()
	if err != nil {
		return ehReqResult{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return ehReqResult{}, err
	}
	defer resp.Body.Close()
	maxBodyChars := c.plugin.cfg.EHReq.MaxBodyChars
	if maxBodyChars <= 0 {
		maxBodyChars = 200000
	}
	limited := io.LimitReader(resp.Body, int64(maxBodyChars)+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return ehReqResult{}, err
	}
	truncated := len([]rune(string(respBody))) > maxBodyChars
	bodyText := string(respBody)
	if truncated {
		bodyText = truncate(bodyText, maxBodyChars)
	}
	return ehReqResult{
		OK:           resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices,
		Site:         normalizedSite,
		Method:       method,
		URL:          requestURL.String(),
		StatusCode:   resp.StatusCode,
		ContentType:  resp.Header.Get("Content-Type"),
		CookieLoaded: cookie != "",
		ProxyEnabled: proxyEnabled,
		Headers:      safeEHResponseHeaders(resp.Header),
		Body:         bodyText,
		Truncated:    truncated,
	}, nil
}

func (c ehReqClient) httpClient() (*http.Client, bool, error) {
	proxyURL := c.proxyURL()
	if proxyURL == "" {
		return c.plugin.httpClient, false, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, false, fmt.Errorf("EH 代理地址无效: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(parsed)
	timeout := time.Duration(c.plugin.cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: transport}, true, nil
}

func (c ehReqClient) proxyURL() string {
	if proxyURL := strings.TrimSpace(c.plugin.cfg.EHReq.ProxyURL); proxyURL != "" {
		return proxyURL
	}
	if envName := strings.TrimSpace(c.plugin.cfg.EHReq.ProxyEnv); envName != "" {
		return strings.TrimSpace(os.Getenv(envName))
	}
	return ""
}

func (c ehReqClient) applyHeaders(req *http.Request) {
	userAgent := strings.TrimSpace(c.plugin.cfg.EHReq.UserAgent)
	if userAgent == "" {
		userAgent = "Mozilla/5.0"
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,*/*;q=0.7")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if req.Header.Get("Referer") == "" {
		req.Header.Set("Referer", ehRefererForHost(req.URL.Host))
	}
}

func (c ehReqClient) loadCookie() string {
	if cookie := normalizeEHCookie(c.plugin.cfg.EHReq.Cookie); cookie != "" {
		return cookie
	}
	if envName := strings.TrimSpace(c.plugin.cfg.EHReq.CookieEnv); envName != "" {
		if cookie := normalizeEHCookie(os.Getenv(envName)); cookie != "" {
			return cookie
		}
	}
	path := strings.TrimSpace(c.plugin.cfg.EHReq.CookiePath)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return normalizeEHCookie(string(data))
}

func normalizeEHCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "{") {
		var values map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &values); err == nil {
			parts := make([]string, 0, len(values))
			for _, key := range []string{"ipb_member_id", "ipb_pass_hash", "igneous", "sk"} {
				if value := strings.TrimSpace(fmt.Sprint(values[key])); value != "" && value != "<nil>" {
					parts = append(parts, key+"="+value)
				}
			}
			return strings.Join(parts, "; ")
		}
	}
	if strings.Contains(raw, "\t") {
		return parseNetscapeCookie(raw)
	}
	lines := strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' })
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "=") {
			parts = append(parts, strings.TrimRight(line, ";"))
		}
	}
	return strings.Join(parts, "; ")
}

func parseNetscapeCookie(raw string) string {
	parts := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := fields[0]
		if !strings.Contains(domain, "e-hentai.org") && !strings.Contains(domain, "exhentai.org") {
			continue
		}
		name := strings.TrimSpace(fields[5])
		value := strings.TrimSpace(fields[6])
		if name != "" && value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	return strings.Join(parts, "; ")
}

func ehSiteBaseURL(site string) (string, string, error) {
	site = strings.ToUpper(strings.TrimSpace(site))
	switch site {
	case "", "EX", "EXHENTAI":
		return "https://exhentai.org", "EX", nil
	case "EH", "E-HENTAI", "EHENTAI":
		return "https://e-hentai.org", "EH", nil
	default:
		return "", "", fmt.Errorf("未知 EH 站点：%s", site)
	}
}

func validateEHURL(value *url.URL) error {
	if value == nil || value.Scheme != "https" {
		return fmt.Errorf("只允许 HTTPS EH/EX 请求")
	}
	host := strings.ToLower(value.Hostname())
	if host == "e-hentai.org" || host == "exhentai.org" || host == "api.e-hentai.org" {
		return nil
	}
	return fmt.Errorf("不允许请求非 EH/EX 域名：%s", host)
}

func ehRefererForHost(host string) string {
	host = strings.ToLower(host)
	if strings.Contains(host, "exhentai.org") {
		return "https://exhentai.org/"
	}
	return "https://e-hentai.org/"
}

func safeEHResponseHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)
	for _, key := range []string{"Content-Type", "Content-Length", "Location", "X-Frame-Options"} {
		if value := headers.Get(key); value != "" {
			result[key] = value
		}
	}
	return result
}

func stringMapArg(args map[string]interface{}, key string) map[string]string {
	result := make(map[string]string)
	if args == nil || args[key] == nil {
		return result
	}
	values, ok := args[key].(map[string]interface{})
	if !ok {
		return result
	}
	for k, v := range values {
		text := strings.TrimSpace(fmt.Sprint(v))
		if strings.TrimSpace(k) != "" && text != "" {
			result[strings.TrimSpace(k)] = text
		}
	}
	return result
}

func mapArg(args map[string]interface{}, key string) map[string]interface{} {
	if args == nil || args[key] == nil {
		return nil
	}
	values, ok := args[key].(map[string]interface{})
	if !ok {
		return nil
	}
	return values
}

func isForbiddenEHQueryKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "cookie" || key == "set-cookie" || key == "authorization"
}
