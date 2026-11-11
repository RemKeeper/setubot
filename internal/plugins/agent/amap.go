package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	zero "github.com/wdvxdr1123/ZeroBot"
)

// 高德 Web 服务工具：路线规划、地点检索、地区信息与静态地图。
//
// 设计约定：
//   - 只走 restapi.amap.com（Web 服务 Key），全链路 GCJ-02 坐标；
//   - 起终点既接受 "经度,纬度" 也接受中文地名，内部自动地理编码；
//   - 地名命中多个候选或与 city 提示不符时，在结果里显式给出歧义提示；
//   - 所有失败都返回中文可执行建议，避免 agent 在错误上继续编。

const (
	amapDefaultBaseURL = "https://restapi.amap.com"
	amapMaxSteps       = 12
	amapMaxPOI         = 10
)

// amapCacheEntry 是高德 GET 结果的 TTL 缓存项。
type amapCacheEntry struct {
	expires time.Time
	payload map[string]interface{}
}

// amapError 携带高德错误码与可执行建议。
type amapError struct {
	Info     string
	Infocode string
	Path     string
	Hint     string
}

func (e *amapError) Error() string {
	parts := []string{"高德接口返回错误：" + fallbackText(e.Info, "未知错误")}
	if e.Path != "" {
		parts = append(parts, "path="+e.Path)
	}
	if e.Infocode != "" {
		parts = append(parts, "infocode="+e.Infocode)
	}
	if e.Hint != "" {
		parts = append(parts, "建议："+e.Hint)
	}
	return strings.Join(parts, " | ")
}

var amapErrorHints = map[string]string{
	"10001": "Key 无效：确认 agent.amap.apiKey 或 AMAP_API_KEY 是否为空、是否已被删除。",
	"10002": "服务不可用：该 Key 未开通此接口，或接口路径已变更。",
	"10003": "日调用量已超限：换 Key 或次日再试。",
	"10004": "访问过于频繁（QPS 超限）：降低并发后重试。",
	"10005": "IP 白名单不匹配：控制台清空白名单，或加入当前出口 IP。",
	"10006": "绑定域名不匹配：Web 服务 Key 不应绑定域名/包名。",
	"10007": "数字签名校验失败：服务端未开启强制签名时不要带 sig。",
	"10009": "Key 平台类型不匹配：该 Key 不是「Web服务」类型。到 https://console.amap.com/dev/key/app 新建 Key，服务平台选「Web服务」后替换。",
	"10010": "IP 访问超限：降低频率或申请更高配额。",
	"10012": "权限不足：该 Key 未开通对应服务。",
	"10013": "Key 已被禁用。",
	"10017": "接口路径不存在：核对 path 是否为高德真实接口。",
	"10019": "高德服务端异常：稍后重试。",
	"10020": "高德服务未知错误：稍后重试。",
	"10021": "QPS 超限：请降低调用频率。",
	"10026": "账号欠费或未开通该服务。",
	"10029": "并发/QPS 超限：稍后重试。",
	"10041": "缺少必填参数。",
	"10044": "账号维度日调用量超限。",
	"20000": "请求参数错误：检查参数格式；路况矩形跨度建议 ≤0.05°。",
	"20001": "缺少必填参数。",
	"20003": "参数格式非法或道路未识别：道路名换主干道，或改用中心点查询。",
	"20800": "请求路径/参数不匹配。",
	"20802": "服务不支持该能力。",
}

func amapHint(infocode string) string {
	if hint, ok := amapErrorHints[strings.TrimSpace(infocode)]; ok {
		return hint
	}
	return "参考高德错误码文档，或到 https://console.amap.com/dev/key/app 检查 Key 配置。"
}

// amapGuard 统一入口校验：启用开关 + ownerOnly 鉴权。
// 鉴权在工具层完成，不依赖模型判断。
func (p *plugin) amapGuard(zc *zero.Ctx) error {
	if !p.cfg.Amap.Enabled {
		return fmt.Errorf("高德工具未启用，请在 agent.amap.enabled 中开启")
	}
	if p.cfg.Amap.OwnerOnly && (zc == nil || !p.isSuperUser(zc.Event.UserID)) {
		return fmt.Errorf("高德工具仅主人可用")
	}
	return nil
}

// ---------------------------------------------------------------- 客户端

func (p *plugin) amapKey() (string, error) {
	key := strings.TrimSpace(p.cfg.Amap.APIKey)
	envName := strings.TrimSpace(p.cfg.Amap.APIKeyEnv)
	if envName == "" {
		envName = "AMAP_API_KEY"
	}
	if key == "" {
		key = strings.TrimSpace(os.Getenv(envName))
	}
	if key == "" {
		return "", fmt.Errorf("高德 API Key 未配置：请在 agent.amap.apiKey 或环境变量 %s 中设置（必须是高德控制台的「Web服务」类型 Key）", envName)
	}
	return key, nil
}

func (p *plugin) amapBaseURL() string {
	base := strings.TrimSpace(p.cfg.Amap.BaseURL)
	if base == "" {
		base = amapDefaultBaseURL
	}
	return strings.TrimRight(base, "/")
}

func (p *plugin) amapHTTPClient() *http.Client {
	if p.amapHTTP != nil {
		return p.amapHTTP
	}
	timeout := p.cfg.Amap.Timeout
	if timeout <= 0 {
		timeout = 10
	}
	return &http.Client{Timeout: time.Duration(timeout) * time.Second}
}

func (p *plugin) amapCacheLoad(key string) map[string]interface{} {
	p.amapCacheM.Lock()
	defer p.amapCacheM.Unlock()
	entry, ok := p.amapCache[key]
	if !ok {
		return nil
	}
	if time.Now().After(entry.expires) {
		delete(p.amapCache, key)
		return nil
	}
	return entry.payload
}

func (p *plugin) amapCacheStore(key string, payload map[string]interface{}, ttl time.Duration) {
	p.amapCacheM.Lock()
	defer p.amapCacheM.Unlock()
	if p.amapCache == nil {
		p.amapCache = make(map[string]amapCacheEntry)
	}
	if len(p.amapCache) >= 512 {
		now := time.Now()
		for k, v := range p.amapCache {
			if now.After(v.expires) {
				delete(p.amapCache, k)
			}
		}
		if len(p.amapCache) >= 512 {
			p.amapCache = make(map[string]amapCacheEntry)
		}
	}
	p.amapCache[key] = amapCacheEntry{expires: time.Now().Add(ttl), payload: payload}
}

// amapGet 调用一个高德 GET 接口，返回已校验成功的 JSON。
func (p *plugin) amapGet(ctx context.Context, path string, params map[string]string) (map[string]interface{}, error) {
	key, err := p.amapKey()
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	for name, value := range params {
		if strings.TrimSpace(value) != "" {
			query.Set(name, value)
		}
	}
	query.Set("key", key)
	query.Set("output", "json")

	cacheKey := path + "?" + query.Encode()
	ttl := p.cfg.Amap.CacheTTL
	if ttl > 0 {
		if cached := p.amapCacheLoad(cacheKey); cached != nil {
			return cached, nil
		}
	}

	endpoint := p.amapBaseURL() + path + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "setubot-amap/1.0")

	resp, err := p.amapHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求高德接口失败：%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取高德响应失败：%w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("高德接口返回 HTTP %d：%s", resp.StatusCode, truncate(string(body), 200))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("高德返回内容不是合法 JSON：%w", err)
	}
	if err := amapCheckPayload(payload, path); err != nil {
		return nil, err
	}
	if ttl > 0 {
		p.amapCacheStore(cacheKey, payload, time.Duration(ttl)*time.Second)
	}
	return payload, nil
}

func amapCheckPayload(payload map[string]interface{}, path string) error {
	if errcode, ok := payload["errcode"]; ok && errcode != nil {
		code := amapString(errcode)
		if code != "" && code != "0" {
			return &amapError{
				Info:     fallbackText(amapString(payload["errmsg"]), "未知错误"),
				Infocode: code,
				Path:     path,
				Hint:     amapHint(code),
			}
		}
	}
	status := amapString(payload["status"])
	if status == "" || status == "1" {
		return nil
	}
	infocode := amapString(payload["infocode"])
	return &amapError{
		Info:     fallbackText(amapString(payload["info"]), "未知错误"),
		Infocode: infocode,
		Path:     path,
		Hint:     amapHint(infocode),
	}
}

// ---------------------------------------------------------------- 基础取值

func amapString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []interface{}:
		// 高德用 [] 表示空值
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func amapFloat(value interface{}) (float64, bool) {
	text := strings.TrimSuffix(amapString(value), "%")
	if text == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func amapInt(value interface{}) (int, bool) {
	parsed, ok := amapFloat(value)
	if !ok {
		return 0, false
	}
	return int(parsed), true
}

func amapMap(value interface{}) map[string]interface{} {
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func amapSlice(value interface{}) []interface{} {
	if s, ok := value.([]interface{}); ok {
		return s
	}
	return nil
}

func amapNum(value float64, digits int) string {
	return strconv.FormatFloat(value, 'f', digits, 64)
}

// amapTrimFloat 保留最多 digits 位小数，并去掉无意义的尾随 0（32.50 -> 32.5，32.0 -> 32）。
func amapTrimFloat(value float64, digits int) string {
	text := strconv.FormatFloat(value, 'f', digits, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(text, "0")
		text = strings.TrimRight(text, ".")
	}
	return text
}

func amapDistanceText(meters float64) string {
	if meters < 1000 {
		return fmt.Sprintf("%d m", int(math.Round(meters)))
	}
	return amapNum(meters/1000, 1) + " km"
}

func amapDurationText(seconds float64) string {
	minutes := int(math.Round(seconds / 60))
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := minutes / 60
	rest := minutes % 60
	if rest == 0 {
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, rest)
}

// ---------------------------------------------------------------- 坐标

func amapParseLngLat(text string) (float64, float64, bool) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(text), "，", ",")
	parts := strings.Split(cleaned, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	lng, errLng := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lat, errLat := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if errLng != nil || errLat != nil {
		return 0, 0, false
	}
	if lng < -180 || lng > 180 || lat < -90 || lat > 90 {
		return 0, 0, false
	}
	return lng, lat, true
}

func amapHaversine(lng1, lat1, lng2, lat2 float64) float64 {
	const earthRadius = 6371008.8
	radLng1 := lng1 * math.Pi / 180
	radLat1 := lat1 * math.Pi / 180
	radLng2 := lng2 * math.Pi / 180
	radLat2 := lat2 * math.Pi / 180
	dLng := radLng2 - radLng1
	dLat := radLat2 - radLat1
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(radLat1)*math.Cos(radLat2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadius * math.Asin(math.Sqrt(h))
}

// amapResolved 是地名解析结果，带歧义信息。
type amapResolved struct {
	Location       string
	Input          string
	MatchedAddress string
	MatchedCity    string
	Candidates     int
	Ambiguous      bool
}

// amapResolve 把 "lng,lat" 或中文地名统一解析成高德要求的 "lng,lat"。
func (p *plugin) amapResolve(ctx context.Context, value string, city string, field string) (amapResolved, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return amapResolved{}, fmt.Errorf("缺少参数 %s", field)
	}
	if lng, lat, ok := amapParseLngLat(text); ok {
		return amapResolved{
			Location:   fmt.Sprintf("%.6f,%.6f", lng, lat),
			Input:      text,
			Candidates: 1,
		}, nil
	}

	payload, err := p.amapGet(ctx, "/v3/geocode/geo", map[string]string{
		"address": text,
		"city":    city,
	})
	if err != nil {
		return amapResolved{}, err
	}
	geocodes := amapSlice(payload["geocodes"])
	if len(geocodes) == 0 {
		return amapResolved{}, fmt.Errorf("无法解析地名「%s」为坐标，请换成更完整的地址，或直接提供 \"经度,纬度\"", text)
	}
	first := amapMap(geocodes[0])
	location := amapString(first["location"])
	if location == "" {
		return amapResolved{}, fmt.Errorf("地名「%s」没有返回坐标", text)
	}
	matchedCity := amapString(first["city"])
	if matchedCity == "" {
		matchedCity = amapString(first["province"])
	}
	ambiguous := len(geocodes) > 1
	if hint := strings.TrimSpace(city); hint != "" && matchedCity != "" {
		normalizedHint := strings.TrimSuffix(hint, "市")
		normalizedMatch := strings.TrimSuffix(matchedCity, "市")
		if !strings.Contains(normalizedMatch, normalizedHint) && !strings.Contains(normalizedHint, normalizedMatch) {
			ambiguous = true
		}
	}
	return amapResolved{
		Location:       location,
		Input:          text,
		MatchedAddress: amapString(first["formatted_address"]),
		MatchedCity:    matchedCity,
		Candidates:     len(geocodes),
		Ambiguous:      ambiguous,
	}, nil
}

// amapCityOf 用逆地理编码猜城市名，公交规划必填 city。
func (p *plugin) amapCityOf(ctx context.Context, location string) string {
	payload, err := p.amapGet(ctx, "/v3/geocode/regeo", map[string]string{
		"location":   location,
		"extensions": "base",
	})
	if err != nil {
		return ""
	}
	regeo := amapMap(payload["regeocode"])
	component := amapMap(regeo["addressComponent"])
	for _, name := range []string{"city", "province"} {
		if value := amapString(component[name]); value != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------- 路线规划

var amapDrivingStrategies = map[int]string{
	0:  "速度优先（不考虑当前路况）",
	2:  "距离优先（不考虑当前路况）",
	3:  "避免收费",
	4:  "躲避拥堵",
	5:  "多策略（躲避拥堵、收费、距离）",
	6:  "不走高速",
	7:  "不走高速且避免收费",
	8:  "躲避拥堵且避免收费",
	9:  "躲避拥堵且不走高速",
	10: "躲避拥堵、不走高速且避免收费",
}

var amapTransitStrategies = map[int]string{
	0: "最快捷",
	1: "最经济",
	2: "最少换乘",
	3: "最少步行",
	4: "最舒适（有座）",
	5: "不乘地铁",
}

var amapModeLabels = map[string]string{
	"driving":   "驾车",
	"walking":   "步行",
	"bicycling": "骑行",
	"transit":   "公交/地铁",
}

func (p *plugin) callAmapRoute(ctx context.Context, args map[string]interface{}) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(stringArg(args, "mode")))
	if mode == "" {
		mode = "driving"
	}
	if _, ok := amapModeLabels[mode]; !ok {
		return "", fmt.Errorf("mode 必须是 driving / walking / bicycling / transit，收到 %q", mode)
	}

	city := stringArg(args, "city")
	origin, err := p.amapResolve(ctx, stringArg(args, "origin"), city, "origin")
	if err != nil {
		return "", err
	}
	destination, err := p.amapResolve(ctx, stringArg(args, "destination"), city, "destination")
	if err != nil {
		return "", err
	}

	strategy := -1
	if value, ok := args["strategy"]; ok && value != nil {
		strategy = numberArg(args, "strategy", -1)
	}
	detail := strings.ToLower(strings.TrimSpace(stringArg(args, "detail")))
	if detail == "" {
		detail = "summary"
	}
	withSteps := detail == "steps" || detail == "full"
	maxPaths := clamp(numberArg(args, "max_paths", 2), 1, 3)

	var (
		builder strings.Builder
		summary string
	)

	switch mode {
	case "driving":
		params := map[string]string{
			"origin":      origin.Location,
			"destination": destination.Location,
			"extensions":  "base",
		}
		if strategy >= 0 {
			params["strategy"] = strconv.Itoa(strategy)
		}
		if withSteps {
			params["extensions"] = "all"
		}
		if waypoints := stringSliceArg(args, "waypoints"); len(waypoints) > 0 {
			resolved := make([]string, 0, len(waypoints))
			for _, point := range waypoints {
				item, err := p.amapResolve(ctx, point, city, "waypoints")
				if err != nil {
					return "", err
				}
				resolved = append(resolved, item.Location)
			}
			params["waypoints"] = strings.Join(resolved, ";")
		}
		payload, err := p.amapGet(ctx, "/v3/direction/driving", params)
		if err != nil {
			return "", err
		}
		route := amapMap(payload["route"])
		paths := amapSlice(route["paths"])
		if len(paths) == 0 {
			return "", fmt.Errorf("驾车路线规划未返回可行方案（起点终点可能过近，或该区域无路网数据）")
		}
		summary = p.amapRenderDriving(&builder, mode, origin, destination, paths, maxPaths, withSteps, strategy, route)

	case "walking":
		payload, err := p.amapGet(ctx, "/v3/direction/walking", map[string]string{
			"origin":      origin.Location,
			"destination": destination.Location,
		})
		if err != nil {
			return "", err
		}
		route := amapMap(payload["route"])
		paths := amapSlice(route["paths"])
		if len(paths) == 0 {
			return "", fmt.Errorf("步行路线规划未返回可行方案")
		}
		summary = p.amapRenderSimple(&builder, mode, origin, destination, paths, maxPaths, withSteps)

	case "bicycling":
		payload, err := p.amapGet(ctx, "/v4/direction/bicycling", map[string]string{
			"origin":      origin.Location,
			"destination": destination.Location,
		})
		if err != nil {
			return "", err
		}
		data := amapMap(payload["data"])
		if data == nil {
			data = payload
		}
		paths := amapSlice(data["paths"])
		if len(paths) == 0 {
			return "", fmt.Errorf("骑行路线规划未返回可行方案")
		}
		summary = p.amapRenderSimple(&builder, mode, origin, destination, paths, maxPaths, withSteps)

	default: // transit
		originCity := city
		if originCity == "" {
			originCity = p.amapCityOf(ctx, origin.Location)
		}
		destinationCity := stringArg(args, "city_destination")
		if destinationCity == "" {
			destinationCity = originCity
		}
		if originCity == "" || destinationCity == "" {
			return "", fmt.Errorf("公交规划需要 city（出发城市）与 city_destination（目的城市），未能自动推断，请显式传入")
		}
		params := map[string]string{
			"origin":      origin.Location,
			"destination": destination.Location,
			"city":        originCity,
			"cityd":       destinationCity,
			"extensions":  "all",
			"nightflag":   "0",
		}
		if strategy >= 0 {
			params["strategy"] = strconv.Itoa(strategy)
		}
		if departTime := strings.TrimSpace(stringArg(args, "depart_time")); departTime != "" {
			parts := strings.SplitN(departTime, " ", 2)
			params["date"] = parts[0]
			if len(parts) == 2 {
				params["time"] = parts[1]
			}
		}
		payload, err := p.amapGet(ctx, "/v3/direction/transit/integrated", params)
		if err != nil {
			return "", err
		}
		route := amapMap(payload["route"])
		transits := amapSlice(route["transits"])
		if len(transits) == 0 {
			return "", fmt.Errorf("公交路线规划未返回可行方案（%s → %s）", originCity, destinationCity)
		}
		summary = p.amapRenderTransit(&builder, origin, destination, transits, maxPaths, withSteps, strategy, originCity, destinationCity, route)
	}

	var out strings.Builder
	out.WriteString(summary)
	out.WriteString(builder.String())
	if warning := amapAmbiguityWarning(origin, destination); warning != "" {
		out.WriteString("\n⚠ " + warning)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func amapAmbiguityWarning(origin, destination amapResolved) string {
	notes := make([]string, 0, 2)
	for _, item := range []struct {
		label string
		data  amapResolved
	}{{"起点", origin}, {"终点", destination}} {
		if !item.data.Ambiguous {
			continue
		}
		matched := item.data.MatchedAddress
		if matched == "" {
			matched = item.data.Location
		}
		notes = append(notes, fmt.Sprintf("%s「%s」被解析为「%s」（候选 %d 个）", item.label, item.data.Input, matched, item.data.Candidates))
	}
	if len(notes) == 0 {
		return ""
	}
	return strings.Join(notes, "；") + "。若与预期不符，请补 city/city_destination，或先用 amap_place 的 geocode 确认坐标后再传 \"经度,纬度\"。"
}

func amapEndpointLine(label string, data amapResolved) string {
	matched := data.MatchedAddress
	if matched == "" {
		matched = "（直接坐标）"
	}
	return fmt.Sprintf("%s：%s → %s", label, matched, data.Location)
}

func (p *plugin) amapRenderDriving(builder *strings.Builder, mode string, origin, destination amapResolved, paths []interface{}, maxPaths int, withSteps bool, strategy int, route map[string]interface{}) string {
	var head strings.Builder
	head.WriteString(fmt.Sprintf("%s %s → %s\n", amapModeLabels[mode], origin.Input, destination.Input))
	head.WriteString(amapEndpointLine("起点", origin) + "\n")
	head.WriteString(amapEndpointLine("终点", destination) + "\n")

	first := amapMap(paths[0])
	distance, _ := amapFloat(first["distance"])
	duration, _ := amapFloat(first["duration"])
	head.WriteString(fmt.Sprintf("距离 %s，预计 %s", amapDistanceText(distance), amapDurationText(duration)))
	if lights, ok := amapInt(first["traffic_lights"]); ok && lights > 0 {
		head.WriteString(fmt.Sprintf("，红绿灯 %d 个", lights))
	}
	if tolls, ok := amapFloat(first["tolls"]); ok && tolls > 0 {
		head.WriteString(fmt.Sprintf("，过路费约 %s 元", amapNum(tolls, 0)))
	}
	if taxi, ok := amapFloat(route["taxi_cost"]); ok && taxi > 0 {
		head.WriteString(fmt.Sprintf("，打车约 %s 元", amapNum(taxi, 0)))
	}
	if strategy >= 0 {
		if text, ok := amapDrivingStrategies[strategy]; ok {
			head.WriteString("（策略：" + text + "）")
		}
	}
	head.WriteString("\n")

	for index, raw := range paths {
		if index >= maxPaths {
			break
		}
		path := amapMap(raw)
		pathDistance, _ := amapFloat(path["distance"])
		pathDuration, _ := amapFloat(path["duration"])
		if index == 0 {
			builder.WriteString(fmt.Sprintf("\n方案1：%s / %s\n", amapDistanceText(pathDistance), amapDurationText(pathDuration)))
		} else {
			builder.WriteString(fmt.Sprintf("\n方案%d：%s / %s\n", index+1, amapDistanceText(pathDistance), amapDurationText(pathDuration)))
		}
		if withSteps {
			amapWriteSteps(builder, amapSlice(path["steps"]))
		}
	}
	return head.String()
}

func (p *plugin) amapRenderSimple(builder *strings.Builder, mode string, origin, destination amapResolved, paths []interface{}, maxPaths int, withSteps bool) string {
	var head strings.Builder
	head.WriteString(fmt.Sprintf("%s %s → %s\n", amapModeLabels[mode], origin.Input, destination.Input))
	head.WriteString(amapEndpointLine("起点", origin) + "\n")
	head.WriteString(amapEndpointLine("终点", destination) + "\n")

	first := amapMap(paths[0])
	distance, _ := amapFloat(first["distance"])
	duration, _ := amapFloat(first["duration"])
	head.WriteString(fmt.Sprintf("距离 %s，预计 %s\n", amapDistanceText(distance), amapDurationText(duration)))

	for index, raw := range paths {
		if index >= maxPaths {
			break
		}
		path := amapMap(raw)
		pathDistance, _ := amapFloat(path["distance"])
		pathDuration, _ := amapFloat(path["duration"])
		builder.WriteString(fmt.Sprintf("\n方案%d：%s / %s\n", index+1, amapDistanceText(pathDistance), amapDurationText(pathDuration)))
		if withSteps {
			amapWriteSteps(builder, amapSlice(path["steps"]))
		}
	}
	return head.String()
}

func (p *plugin) amapRenderTransit(builder *strings.Builder, origin, destination amapResolved, transits []interface{}, maxPaths int, withSteps bool, strategy int, originCity, destinationCity string, route map[string]interface{}) string {
	var head strings.Builder
	head.WriteString(fmt.Sprintf("%s %s → %s（%s）\n", amapModeLabels["transit"], origin.Input, destination.Input, originCity))
	head.WriteString(amapEndpointLine("起点", origin) + "\n")
	head.WriteString(amapEndpointLine("终点", destination) + "\n")

	first := amapMap(transits[0])
	distance, _ := amapFloat(first["distance"])
	duration, _ := amapFloat(first["duration"])
	head.WriteString(fmt.Sprintf("距离 %s，预计 %s", amapDistanceText(distance), amapDurationText(duration)))
	if cost, ok := amapFloat(first["cost"]); ok && cost > 0 {
		head.WriteString(fmt.Sprintf("，票价约 %s 元", amapNum(cost, 1)))
	}
	if walking, ok := amapFloat(first["walking_distance"]); ok && walking > 0 {
		head.WriteString(fmt.Sprintf("，步行 %s", amapDistanceText(walking)))
	}
	if strategy >= 0 {
		if text, ok := amapTransitStrategies[strategy]; ok {
			head.WriteString("（策略：" + text + "）")
		}
	}
	head.WriteString("\n")

	for index, raw := range transits {
		if index >= maxPaths {
			break
		}
		transit := amapMap(raw)
		transitDistance, _ := amapFloat(transit["distance"])
		transitDuration, _ := amapFloat(transit["duration"])
		cost, _ := amapFloat(transit["cost"])
		builder.WriteString(fmt.Sprintf("\n方案%d：%s / %s", index+1, amapDistanceText(transitDistance), amapDurationText(transitDuration)))
		if cost > 0 {
			builder.WriteString(fmt.Sprintf(" / %s 元", amapNum(cost, 1)))
		}
		builder.WriteString("\n")
		step := 0
		for _, rawSegment := range amapSlice(transit["segments"]) {
			for _, line := range amapSegmentLines(amapMap(rawSegment)) {
				step++
				builder.WriteString(fmt.Sprintf("  %d. %s\n", step, line))
			}
		}
	}
	return head.String()
}

func amapWriteSteps(builder *strings.Builder, steps []interface{}) {
	written := 0
	for _, raw := range steps {
		if written >= amapMaxSteps {
			builder.WriteString("  …（后续指引已省略）\n")
			return
		}
		step := amapMap(raw)
		instruction := amapString(step["instruction"])
		if instruction == "" {
			continue
		}
		written++
		distance, _ := amapFloat(step["distance"])
		extra := ""
		if distance > 0 {
			extra = "（" + amapDistanceText(distance) + "）"
		}
		builder.WriteString(fmt.Sprintf("  %d. %s%s\n", written, instruction, extra))
	}
}

// amapSegmentLines 把公交换乘的一段拍平成可读行。
func amapSegmentLines(segment map[string]interface{}) []string {
	lines := make([]string, 0, 3)

	if walking := amapMap(segment["walking"]); walking != nil {
		distance, _ := amapFloat(walking["distance"])
		duration, _ := amapFloat(walking["duration"])
		instruction := ""
		steps := amapSlice(walking["steps"])
		if len(steps) > 0 {
			parts := make([]string, 0, 3)
			for _, raw := range steps {
				if len(parts) >= 3 {
					break
				}
				if text := amapString(amapMap(raw)["instruction"]); text != "" {
					parts = append(parts, text)
				}
			}
			instruction = strings.Join(parts, "；")
		}
		line := fmt.Sprintf("步行 %s（%s）", amapDistanceText(distance), amapDurationText(duration))
		if instruction != "" {
			line += "：" + truncate(instruction, 120)
		}
		lines = append(lines, line)
	}

	if bus := amapMap(segment["bus"]); bus != nil {
		for _, raw := range amapSlice(bus["buslines"]) {
			line := amapMap(raw)
			name := amapString(line["name"])
			if name == "" {
				continue
			}
			departure := amapString(amapMap(line["departure_stop"])["name"])
			arrival := amapString(amapMap(line["arrival_stop"])["name"])
			lineDistance, _ := amapFloat(line["distance"])
			lineDuration, _ := amapFloat(line["duration"])
			text := fmt.Sprintf("%s：%s → %s", name, fallbackText(departure, "?"), fallbackText(arrival, "?"))
			if via, ok := amapInt(line["via_num"]); ok && via > 0 {
				stops := make([]string, 0, 4)
				for _, rawStop := range amapSlice(line["via_stops"]) {
					if len(stops) >= 4 {
						break
					}
					if name := amapString(amapMap(rawStop)["name"]); name != "" {
						stops = append(stops, name)
					}
				}
				text += fmt.Sprintf("，途经 %d 站", via)
				if len(stops) > 0 {
					text += "（" + strings.Join(stops, "、") + "）"
				}
			}
			if lineDistance > 0 {
				text += fmt.Sprintf("，%s / %s", amapDistanceText(lineDistance), amapDurationText(lineDuration))
			}
			lines = append(lines, text)
		}
	}

	if railway := amapMap(segment["railway"]); railway != nil {
		name := amapString(railway["name"])
		departure := amapString(amapMap(railway["departure_stop"])["name"])
		arrival := amapString(amapMap(railway["arrival_stop"])["name"])
		// 站内换乘时常返回空壳 railway 段，别产出「铁路」噪声
		if name != "" || departure != "" || arrival != "" {
			lines = append(lines, fmt.Sprintf("%s：%s → %s", fallbackText(name, "铁路"), fallbackText(departure, "?"), fallbackText(arrival, "?")))
		}
	}

	if taxi := amapMap(segment["taxi"]); taxi != nil {
		distance, _ := amapFloat(taxi["distance"])
		duration, _ := amapFloat(taxi["duration"])
		lines = append(lines, fmt.Sprintf("打车 %s（%s）", amapDistanceText(distance), amapDurationText(duration)))
	}

	return lines
}

// ---------------------------------------------------------------- 地点检索

func (p *plugin) callAmapPlace(ctx context.Context, args map[string]interface{}) (string, error) {
	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
	if action == "" {
		action = "poi"
	}
	city := stringArg(args, "city")

	switch action {
	case "geocode":
		address := stringArg(args, "address")
		if address == "" {
			address = stringArg(args, "keywords")
		}
		if address == "" {
			return "", fmt.Errorf("action=geocode 需要 address")
		}
		payload, err := p.amapGet(ctx, "/v3/geocode/geo", map[string]string{"address": address, "city": city})
		if err != nil {
			return "", err
		}
		geocodes := amapSlice(payload["geocodes"])
		if len(geocodes) == 0 {
			return "", fmt.Errorf("未找到地址「%s」", address)
		}
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("地理编码：%s（%d 条）\n", address, len(geocodes)))
		for index, raw := range geocodes {
			if index >= 5 {
				break
			}
			item := amapMap(raw)
			builder.WriteString(fmt.Sprintf("\n%d. %s\n", index+1, fallbackText(amapString(item["formatted_address"]), address)))
			builder.WriteString(fmt.Sprintf("   坐标：%s\n", amapString(item["location"])))
			region := strings.Join([]string{amapString(item["province"]), amapString(item["city"]), amapString(item["district"])}, " ")
			if strings.TrimSpace(region) != "" {
				builder.WriteString(fmt.Sprintf("   行政区：%s\n", strings.TrimSpace(region)))
			}
			if level := amapString(item["level"]); level != "" {
				builder.WriteString(fmt.Sprintf("   精度：%s\n", level))
			}
			if adcode := amapString(item["adcode"]); adcode != "" {
				builder.WriteString(fmt.Sprintf("   adcode：%s\n", adcode))
			}
		}
		return builder.String(), nil

	case "regeo":
		location := stringArg(args, "location")
		resolved, err := p.amapResolve(ctx, location, city, "location")
		if err != nil {
			return "", err
		}
		radius := clamp(numberArg(args, "radius", 1000), 1, 3000)
		payload, err := p.amapGet(ctx, "/v3/geocode/regeo", map[string]string{
			"location":   resolved.Location,
			"radius":     strconv.Itoa(radius),
			"extensions": "all",
		})
		if err != nil {
			return "", err
		}
		regeo := amapMap(payload["regeocode"])
		component := amapMap(regeo["addressComponent"])
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("逆地理编码：%s\n", resolved.Location))
		builder.WriteString(fmt.Sprintf("地址：%s\n", fallbackText(amapString(regeo["formatted_address"]), "未知")))
		region := strings.Join([]string{amapString(component["province"]), amapString(component["city"]), amapString(component["district"]), amapString(component["township"])}, " ")
		if strings.TrimSpace(region) != "" {
			builder.WriteString(fmt.Sprintf("行政区：%s\n", strings.TrimSpace(region)))
		}
		if street := amapString(component["street"]); street != "" {
			builder.WriteString(fmt.Sprintf("街道：%s%s\n", street, amapString(component["streetNumber"])))
		}
		if name := amapString(amapMap(component["neighborhood"])["name"]); name != "" {
			builder.WriteString(fmt.Sprintf("所在社区：%s\n", name))
		}
		if name := amapString(amapMap(component["building"])["name"]); name != "" {
			builder.WriteString(fmt.Sprintf("楼宇：%s\n", name))
		}
		if adcode := amapString(component["adcode"]); adcode != "" {
			builder.WriteString(fmt.Sprintf("adcode：%s\n", adcode))
		}
		pois := amapSlice(regeo["pois"])
		if len(pois) > 0 {
			builder.WriteString(fmt.Sprintf("\n周边 POI（%d 个）：\n", len(pois)))
			for index, raw := range pois {
				if index >= amapMaxPOI {
					break
				}
				item := amapMap(raw)
				distance, _ := amapFloat(item["distance"])
				builder.WriteString(fmt.Sprintf("  - %s（%s）%s\n",
					fallbackText(amapString(item["name"]), "未知"),
					amapString(item["type"]),
					amapDistanceText(distance)))
			}
		}
		return builder.String(), nil

	case "poi", "search":
		keywords := stringArg(args, "keywords")
		types := stringArg(args, "types")
		if keywords == "" && types == "" {
			return "", fmt.Errorf("action=poi 需要 keywords 或 types，例如 keywords=\"咖啡\" 或 types=\"050500\"")
		}
		pageSize := clamp(numberArg(args, "page_size", 10), 1, 25)
		location := stringArg(args, "location")

		var (
			payload map[string]interface{}
			err     error
			mode    string
			center  string
		)
		if location != "" {
			resolved, resolveErr := p.amapResolve(ctx, location, city, "location")
			if resolveErr != nil {
				return "", resolveErr
			}
			center = resolved.Location
			radius := clamp(numberArg(args, "radius", 3000), 1, 50000)
			payload, err = p.amapGet(ctx, "/v3/place/around", map[string]string{
				"location":   center,
				"keywords":   keywords,
				"types":      types,
				"radius":     strconv.Itoa(radius),
				"sortrule":   fallbackText(stringArg(args, "sortrule"), "distance"),
				"offset":     strconv.Itoa(pageSize),
				"page":       strconv.Itoa(clamp(numberArg(args, "page", 1), 1, 100)),
				"extensions": "all",
			})
			mode = "around"
		} else {
			payload, err = p.amapGet(ctx, "/v3/place/text", map[string]string{
				"keywords":   keywords,
				"types":      types,
				"city":       city,
				"offset":     strconv.Itoa(pageSize),
				"page":       strconv.Itoa(clamp(numberArg(args, "page", 1), 1, 100)),
				"extensions": "all",
			})
			mode = "text"
		}
		if err != nil {
			return "", err
		}
		pois := amapSlice(payload["pois"])
		query := fallbackText(keywords, types)
		var builder strings.Builder
		if mode == "around" {
			builder.WriteString(fmt.Sprintf("周边 POI：%s（中心 %s）\n", query, center))
		} else {
			builder.WriteString(fmt.Sprintf("POI 检索：%s（城市 %s）\n", query, fallbackText(city, "不限")))
		}
		builder.WriteString(fmt.Sprintf("共 %s 条，返回 %d 条\n", fallbackText(amapString(payload["count"]), strconv.Itoa(len(pois))), len(pois)))
		for index, raw := range pois {
			if index >= amapMaxPOI {
				break
			}
			item := amapMap(raw)
			builder.WriteString(fmt.Sprintf("\n%d. %s", index+1, fallbackText(amapString(item["name"]), "未知")))
			if distance, ok := amapFloat(item["distance"]); ok {
				builder.WriteString("  距离 " + amapDistanceText(distance))
			}
			builder.WriteString("\n")
			if typeName := amapString(item["type"]); typeName != "" {
				builder.WriteString("   类型：" + typeName + "\n")
			}
			if address := amapString(item["address"]); address != "" {
				builder.WriteString("   地址：" + address + "\n")
			}
			if location := amapString(item["location"]); location != "" {
				builder.WriteString("   坐标：" + location + "\n")
			}
			biz := amapMap(item["biz_ext"])
			extras := make([]string, 0, 3)
			if rating := amapString(biz["rating"]); rating != "" {
				extras = append(extras, "评分 "+rating)
			}
			if cost := amapString(biz["cost"]); cost != "" && cost != "0" && cost != "0.00" {
				extras = append(extras, "人均 "+cost+" 元")
			}
			if openTime := amapString(biz["open_time"]); openTime != "" {
				extras = append(extras, "营业 "+openTime)
			}
			if len(extras) > 0 {
				builder.WriteString("   " + strings.Join(extras, "，") + "\n")
			}
			if tel := amapString(item["tel"]); tel != "" {
				builder.WriteString("   电话：" + tel + "\n")
			}
			if id := amapString(item["id"]); id != "" {
				builder.WriteString("   POI ID：" + id + "\n")
			}
		}
		return builder.String(), nil

	case "poi_detail":
		poiID := stringArg(args, "poi_id")
		if poiID == "" {
			return "", fmt.Errorf("action=poi_detail 需要 poi_id（来自 amap_place 的 POI ID）")
		}
		payload, err := p.amapGet(ctx, "/v3/place/detail", map[string]string{"id": poiID, "extensions": "all"})
		if err != nil {
			return "", err
		}
		pois := amapSlice(payload["pois"])
		if len(pois) == 0 {
			return "", fmt.Errorf("未找到 POI：%s", poiID)
		}
		item := amapMap(pois[0])
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("POI 详情：%s\n", fallbackText(amapString(item["name"]), poiID)))
		if typeName := amapString(item["type"]); typeName != "" {
			builder.WriteString("类型：" + typeName + "\n")
		}
		if address := amapString(item["address"]); address != "" {
			builder.WriteString("地址：" + address + "\n")
		}
		if location := amapString(item["location"]); location != "" {
			builder.WriteString("坐标：" + location + "\n")
		}
		if tel := amapString(item["tel"]); tel != "" {
			builder.WriteString("电话：" + tel + "\n")
		}
		biz := amapMap(item["biz_ext"])
		if rating := amapString(biz["rating"]); rating != "" {
			builder.WriteString("评分：" + rating + "\n")
		}
		if cost := amapString(biz["cost"]); cost != "" && cost != "0.00" {
			builder.WriteString("人均：" + cost + " 元\n")
		}
		if openTime := amapString(biz["open_time"]); openTime != "" {
			builder.WriteString("营业时间：" + openTime + "\n")
		}
		return builder.String(), nil

	case "tips":
		keywords := stringArg(args, "keywords")
		if keywords == "" {
			return "", fmt.Errorf("action=tips 需要 keywords")
		}
		params := map[string]string{
			"keywords": keywords,
			"city":     city,
			"datatype": "all",
		}
		if location := stringArg(args, "location"); location != "" {
			if resolved, err := p.amapResolve(ctx, location, city, "location"); err == nil {
				params["location"] = resolved.Location
			}
		}
		payload, err := p.amapGet(ctx, "/v3/assistant/inputtips", params)
		if err != nil {
			return "", err
		}
		tips := amapSlice(payload["tips"])
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("输入提示：%s（%d 条）\n", keywords, len(tips)))
		for index, raw := range tips {
			if index >= amapMaxPOI {
				break
			}
			item := amapMap(raw)
			name := amapString(item["name"])
			if name == "" {
				continue
			}
			builder.WriteString(fmt.Sprintf("%d. %s", index+1, name))
			if location := amapString(item["location"]); location != "" {
				builder.WriteString("  " + location)
			}
			builder.WriteString("\n")
			if district := amapString(item["district"]); district != "" {
				builder.WriteString("   " + district + "\n")
			}
			if address := amapString(item["address"]); address != "" {
				builder.WriteString("   " + address + "\n")
			}
			if id := amapString(item["id"]); id != "" {
				builder.WriteString("   POI ID：" + id + "\n")
			}
		}
		return builder.String(), nil
	}

	return "", fmt.Errorf("未知 action：%s（可用 geocode / regeo / poi / poi_detail / tips）", action)
}

// ---------------------------------------------------------------- 地区信息

var amapTrafficStatus = map[string]string{
	"0": "未知",
	"1": "畅通",
	"2": "缓行",
	"3": "拥堵",
	"4": "严重拥堵",
}

func (p *plugin) callAmapInfo(ctx context.Context, args map[string]interface{}) (string, error) {
	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
	if action == "" {
		action = "weather"
	}

	switch action {
	case "weather":
		city := stringArg(args, "city")
		if city == "" {
			return "", fmt.Errorf("action=weather 需要 city（城市名或 adcode）")
		}
		forecast := boolArg(args, "forecast", true)
		extensions := "base"
		if forecast {
			extensions = "all"
		}
		payload, err := p.amapGet(ctx, "/v3/weather/weatherInfo", map[string]string{"city": city, "extensions": extensions})
		if err != nil {
			return "", err
		}
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("天气：%s\n", city))
		for _, raw := range amapSlice(payload["lives"]) {
			item := amapMap(raw)
			builder.WriteString(fmt.Sprintf("\n【实时】%s %s\n", amapString(item["city"]), amapString(item["weather"])))
			builder.WriteString(fmt.Sprintf("气温 %s°C，%s风 %s 级，湿度 %s%%\n",
				amapString(item["temperature"]), amapString(item["winddirection"]),
				amapString(item["windpower"]), amapString(item["humidity"])))
			if reportTime := amapString(item["reporttime"]); reportTime != "" {
				builder.WriteString("发布时间：" + reportTime + "\n")
			}
		}
		for _, raw := range amapSlice(payload["forecasts"]) {
			casts := amapSlice(amapMap(raw)["casts"])
			if len(casts) == 0 {
				continue
			}
			builder.WriteString("\n【预报】\n")
			for _, rawCast := range casts {
				cast := amapMap(rawCast)
				builder.WriteString(fmt.Sprintf("%s  %s转%s  %s~%s°C  %s风 %s 级\n",
					amapString(cast["date"]),
					amapString(cast["dayweather"]), amapString(cast["nightweather"]),
					amapString(cast["nighttemp"]), amapString(cast["daytemp"]),
					amapString(cast["daywind"]), amapString(cast["daypower"])))
			}
		}
		return builder.String(), nil

	case "district":
		keywords := stringArg(args, "keywords")
		subdistrict := clamp(numberArg(args, "subdistrict", 1), 0, 3)
		payload, err := p.amapGet(ctx, "/v3/config/district", map[string]string{
			"keywords":    keywords,
			"subdistrict": strconv.Itoa(subdistrict),
			"extensions":  "base",
		})
		if err != nil {
			return "", err
		}
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("行政区划：%s\n", fallbackText(keywords, "全国")))
		var walk func(items []interface{}, depth int)
		walk = func(items []interface{}, depth int) {
			for _, raw := range items {
				item := amapMap(raw)
				indent := strings.Repeat("  ", depth)
				builder.WriteString(fmt.Sprintf("%s- %s（%s，adcode %s）中心 %s\n",
					indent,
					fallbackText(amapString(item["name"]), "未知"),
					fallbackText(amapString(item["level"]), "?"),
					fallbackText(amapString(item["adcode"]), "?"),
					fallbackText(amapString(item["center"]), "?")))
				if children := amapSlice(item["districts"]); len(children) > 0 && depth < 3 {
					walk(children, depth+1)
				}
			}
		}
		walk(amapSlice(payload["districts"]), 0)
		return builder.String(), nil

	case "traffic":
		kind := strings.ToLower(strings.TrimSpace(stringArg(args, "kind")))
		if kind == "" {
			kind = "road"
		}
		level := clamp(numberArg(args, "level", 5), 1, 6)
		city := stringArg(args, "city")

		var (
			path   string
			params map[string]string
		)
		switch kind {
		case "road":
			road := stringArg(args, "road")
			if road == "" {
				return "", fmt.Errorf("kind=road 需要 road（道路名，如「北四环」）")
			}
			path = "/v3/traffic/status/road"
			params = map[string]string{"name": road, "city": city}
		case "circle":
			location := stringArg(args, "location")
			resolved, err := p.amapResolve(ctx, location, city, "location")
			if err != nil {
				return "", err
			}
			path = "/v3/traffic/status/circle"
			params = map[string]string{
				"location": resolved.Location,
				"radius":   strconv.Itoa(clamp(numberArg(args, "radius", 1000), 0, 5000)),
			}
		case "rectangle":
			rectangle := stringArg(args, "rectangle")
			if rectangle == "" {
				return "", fmt.Errorf("kind=rectangle 需要 rectangle，格式 \"左下经度,纬度;右上经度,纬度\"")
			}
			path = "/v3/traffic/status/rectangle"
			params = map[string]string{"rectangle": rectangle}
		default:
			return "", fmt.Errorf("kind 必须是 road / circle / rectangle")
		}
		params["level"] = strconv.Itoa(level)
		params["extensions"] = "all"

		payload, err := p.amapGet(ctx, path, params)
		if err != nil {
			// 道路名不在路况覆盖范围时，退化为该路所在圆形区域
			if kind == "road" && amapIsUncoveredRoad(err) {
				if fallback, fallbackErr := p.amapTrafficCircleFallback(ctx, stringArg(args, "road"), city, level); fallbackErr == nil {
					return fallback, nil
				}
			}
			return "", err
		}
		return amapRenderTraffic(payload, kind, ""), nil
	}

	return "", fmt.Errorf("未知 action：%s（可用 weather / district / traffic）", action)
}

func amapIsUncoveredRoad(err error) bool {
	var apiErr *amapError
	if ok := asAmapError(err, &apiErr); !ok {
		return false
	}
	switch apiErr.Infocode {
	case "20003", "20000", "10002":
		return true
	}
	return false
}

func asAmapError(err error, target **amapError) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := err.(*amapError); ok {
		*target = apiErr
		return true
	}
	return false
}

// amapTrafficCircleFallback 把「查不到的路」退化成「该路所在 2km 圆形区域」的路况。
func (p *plugin) amapTrafficCircleFallback(ctx context.Context, road string, city string, level int) (string, error) {
	resolved, err := p.amapResolve(ctx, road, city, "road")
	if err != nil {
		return "", err
	}
	payload, err := p.amapGet(ctx, "/v3/traffic/status/circle", map[string]string{
		"location":   resolved.Location,
		"radius":     "2000",
		"level":      strconv.Itoa(level),
		"extensions": "all",
	})
	if err != nil {
		return "", err
	}
	return amapRenderTraffic(payload, "circle", fmt.Sprintf("（道路「%s」不在高德路况覆盖范围，已改为查询它所在的 2km 圆形区域：%s）", road, resolved.Location)), nil
}

func amapRenderTraffic(payload map[string]interface{}, kind string, note string) string {
	info := amapMap(payload["trafficinfo"])
	evaluation := amapMap(info["evaluation"])
	var builder strings.Builder
	builder.WriteString("实时路况")
	if note != "" {
		builder.WriteString(note)
	}
	builder.WriteString("\n")
	if description := amapString(info["description"]); description != "" {
		builder.WriteString("概述：" + description + "\n")
	}
	if evaluation != nil {
		status := amapTrafficStatus[amapString(evaluation["status"])]
		if status == "" {
			status = amapString(evaluation["description"])
		}
		builder.WriteString("整体：" + fallbackText(status, "未知"))
		if expedite, ok := amapFloat(evaluation["expedite"]); ok {
			builder.WriteString(fmt.Sprintf("，畅通 %.1f%%", expedite))
		}
		if congested, ok := amapFloat(evaluation["congested"]); ok {
			builder.WriteString(fmt.Sprintf("，缓行 %.1f%%", congested))
		}
		if blocked, ok := amapFloat(evaluation["blocked"]); ok {
			builder.WriteString(fmt.Sprintf("，拥堵 %.1f%%", blocked))
		}
		builder.WriteString("\n")
	}
	roads := amapSlice(info["roads"])
	if len(roads) > 0 {
		builder.WriteString("\n涉及道路：\n")
		for index, raw := range roads {
			if index >= amapMaxPOI {
				break
			}
			item := amapMap(raw)
			status := amapTrafficStatus[amapString(item["status"])]
			if status == "" {
				status = amapString(item["status"])
			}
			line := fmt.Sprintf("  - %s：%s", fallbackText(amapString(item["name"]), "未知"), fallbackText(status, "未知"))
			if direction := amapString(item["dir"]); direction != "" {
				line += "（" + direction + "）"
			}
			if speed, ok := amapFloat(item["speed"]); ok {
				line += fmt.Sprintf("，%s km/h", amapTrimFloat(speed, 1))
			}
			builder.WriteString(line + "\n")
		}
	}
	return builder.String()
}

// ---------------------------------------------------------------- 静态地图
//
// 高德静态图对参数格式极其挑剔（实测结论）：
//   - location / zoom / size / scale / traffic 稳定可用；
//   - markers 必须写成 "style,color,label:lng,lat"，label 不可省略，否则 20003；
//   - paths 在该 Key 上始终返回 20003（官方文档示例也一样），因此不再对外暴露；
//   - 多个标记点用 ";" 分隔。
//
// 为避免模型自己拼错格式，工具只接受结构化输入（center/points），
// 由这里统一生成合法 URL。

// amapStaticMapPoint 是一个标记点。
type amapStaticMapPoint struct {
	Lng   float64
	Lat   float64
	Label string
	Color string
}

// amapParseStaticMapPoints 解析结构化点列表：支持 "名称:经度,纬度"、"经度,纬度" 两种写法。
func amapParseStaticMapPoints(raw []string) ([]amapStaticMapPoint, error) {
	points := make([]amapStaticMapPoint, 0, len(raw))
	for _, item := range raw {
		text := strings.TrimSpace(item)
		if text == "" {
			continue
		}
		label := ""
		coordText := text
		// 形如 "起点:113.40,23.12"
		if index := strings.LastIndex(text, ":"); index >= 0 {
			candidate := strings.TrimSpace(text[:index])
			rest := strings.TrimSpace(text[index+1:])
			if _, _, ok := amapParseLngLat(rest); ok {
				label = candidate
				coordText = rest
			}
		}
		lng, lat, ok := amapParseLngLat(coordText)
		if !ok {
			return nil, fmt.Errorf("标记点 %q 格式非法，应为 \"名称:经度,纬度\" 或 \"经度,纬度\"", item)
		}
		points = append(points, amapStaticMapPoint{Lng: lng, Lat: lat, Label: label})
	}
	return points, nil
}

// amapBuildMarkers 生成高德要求的 markers 串。label 必填，缺失时用序号补上。
func amapBuildMarkers(points []amapStaticMapPoint) string {
	parts := make([]string, 0, len(points))
	for index, point := range points {
		label := strings.TrimSpace(point.Label)
		if label == "" {
			label = strconv.Itoa(index + 1)
		}
		// 高德 label 只稳定支持短文本，超长截断避免整串失败
		label = truncate(label, 4)
		color := point.Color
		if color == "" {
			color = "0xFF0055"
		}
		parts = append(parts, fmt.Sprintf("mid,%s,%s:%.6f,%.6f", color, label, point.Lng, point.Lat))
	}
	return strings.Join(parts, ";")
}

func (p *plugin) callAmapMapImage(ctx context.Context, args map[string]interface{}) (string, error) {
	key, err := p.amapKey()
	if err != nil {
		return "", err
	}
	city := stringArg(args, "city")
	values := url.Values{}

	// 中心点：显式给 center 就用它，否则用第一个标记点兜底（高德要求必须有 location）
	centerText := stringArg(args, "center")
	if centerText == "" {
		centerText = stringArg(args, "location")
	}

	rawPoints := stringSliceArg(args, "points")
	if len(rawPoints) == 0 {
		rawPoints = stringSliceArg(args, "markers") // 兼容旧参数名
	}
	points, err := amapParseStaticMapPoints(rawPoints)
	if err != nil {
		return "", err
	}

	if centerText != "" {
		resolved, err := p.amapResolve(ctx, centerText, city, "center")
		if err != nil {
			return "", err
		}
		values.Set("location", resolved.Location)
	} else if len(points) > 0 {
		// 用标记点平均位置作中心，避免模型还要自己算
		var sumLng, sumLat float64
		for _, point := range points {
			sumLng += point.Lng
			sumLat += point.Lat
		}
		count := float64(len(points))
		values.Set("location", fmt.Sprintf("%.6f,%.6f", sumLng/count, sumLat/count))
	} else {
		return "", fmt.Errorf("需要 center（地图中心点）或 points（至少一个标记点）")
	}

	values.Set("zoom", strconv.Itoa(clamp(numberArg(args, "zoom", 15), 1, 17)))
	width := clamp(numberArg(args, "width", 600), 1, 1024)
	height := clamp(numberArg(args, "height", 400), 1, 1024)
	values.Set("size", fmt.Sprintf("%d*%d", width, height))
	values.Set("scale", strconv.Itoa(clamp(numberArg(args, "scale", 2), 1, 2)))
	if boolArg(args, "show_traffic", false) {
		values.Set("traffic", "1")
	}
	if len(points) > 0 {
		values.Set("markers", amapBuildMarkers(points))
	}
	values.Set("key", key)

	imageURL := p.amapBaseURL() + "/v3/staticmap?" + values.Encode()

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("静态地图已生成（%dx%d，缩放 %s，中心 %s）\n", width, height, values.Get("zoom"), values.Get("location")))
	builder.WriteString(imageURL + "\n")
	if len(points) > 0 {
		builder.WriteString(fmt.Sprintf("\n标记点 %d 个：\n", len(points)))
		for index, point := range points {
			label := point.Label
			if label == "" {
				label = strconv.Itoa(index + 1)
			}
			builder.WriteString(fmt.Sprintf("  %s → %.6f,%.6f\n", label, point.Lng, point.Lat))
		}
		builder.WriteString("\n注意：高德静态图的 markers 图层在部分 Key 上会被静默忽略，若打开后看不到标记点，请以坐标文字为准。\n")
	}
	builder.WriteString("注意：URL 内含 API Key，只发给可信对象。高德静态图不支持折线（paths），需要连线请用文字描述路线。")
	return builder.String(), nil
}
