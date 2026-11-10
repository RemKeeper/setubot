package agent

// 真实接口冒烟测试：默认跳过，需要显式开启，避免 CI 依赖外网与配额。
//
//	AMAP_API_KEY=xxx go test -run TestAmapLive -tags live ./internal/plugins/agent/ -v
//
// 或直接：
//
//	AMAP_LIVE=1 AMAP_API_KEY=xxx go test -run TestAmapLive ./internal/plugins/agent/ -v

import (
	"context"
	"os"
	"strings"
	"testing"

	"setubot/internal/config"
)

func liveAmapPlugin(t *testing.T) *plugin {
	t.Helper()
	if os.Getenv("AMAP_LIVE") == "" {
		t.Skip("跳过真实接口测试：设置 AMAP_LIVE=1 与 AMAP_API_KEY 后运行")
	}
	key := strings.TrimSpace(os.Getenv("AMAP_API_KEY"))
	if key == "" {
		t.Skip("跳过真实接口测试：AMAP_API_KEY 未设置")
	}
	return &plugin{
		cfg: config.AgentConfig{
			Amap: config.AmapConfig{
				Enabled:  true,
				APIKey:   key,
				BaseURL:  "https://restapi.amap.com",
				Timeout:  15,
				CacheTTL: 0,
			},
		},
		amapCache: make(map[string]amapCacheEntry),
	}
}

func TestAmapLiveRouteDriving(t *testing.T) {
	p := liveAmapPlugin(t)
	out, err := p.callAmapRoute(context.Background(), map[string]interface{}{
		"mode":        "driving",
		"origin":      "杭州西湖文化广场",
		"destination": "杭州东站",
		"city":        "杭州",
	})
	if err != nil {
		t.Fatalf("真实驾车规划失败: %v", err)
	}
	t.Logf("\n%s", out)
	if !strings.Contains(out, "km") || !strings.Contains(out, "分钟") {
		t.Fatalf("输出缺少距离/时长: %s", out)
	}
}

func TestAmapLiveRouteTransit(t *testing.T) {
	p := liveAmapPlugin(t)
	out, err := p.callAmapRoute(context.Background(), map[string]interface{}{
		"mode":        "transit",
		"origin":      "120.163755,30.276652",
		"destination": "120.212605,30.290846",
		"city":        "杭州",
	})
	if err != nil {
		t.Fatalf("真实公交规划失败: %v", err)
	}
	t.Logf("\n%s", out)
}

func TestAmapLivePlaceAndInfo(t *testing.T) {
	p := liveAmapPlugin(t)
	ctx := context.Background()

	geo, err := p.callAmapPlace(ctx, map[string]interface{}{
		"action":  "geocode",
		"address": "杭州市西湖区文三路90号",
		"city":    "杭州",
	})
	if err != nil {
		t.Fatalf("真实地理编码失败: %v", err)
	}
	t.Logf("\n%s", geo)

	poi, err := p.callAmapPlace(ctx, map[string]interface{}{
		"action":   "poi",
		"keywords": "咖啡",
		"location": "120.16,30.28",
		"radius":   float64(2000),
	})
	if err != nil {
		t.Fatalf("真实 POI 检索失败: %v", err)
	}
	t.Logf("\n%s", poi)

	weather, err := p.callAmapInfo(ctx, map[string]interface{}{"action": "weather", "city": "杭州"})
	if err != nil {
		t.Fatalf("真实天气查询失败: %v", err)
	}
	t.Logf("\n%s", weather)

	traffic, err := p.callAmapInfo(ctx, map[string]interface{}{
		"action":   "traffic",
		"kind":     "circle",
		"location": "120.16,30.28",
		"radius":   float64(1000),
	})
	if err != nil {
		t.Fatalf("真实路况查询失败: %v", err)
	}
	t.Logf("\n%s", traffic)
}
