package agent

import (
	"os"
	"strings"
	"testing"
)

func loadEHSample(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv("EH_SAMPLE_DIR")
	if path == "" {
		t.Skip("EH_SAMPLE_DIR 未设置，跳过真实 HTML 样本测试")
	}
	data, err := os.ReadFile(path + string(os.PathSeparator) + name)
	if err != nil {
		t.Skipf("读取样本失败: %v", err)
	}
	return string(data)
}

func TestRenderEHSearchMarkdownFromSample(t *testing.T) {
	body := loadEHSample(t, "eh_search_raw.html")
	md := renderEHSearchMarkdown(body, nil)

	if !strings.Contains(md, "language:chinese") {
		t.Fatalf("缺少搜索关键词: %s", md[:200])
	}
	if !strings.Contains(md, "Found 260,976 results") {
		t.Fatal("缺少结果总数")
	}
	if !strings.Contains(md, "布局 gltc") {
		t.Fatal("缺少布局探测结果")
	}
	if strings.Count(md, "\n## ") != 25 {
		t.Fatalf("结果条数应为 25，实际 %d", strings.Count(md, "\n## "))
	}
	if !strings.Contains(md, "`4323509` / `00a19e5324`") {
		t.Fatal("缺少 gid/token")
	}
	if !strings.Contains(md, "https://exhentai.org/g/4323509/00a19e5324/") {
		t.Fatal("缺少作品链接")
	}
	// 半星：-16px -21px => 5 - 1 - 0.5 = 3.5
	if !strings.Contains(md, "★3.5") {
		t.Fatal("半星评分换算错误")
	}
	if !strings.Contains(md, "★5") {
		t.Fatal("满星评分换算错误")
	}
	if !strings.Contains(md, "language:chinese") || !strings.Contains(md, "parody:bocchi the rock") {
		t.Fatal("缺少标签")
	}
	if !strings.Contains(md, "下一页: https://exhentai.org/?f_search=language:chinese&next=4323082") {
		t.Fatal("缺少下一页链接")
	}
	if strings.Contains(md, "<div") || strings.Contains(md, "<td") {
		t.Fatal("markdown 中残留 HTML 标签")
	}
}

func TestRenderEHSearchMarkdownNoHits(t *testing.T) {
	body := loadEHSample(t, "eh_search_empty.html")
	md := renderEHSearchMarkdown(body, nil)

	if !strings.Contains(md, "无结果") {
		t.Fatalf("应提示无结果: %s", md)
	}
	if strings.Contains(md, "## 1.") {
		t.Fatal("无结果时不应有条目")
	}
}

func TestRenderEHGalleryMarkdownFromSample(t *testing.T) {
	body := loadEHSample(t, "eh_gallery_raw.html")
	md := renderEHGalleryMarkdown(body, nil)

	if !strings.Contains(md, "Gotoh Bowl") {
		t.Fatal("缺少标题")
	}
	if !strings.Contains(md, "- 分类: Manga") {
		t.Fatal("缺少分类")
	}
	if !strings.Contains(md, "- 上传者: gtsloverq") {
		t.Fatal("缺少上传者")
	}
	if !strings.Contains(md, "- Length: 17 pages") {
		t.Fatal("缺少页数")
	}
	if !strings.Contains(md, "- 平均评分: 4.61/5") {
		t.Fatal("缺少平均评分")
	}
	if !strings.Contains(md, "14 人评分") {
		t.Fatal("缺少评分人数")
	}
	if !strings.Contains(md, "- Favorited: 229 times") {
		t.Fatal("缺少收藏数")
	}
	// gdd 已有 Favorited 行时不应重复输出「收藏」行。
	if strings.Contains(md, "- 收藏:") {
		t.Fatal("收藏数重复输出")
	}
	// 标签按命名空间分组，下划线转空格
	if !strings.Contains(md, "**parody**: bocchi the rock") {
		t.Fatal("标签命名空间解析错误")
	}
	if !strings.Contains(md, "**character**: hitori gotou") {
		t.Fatal("标签 key 下划线未转空格")
	}
	if !strings.Contains(md, "**language**: chinese, translated") {
		t.Fatal("同命名空间多标签未合并")
	}
	// gtl 是低置信标记，翻译本里极常见，不应作为噪音输出。
	if strings.Contains(md, "(存疑)") {
		t.Fatal("gtl 不应输出存疑标记")
	}
	// gtw 表示被标记为错误，需要保留提示。
	if !strings.Contains(md, "lactation (错误)") {
		t.Fatal("gtw 应输出错误标记")
	}
	// 新结构缩略图：多页共用雪碧图，只输出图片页入口且不重复。
	if !strings.Contains(md, "p1: https://exhentai.org/s/f07ccb1be1/4323509-1") {
		t.Fatal("缺少缩略图入口")
	}
	if strings.Contains(md, "hath.network") {
		t.Fatal("雪碧图 URL 不应输出")
	}
	if !strings.Contains(md, "缩略图入口（17 个）") {
		t.Fatal("缩略图数量错误")
	}
	if strings.Contains(md, "<div") || strings.Contains(md, "<td") {
		t.Fatal("markdown 中残留 HTML 标签")
	}
}

func TestRenderEHGalleryCommentsFromSample(t *testing.T) {
	body := loadEHSample(t, "eh_gallery_comments.html")
	md := renderEHGalleryMarkdown(body, nil)

	if !strings.Contains(md, "## 评论") {
		t.Fatal("缺少评论区块")
	}
	// 上传者评论
	if !strings.Contains(md, "### Baixun（上传者）") {
		t.Fatal("未识别上传者评论")
	}
	// 普通评论带评分
	if !strings.Contains(md, "### ismhd") {
		t.Fatal("缺少普通评论作者")
	}
	if !strings.Contains(md, "评分 +851") {
		t.Fatal("缺少评论评分")
	}
	if !strings.Contains(md, "展示军火↑") {
		t.Fatal("缺少评论正文")
	}
	// 时间提取
	if !strings.Contains(md, "20 October 2026, 17:55") {
		t.Fatal("缺少评论时间")
	}
	// 条数上限
	if !strings.Contains(md, "其余 123 条评论省略") {
		t.Fatalf("评论截断提示错误")
	}
	if strings.Count(md, "\n### ") != ehMarkdownMaxComments {
		t.Fatalf("评论条数应为 %d，实际 %d", ehMarkdownMaxComments, strings.Count(md, "\n### "))
	}
	// 单条正文截断
	if !strings.Contains(md, "…") {
		t.Fatal("超长评论未截断")
	}
	if strings.Contains(md, "<div") || strings.Contains(md, "<br") {
		t.Fatal("评论中残留 HTML")
	}
}

func TestParseEHCommentTime(t *testing.T) {
	cases := map[string]string{
		"Posted on 12 November 2026, 06:22 by: Baixun": "12 November 2026, 06:22",
		"Posted on 20 October 2026, 17:55":             "20 October 2026, 17:55",
		"":                                             "",
	}
	for raw, want := range cases {
		if got := parseEHCommentTime(raw); got != want {
			t.Fatalf("parseEHCommentTime(%q)=%q, want %q", raw, got, want)
		}
	}
}

func TestRenderEHCommentsMarkdownNoComments(t *testing.T) {
	if got := renderEHCommentsMarkdown("<html><body>no comments</body></html>"); got != "" {
		t.Fatalf("无评论时应返回空串，实际 %q", got)
	}
}

func TestEHRatingFromSprite(t *testing.T) {
	cases := []struct {
		x, y int
		want float64
	}{
		{0, -1, 5},
		{0, -21, 4.5},
		{-16, -1, 4},
		{-16, -21, 3.5},
		{-32, -1, 3},
		{-48, -1, 2},
		{-64, -1, 1},
	}
	for _, c := range cases {
		if got := ehRatingFromSprite(c.x, c.y); got != c.want {
			t.Fatalf("ehRatingFromSprite(%d,%d)=%v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestNormalizeEHFormat(t *testing.T) {
	if normalizeEHFormat("") != ehFormatRaw {
		t.Fatal("空值应为 raw")
	}
	if normalizeEHFormat("MARKDOWN") != ehFormatMarkdown {
		t.Fatal("应大小写不敏感")
	}
	if normalizeEHFormat(" markdown ") != ehFormatMarkdown {
		t.Fatal("应忽略空白")
	}
	if normalizeEHFormat("html") != ehFormatRaw {
		t.Fatal("未知值应回退 raw")
	}
}

func TestRenderEHMarkdownQuotaAndLogin(t *testing.T) {
	quota := renderEHMarkdown(ehReqResult{OK: true, Body: "You have exceeded the image viewing limit"}, "search", nil)
	if !strings.Contains(quota, "配额限制") {
		t.Fatalf("应识别配额页: %s", quota)
	}
	login := renderEHMarkdown(ehReqResult{OK: true, Body: "This page requires you to be logged in"}, "gallery", nil)
	if !strings.Contains(login, "需要登录态") {
		t.Fatalf("应识别登录页: %s", login)
	}
	failed := renderEHMarkdown(ehReqResult{OK: false, StatusCode: 404}, "search", nil)
	if !strings.Contains(failed, "HTTP 404") {
		t.Fatalf("应提示 HTTP 错误: %s", failed)
	}
}
