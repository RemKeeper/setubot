package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"setubot/internal/config"
)

// newAmapTestPlugin 用本地 httptest 服务替换高德，避免测试依赖外网与真实 Key。
func newAmapTestPlugin(t *testing.T, handler http.HandlerFunc) (*plugin, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p := &plugin{
		cfg: config.AgentConfig{
			Amap: config.AmapConfig{
				Enabled:  true,
				APIKey:   "test-key",
				BaseURL:  server.URL,
				Timeout:  5,
				CacheTTL: 0,
			},
		},
		amapHTTP:  server.Client(),
		amapCache: make(map[string]amapCacheEntry),
	}
	return p, server
}

func amapJSON(t *testing.T, w http.ResponseWriter, payload map[string]interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("写入响应失败: %v", err)
	}
}

func amapOK(extra map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{"status": "1", "info": "OK", "infocode": "10000"}
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func TestAmapParseLngLat(t *testing.T) {
	cases := []struct {
		input   string
		wantOK  bool
		wantLng float64
		wantLat float64
	}{
		{"120.16,30.28", true, 120.16, 30.28},
		{" 120.16 ， 30.28 ", true, 120.16, 30.28},
		{"-73.5,45.4", true, -73.5, 45.4},
		{"120.16", false, 0, 0},
		{"abc,def", false, 0, 0},
		{"200,30", false, 0, 0},
		{"", false, 0, 0},
	}
	for _, tc := range cases {
		lng, lat, ok := amapParseLngLat(tc.input)
		if ok != tc.wantOK {
			t.Fatalf("amapParseLngLat(%q) ok=%v, want %v", tc.input, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if lng != tc.wantLng || lat != tc.wantLat {
			t.Fatalf("amapParseLngLat(%q) = %v,%v, want %v,%v", tc.input, lng, lat, tc.wantLng, tc.wantLat)
		}
	}
}

func TestAmapDistanceAndDurationText(t *testing.T) {
	if got := amapDistanceText(800); got != "800 m" {
		t.Fatalf("amapDistanceText(800) = %q", got)
	}
	if got := amapDistanceText(12345); got != "12.3 km" {
		t.Fatalf("amapDistanceText(12345) = %q", got)
	}
	if got := amapDurationText(1800); got != "30 分钟" {
		t.Fatalf("amapDurationText(1800) = %q", got)
	}
	if got := amapDurationText(3600); got != "1 小时" {
		t.Fatalf("amapDurationText(3600) = %q", got)
	}
	if got := amapDurationText(5400); got != "1 小时 30 分钟" {
		t.Fatalf("amapDurationText(5400) = %q", got)
	}
}

func TestAmapStringHandlesEmptyArray(t *testing.T) {
	// 高德用 [] 表示空值，不能变成 "[]" 字符串
	if got := amapString([]interface{}{}); got != "" {
		t.Fatalf("amapString([]) = %q, want empty", got)
	}
	if got := amapString("  杭州  "); got != "杭州" {
		t.Fatalf("amapString 未 trim: %q", got)
	}
}

func TestAmapPercentParsing(t *testing.T) {
	value, ok := amapFloat("85.71%")
	if !ok || value != 85.71 {
		t.Fatalf("amapFloat(\"85.71%%\") = %v, %v", value, ok)
	}
}

func TestAmapResolveCoordinatesBypassesGeocode(t *testing.T) {
	called := false
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		amapJSON(t, w, amapOK(nil))
	})

	resolved, err := p.amapResolve(context.Background(), "120.16,30.28", "", "origin")
	if err != nil {
		t.Fatalf("amapResolve 失败: %v", err)
	}
	if called {
		t.Fatal("坐标输入不应触发地理编码请求")
	}
	if resolved.Location != "120.160000,30.280000" {
		t.Fatalf("坐标格式化异常: %q", resolved.Location)
	}
	if resolved.Ambiguous {
		t.Fatal("纯坐标不应标记歧义")
	}
}

func TestAmapResolveMarksAmbiguityWhenCityMismatch(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/geocode/geo" {
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
		amapJSON(t, w, amapOK(map[string]interface{}{
			"geocodes": []interface{}{
				map[string]interface{}{
					"formatted_address": "江西省南昌市西湖区",
					"province":          "江西省",
					"city":              "南昌市",
					"district":          "西湖区",
					"location":          "115.876998,28.657005",
					"level":             "区县",
				},
				map[string]interface{}{
					"formatted_address": "浙江省杭州市西湖区",
					"province":          "浙江省",
					"city":              "杭州市",
					"location":          "120.130396,30.259242",
				},
			},
		}))
	})

	resolved, err := p.amapResolve(context.Background(), "西湖", "杭州", "origin")
	if err != nil {
		t.Fatalf("amapResolve 失败: %v", err)
	}
	if !resolved.Ambiguous {
		t.Fatal("候选多个且城市不符时应标记歧义")
	}
	if resolved.Candidates != 2 {
		t.Fatalf("候选数异常: %d", resolved.Candidates)
	}
	if resolved.MatchedCity != "南昌市" {
		t.Fatalf("命中城市异常: %q", resolved.MatchedCity)
	}

	warning := amapAmbiguityWarning(resolved, amapResolved{})
	if !strings.Contains(warning, "江西省南昌市西湖区") {
		t.Fatalf("歧义提示未包含命中地址: %q", warning)
	}
}

func TestAmapResolveUnresolvableAddress(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		amapJSON(t, w, amapOK(map[string]interface{}{"geocodes": []interface{}{}}))
	})
	if _, err := p.amapResolve(context.Background(), "不存在的地方zzz", "", "origin"); err == nil {
		t.Fatal("解析不到坐标时应返回错误")
	}
}

func TestAmapKeyPlatformMismatchHint(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		amapJSON(t, w, map[string]interface{}{
			"status":   "0",
			"info":     "USERKEY_PLAT_NOMATCH",
			"infocode": "10009",
		})
	})

	_, err := p.amapResolve(context.Background(), "杭州西湖", "", "origin")
	if err == nil {
		t.Fatal("应返回错误")
	}
	message := err.Error()
	if !strings.Contains(message, "10009") {
		t.Fatalf("错误信息缺少 infocode: %q", message)
	}
	if !strings.Contains(message, "Web服务") {
		t.Fatalf("错误信息缺少换 Key 指引: %q", message)
	}
}

func TestAmapMissingKey(t *testing.T) {
	p := &plugin{cfg: config.AgentConfig{Amap: config.AmapConfig{Enabled: true}}}
	t.Setenv("AMAP_API_KEY", "")
	_, err := p.amapResolve(context.Background(), "杭州", "", "origin")
	if err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("缺少 Key 时应提示配置，实际: %v", err)
	}
}

func TestAmapRouteDrivingSummary(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/direction/driving":
			if got := r.URL.Query().Get("extensions"); got != "base" {
				t.Fatalf("summary 模式应使用 extensions=base，实际 %q", got)
			}
			amapJSON(t, w, amapOK(map[string]interface{}{
				"route": map[string]interface{}{
					"taxi_cost": "35",
					"paths": []interface{}{
						map[string]interface{}{
							"distance":       "12345",
							"duration":       "1800",
							"tolls":          "10",
							"traffic_lights": "12",
						},
					},
				},
			}))
		default:
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
	})

	out, err := p.callAmapRoute(context.Background(), map[string]interface{}{
		"mode":        "driving",
		"origin":      "120.123456,30.276543",
		"destination": "120.20,30.25",
	})
	if err != nil {
		t.Fatalf("路线规划失败: %v", err)
	}
	for _, want := range []string{"驾车", "12.3 km", "30 分钟", "红绿灯 12 个", "过路费约 10 元", "打车约 35 元"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestAmapRouteDrivingSteps(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("extensions"); got != "all" {
			t.Fatalf("steps 模式应使用 extensions=all，实际 %q", got)
		}
		amapJSON(t, w, amapOK(map[string]interface{}{
			"route": map[string]interface{}{
				"paths": []interface{}{
					map[string]interface{}{
						"distance": "500",
						"duration": "120",
						"steps": []interface{}{
							map[string]interface{}{"instruction": "沿文三路向东行驶500米", "distance": "500"},
						},
					},
				},
			},
		}))
	})

	out, err := p.callAmapRoute(context.Background(), map[string]interface{}{
		"mode":        "driving",
		"origin":      "120.1,30.2",
		"destination": "120.2,30.3",
		"detail":      "steps",
	})
	if err != nil {
		t.Fatalf("路线规划失败: %v", err)
	}
	if !strings.Contains(out, "沿文三路向东行驶500米") {
		t.Fatalf("缺少逐步指引:\n%s", out)
	}
}

func TestAmapRouteTransitSegments(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/direction/transit/integrated":
			if got := r.URL.Query().Get("city"); got != "杭州市" {
				t.Fatalf("公交规划应带 city，实际 %q", got)
			}
			amapJSON(t, w, amapOK(map[string]interface{}{
				"route": map[string]interface{}{
					"transits": []interface{}{
						map[string]interface{}{
							"cost":             "4",
							"duration":         "2400",
							"distance":         "9800",
							"walking_distance": "600",
							"segments": []interface{}{
								map[string]interface{}{
									"walking": map[string]interface{}{
										"distance": "300",
										"duration": "240",
										"steps": []interface{}{
											map[string]interface{}{"instruction": "步行至文三路地铁站"},
										},
									},
								},
								map[string]interface{}{
									"bus": map[string]interface{}{
										"buslines": []interface{}{
											map[string]interface{}{
												"name":           "地铁2号线(良渚--朝阳)",
												"type":           "地铁线路",
												"distance":       "7000",
												"duration":       "1500",
												"via_num":        "5",
												"departure_stop": map[string]interface{}{"name": "文三路站"},
												"arrival_stop":   map[string]interface{}{"name": "钱江路站"},
												"via_stops": []interface{}{
													map[string]interface{}{"name": "沈塘桥"},
												},
											},
										},
									},
								},
								// 空壳 railway 段不应产出「铁路」噪声
								map[string]interface{}{"railway": map[string]interface{}{}},
							},
						},
					},
				},
			}))
		default:
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
	})

	out, err := p.callAmapRoute(context.Background(), map[string]interface{}{
		"mode":        "transit",
		"origin":      "120.15,30.28",
		"destination": "120.20,30.25",
		"city":        "杭州市",
	})
	if err != nil {
		t.Fatalf("公交规划失败: %v", err)
	}
	for _, want := range []string{"公交/地铁", "地铁2号线", "文三路站", "钱江路站", "途经 5 站", "步行"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "铁路") {
		t.Fatalf("空壳 railway 段不应输出:\n%s", out)
	}
}

func TestAmapRouteInvalidMode(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		amapJSON(t, w, amapOK(nil))
	})
	if _, err := p.callAmapRoute(context.Background(), map[string]interface{}{
		"mode":        "flying",
		"origin":      "120.1,30.2",
		"destination": "120.2,30.3",
	}); err == nil {
		t.Fatal("非法 mode 应报错")
	}
}

func TestAmapPlaceGeocode(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/geocode/geo" {
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
		amapJSON(t, w, amapOK(map[string]interface{}{
			"geocodes": []interface{}{
				map[string]interface{}{
					"formatted_address": "浙江省杭州市西湖区文三路90号",
					"location":          "120.146278,30.278893",
					"province":          "浙江省",
					"city":              "杭州市",
					"district":          "西湖区",
					"adcode":            "330106",
					"level":             "门址",
				},
			},
		}))
	})

	out, err := p.callAmapPlace(context.Background(), map[string]interface{}{
		"action":  "geocode",
		"address": "杭州市西湖区文三路90号",
		"city":    "杭州",
	})
	if err != nil {
		t.Fatalf("地理编码失败: %v", err)
	}
	for _, want := range []string{"120.146278,30.278893", "浙江省 杭州市 西湖区", "adcode：330106"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestAmapPlacePOIAround(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/place/around" {
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("radius"); got != "2000" {
			t.Fatalf("半径参数异常: %q", got)
		}
		amapJSON(t, w, amapOK(map[string]interface{}{
			"count": "2",
			"pois": []interface{}{
				map[string]interface{}{
					"id":       "B1",
					"name":     "西湖文化广场",
					"type":     "风景名胜;公园广场",
					"address":  "环城北路",
					"location": "120.16,30.28",
					"distance": "800",
					"biz_ext": map[string]interface{}{
						"rating":    "4.6",
						"cost":      "0.00",
						"open_time": "09:00-17:00",
					},
					"tel": "0571-12345678",
				},
			},
		}))
	})

	out, err := p.callAmapPlace(context.Background(), map[string]interface{}{
		"action":   "poi",
		"keywords": "咖啡",
		"location": "120.16,30.28",
		"radius":   float64(2000),
	})
	if err != nil {
		t.Fatalf("POI 检索失败: %v", err)
	}
	for _, want := range []string{"西湖文化广场", "800 m", "评分 4.6", "营业 09:00-17:00", "POI ID：B1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 人均 0.00 不应显示
	if strings.Contains(out, "人均") {
		t.Fatalf("人均为 0 时不应显示:\n%s", out)
	}
}

func TestAmapPlaceRequiresKeywordOrTypes(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		amapJSON(t, w, amapOK(nil))
	})
	if _, err := p.callAmapPlace(context.Background(), map[string]interface{}{"action": "poi"}); err == nil {
		t.Fatal("缺少 keywords/types 应报错")
	}
}

func TestAmapInfoWeather(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/weather/weatherInfo" {
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
		amapJSON(t, w, amapOK(map[string]interface{}{
			"lives": []interface{}{
				map[string]interface{}{
					"province":      "浙江",
					"city":          "杭州市",
					"weather":       "晴",
					"temperature":   "18",
					"winddirection": "东北",
					"windpower":     "3",
					"humidity":      "56",
				},
			},
			"forecasts": []interface{}{
				map[string]interface{}{
					"city": "杭州市",
					"casts": []interface{}{
						map[string]interface{}{
							"date":         "2026-11-10",
							"dayweather":   "晴",
							"nightweather": "多云",
							"daytemp":      "21",
							"nighttemp":    "12",
							"daywind":      "东北",
							"daypower":     "3",
						},
					},
				},
			},
		}))
	})

	out, err := p.callAmapInfo(context.Background(), map[string]interface{}{
		"action": "weather",
		"city":   "杭州",
	})
	if err != nil {
		t.Fatalf("天气查询失败: %v", err)
	}
	for _, want := range []string{"晴", "18°C", "湿度 56%", "2026-11-10", "12~21°C"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestAmapInfoTrafficPercentAndStatus(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/traffic/status/road" {
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
		amapJSON(t, w, amapOK(map[string]interface{}{
			"trafficinfo": map[string]interface{}{
				"description": "中河高架：双向畅通。",
				"evaluation": map[string]interface{}{
					"status":    "2",
					"expedite":  "85.71%",
					"congested": "9.52%",
					"blocked":   "0.00%",
				},
				"roads": []interface{}{
					map[string]interface{}{
						"name":   "中河高架",
						"status": "2",
						"dir":    "由北向南",
						"speed":  "32.5",
					},
				},
			},
		}))
	})

	out, err := p.callAmapInfo(context.Background(), map[string]interface{}{
		"action": "traffic",
		"kind":   "road",
		"road":   "中河高架",
		"city":   "杭州",
	})
	if err != nil {
		t.Fatalf("路况查询失败: %v", err)
	}
	for _, want := range []string{"畅通 85.7%", "缓行 9.5%", "中河高架", "缓行", "32.5 km/h"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestAmapTrafficFallsBackToCircle(t *testing.T) {
	var circleCalled bool
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/traffic/status/road":
			amapJSON(t, w, map[string]interface{}{
				"status":   "0",
				"info":     "UNKNOWN_ERROR",
				"infocode": "20003",
			})
		case "/v3/geocode/geo":
			amapJSON(t, w, amapOK(map[string]interface{}{
				"geocodes": []interface{}{
					map[string]interface{}{"location": "120.146278,30.278893", "city": "杭州市"},
				},
			}))
		case "/v3/traffic/status/circle":
			circleCalled = true
			amapJSON(t, w, amapOK(map[string]interface{}{
				"trafficinfo": map[string]interface{}{
					"description": "文三路：双向畅通。",
					"evaluation":  map[string]interface{}{"status": "1"},
				},
			}))
		default:
			t.Fatalf("意外路径: %s", r.URL.Path)
		}
	})

	out, err := p.callAmapInfo(context.Background(), map[string]interface{}{
		"action": "traffic",
		"kind":   "road",
		"road":   "文三路",
		"city":   "杭州",
	})
	if err != nil {
		t.Fatalf("路况查询失败: %v", err)
	}
	if !circleCalled {
		t.Fatal("道路未覆盖时应退化为圆形区域查询")
	}
	if !strings.Contains(out, "2km 圆形区域") {
		t.Fatalf("退化说明缺失:\n%s", out)
	}
}

func TestAmapMapImageURL(t *testing.T) {
	p, _ := newAmapTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("静态地图不应发起请求: %s", r.URL.Path)
	})

	out, err := p.callAmapMapImage(context.Background(), map[string]interface{}{
		"location": "120.16,30.28",
		"width":    float64(400),
		"height":   float64(300),
	})
	if err != nil {
		t.Fatalf("生成静态地图失败: %v", err)
	}
	for _, want := range []string{"/v3/staticmap", "size=400%2A300", "key=test-key", "内含 API Key"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestAmapToolsRegisteredOnlyWhenEnabled(t *testing.T) {
	disabled := &plugin{cfg: config.AgentConfig{}}
	for _, tool := range disabled.toolDefinitions() {
		if strings.HasPrefix(tool.Function.Name, "amap_") {
			t.Fatalf("未启用时不应注册 %s", tool.Function.Name)
		}
	}

	enabled := &plugin{cfg: config.AgentConfig{Amap: config.AmapConfig{Enabled: true}}}
	names := make(map[string]bool)
	for _, tool := range enabled.toolDefinitions() {
		names[tool.Function.Name] = true
	}
	for _, want := range []string{"amap_route", "amap_place", "amap_info", "amap_map_image"} {
		if !names[want] {
			t.Fatalf("启用后应注册 %s", want)
		}
	}
}

func TestAmapGuard(t *testing.T) {
	// 未启用
	disabled := &plugin{cfg: config.AgentConfig{Amap: config.AmapConfig{Enabled: false}}}
	if err := disabled.amapGuard(nil); err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("未启用时应拒绝调用，实际: %v", err)
	}

	// 启用但 ownerOnly，无 ctx（等价于非主人）
	ownerOnly := &plugin{
		cfg:        config.AgentConfig{Amap: config.AmapConfig{Enabled: true, OwnerOnly: true}},
		superUsers: []int64{10001},
	}
	if err := ownerOnly.amapGuard(nil); err == nil || !strings.Contains(err.Error(), "仅主人") {
		t.Fatalf("ownerOnly 且无 ctx 时应拒绝，实际: %v", err)
	}

	// 启用且不限制主人
	open := &plugin{cfg: config.AgentConfig{Amap: config.AmapConfig{Enabled: true}}}
	if err := open.amapGuard(nil); err != nil {
		t.Fatalf("启用且非 ownerOnly 时应放行，实际: %v", err)
	}
}
