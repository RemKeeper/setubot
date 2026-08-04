package agent

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"setubot/internal/config"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
}

func TestVisionImageDataURLDownloadsRemoteImage(t *testing.T) {
	var gotAccept, gotUserAgent, gotReferer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotUserAgent = r.Header.Get("User-Agent")
		gotReferer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(tinyPNG)
	}))
	defer server.Close()

	p := &plugin{
		cfg:        config.AgentConfig{Vision: config.VisionConfig{MaxImageBytes: 1024}},
		httpClient: server.Client(),
	}
	dataURL, size, contentType, err := p.visionImageDataURL(server.URL + "/image?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	if size != len(tinyPNG) || contentType != "image/png" {
		t.Fatalf("unexpected image metadata: size=%d contentType=%q", size, contentType)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	if dataURL != want {
		t.Fatalf("unexpected data URL: %q", dataURL)
	}
	if !strings.Contains(gotAccept, "image/") || gotUserAgent == "" || gotReferer != server.URL+"/" {
		t.Fatalf("unexpected request headers: accept=%q userAgent=%q referer=%q", gotAccept, gotUserAgent, gotReferer)
	}
}

func TestVisionImageDataURLRejectsOversizedImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer server.Close()

	p := &plugin{
		cfg:        config.AgentConfig{Vision: config.VisionConfig{MaxImageBytes: 8}},
		httpClient: server.Client(),
	}
	_, _, _, err := p.visionImageDataURL(server.URL)
	if err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("expected oversized image error, got %v", err)
	}
}

func TestVisionImageDataURLReencodesExistingDataURL(t *testing.T) {
	p := &plugin{cfg: config.AgentConfig{Vision: config.VisionConfig{MaxImageBytes: 1024}}}
	source := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	dataURL, size, contentType, err := p.visionImageDataURL(source)
	if err != nil {
		t.Fatal(err)
	}
	if dataURL != source || size != len(tinyPNG) || contentType != "image/png" {
		t.Fatalf("unexpected data URL result: %q %d %q", dataURL, size, contentType)
	}
}

func TestVisionImageDataURLRejectsNonImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not image</html>"))
	}))
	defer server.Close()

	p := &plugin{
		cfg:        config.AgentConfig{Vision: config.VisionConfig{MaxImageBytes: 1024}},
		httpClient: server.Client(),
	}
	_, _, _, err := p.visionImageDataURL(server.URL)
	if err == nil || !strings.Contains(err.Error(), "不是支持的图片") {
		t.Fatalf("expected non-image error, got %v", err)
	}
}
