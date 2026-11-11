package agent

// 静态地图真实接口验证：确认工具生成的 URL 真的能出图（而不是返回 20003 JSON）。
//
//	AMAP_LIVE=1 AMAP_API_KEY=xxx go test -run TestAmapLiveStaticMap ./internal/plugins/agent/ -v

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAmapLiveStaticMap(t *testing.T) {
	p := liveAmapPlugin(t)

	cases := []struct {
		name string
		args map[string]interface{}
	}{
		{
			name: "仅中心点",
			args: map[string]interface{}{"center": "113.47,22.96", "zoom": float64(10)},
		},
		{
			name: "两地标记点（不传中心）",
			args: map[string]interface{}{
				"points": []interface{}{"起点:113.402944,23.122033", "终点:113.532737,22.795537"},
				"zoom":   float64(10),
			},
		},
		{
			name: "中文地名中心",
			args: map[string]interface{}{"center": "广州塔", "city": "广州", "zoom": float64(14)},
		},
		{
			name: "带路况图层",
			args: map[string]interface{}{"center": "120.16,30.28", "zoom": float64(13), "show_traffic": true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := p.callAmapMapImage(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("生成失败: %v", err)
			}
			// 从输出里取出 URL
			var imageURL string
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "https://") {
					imageURL = strings.TrimSpace(line)
					break
				}
			}
			if imageURL == "" {
				t.Fatalf("输出里没有 URL:\n%s", out)
			}

			resp, err := http.Get(imageURL)
			if err != nil {
				t.Fatalf("请求图片失败: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			contentType := resp.Header.Get("Content-Type")

			if strings.Contains(contentType, "json") {
				// 配额类错误不是格式问题，跳过而不是失败
				if strings.Contains(string(body), "10045") || strings.Contains(string(body), "OVER_LIMIT") {
					t.Skipf("配额超限，跳过: %s", string(body))
				}
				t.Fatalf("高德返回了 JSON 错误而不是图片：%s", string(body))
			}
			if !strings.Contains(contentType, "image") {
				t.Fatalf("Content-Type 异常: %s", contentType)
			}
			t.Logf("✓ %s -> %s (%d bytes)", tc.name, contentType, len(body))
		})
	}
}
