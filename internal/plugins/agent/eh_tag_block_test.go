package agent

import (
	"strings"
	"testing"
)

func TestEHTagBlockerExactMatch(t *testing.T) {
	blocker := newEHTagBlocker([]string{"female:lolicon"})

	if !blocker.blocked("female", "lolicon") {
		t.Fatal("应命中精确规则")
	}
	if blocker.blocked("male", "lolicon") {
		t.Fatal("精确规则不应跨命名空间命中")
	}
	if blocker.blocked("female", "milf") {
		t.Fatal("不应命中未配置的标签")
	}
}

func TestEHTagBlockerKeyOnly(t *testing.T) {
	blocker := newEHTagBlocker([]string{"lolicon"})

	if !blocker.blocked("female", "lolicon") {
		t.Fatal("key 规则应命中任意命名空间")
	}
	if !blocker.blocked("male", "lolicon") {
		t.Fatal("key 规则应命中任意命名空间")
	}
	if blocker.blocked("female", "milf") {
		t.Fatal("不应命中未配置的标签")
	}
}

func TestEHTagBlockerNamespaceWildcard(t *testing.T) {
	blocker := newEHTagBlocker([]string{"female:*"})

	if !blocker.blocked("female", "anything") {
		t.Fatal("命名空间通配应命中任意 key")
	}
	if blocker.blocked("male", "anything") {
		t.Fatal("不应跨命名空间命中")
	}
}

func TestEHTagBlockerNormalization(t *testing.T) {
	blocker := newEHTagBlocker([]string{"  Female:LoliCon  ", "big_breasts"})

	if !blocker.blocked("female", "lolicon") {
		t.Fatal("应忽略大小写与空白")
	}
	// 下划线与空格等价
	if !blocker.blocked("female", "big breasts") {
		t.Fatal("下划线与空格应等价")
	}
	if !blocker.blocked("female", "big_breasts") {
		t.Fatal("下划线与空格应等价")
	}
}

func TestEHTagBlockerEmpty(t *testing.T) {
	if !newEHTagBlocker(nil).empty() {
		t.Fatal("空配置应为 empty")
	}
	if !newEHTagBlocker([]string{"", "  "}).empty() {
		t.Fatal("全空白配置应为 empty")
	}
	if newEHTagBlocker([]string{"female:lolicon"}).empty() {
		t.Fatal("有规则时不应为 empty")
	}
	// nil blocker 不应 panic
	var nilBlocker *ehTagBlocker
	if nilBlocker.blocked("female", "lolicon") {
		t.Fatal("nil blocker 应返回 false")
	}
	if !nilBlocker.empty() {
		t.Fatal("nil blocker 应为 empty")
	}
}

func TestEHTagBlockerSearchValue(t *testing.T) {
	blocker := newEHTagBlocker([]string{"female:lolicon", "male:*"})

	if !blocker.blockedSearchValue("female:lolicon") {
		t.Fatal("应命中搜索表达式")
	}
	if !blocker.blockedSearchValue("male:anything") {
		t.Fatal("应命中命名空间通配")
	}
	if blocker.blockedSearchValue("language:chinese") {
		t.Fatal("不应命中未配置的标签")
	}

	kept, removed := blocker.filterSearchValues([]string{"language:chinese", "female:lolicon", "male:yaoi"})
	if removed != 2 || len(kept) != 1 || kept[0] != "language:chinese" {
		t.Fatalf("过滤结果错误: kept=%v removed=%d", kept, removed)
	}
}

func TestRenderEHSearchMarkdownBlocksTags(t *testing.T) {
	body := loadEHSample(t, "eh_search_raw.html")
	blocker := newEHTagBlocker([]string{"female:lolicon", "female:milf"})
	md := renderEHSearchMarkdown(body, blocker)

	if strings.Contains(md, "female:lolicon") {
		t.Fatal("被屏蔽标签不应出现在搜索结果中")
	}
	if strings.Contains(md, "female:milf") {
		t.Fatal("被屏蔽标签不应出现在搜索结果中")
	}
	// 其它标签应保留
	if !strings.Contains(md, "language:chinese") {
		t.Fatal("未屏蔽的标签应保留")
	}
	if !strings.Contains(md, "parody:bocchi the rock") {
		t.Fatal("未屏蔽的标签应保留")
	}
}

func TestRenderEHGalleryMarkdownBlocksTags(t *testing.T) {
	body := loadEHSample(t, "eh_gallery_raw.html")
	blocker := newEHTagBlocker([]string{"female:lolicon", "female:milf", "female:shemale"})
	md := renderEHGalleryMarkdown(body, blocker)

	if strings.Contains(md, "lolicon") {
		t.Fatal("被屏蔽标签不应出现在详情页中")
	}
	if strings.Contains(md, "milf") {
		t.Fatal("被屏蔽标签不应出现在详情页中")
	}
	if strings.Contains(md, "shemale") {
		t.Fatal("被屏蔽标签不应出现在详情页中")
	}
	// 同命名空间其它标签应保留
	if !strings.Contains(md, "**female**: big breasts") {
		t.Fatal("未屏蔽的标签应保留")
	}
	if !strings.Contains(md, "**language**: chinese, translated") {
		t.Fatal("未屏蔽的命名空间应保留")
	}
}

func TestRenderEHGalleryMarkdownBlocksWholeNamespace(t *testing.T) {
	body := loadEHSample(t, "eh_gallery_raw.html")
	blocker := newEHTagBlocker([]string{"female:*"})
	md := renderEHGalleryMarkdown(body, blocker)

	if strings.Contains(md, "**female**") {
		t.Fatal("整个命名空间被屏蔽时不应输出该行")
	}
	if !strings.Contains(md, "**language**: chinese, translated") {
		t.Fatal("其它命名空间应保留")
	}
}

func TestFilterEHTagMatches(t *testing.T) {
	p := &plugin{ehTagBlocker: newEHTagBlocker([]string{"female:lolicon"})}
	matches := []ehTagMatch{
		{Namespace: "female", Key: "lolicon", SearchValue: "female:lolicon"},
		{Namespace: "female", Key: "milf", SearchValue: "female:milf"},
		{Namespace: "language", Key: "chinese", SearchValue: "language:chinese"},
	}

	kept := p.filterEHTagMatches(matches)
	if len(kept) != 2 {
		t.Fatalf("应保留 2 条，实际 %d", len(kept))
	}
	for _, match := range kept {
		if match.Key == "lolicon" {
			t.Fatal("被屏蔽标签未被剔除")
		}
	}
}

func TestFilterEHTagResolveResultFallsBack(t *testing.T) {
	p := &plugin{ehTagBlocker: newEHTagBlocker([]string{"female:lolicon"})}
	result := map[string]interface{}{
		"originalKeyword": "萝莉",
		"resolvedKeyword": "female:lolicon",
		"source":          "tagTranslation",
		"selected":        ehTagMatch{Namespace: "female", Key: "lolicon", SearchValue: "female:lolicon"},
		"candidates": []ehTagMatch{
			{Namespace: "female", Key: "lolicon", SearchValue: "female:lolicon"},
			{Namespace: "female", Key: "milf", SearchValue: "female:milf"},
		},
		"warnings": []string{},
	}

	p.filterEHTagResolveResult(result)

	if _, ok := result["selected"]; ok {
		t.Fatal("被屏蔽的 selected 应被移除")
	}
	if result["resolvedKeyword"] != "萝莉" {
		t.Fatalf("resolvedKeyword 应回退为原始关键词，实际 %v", result["resolvedKeyword"])
	}
	if result["source"] != "rawKeyword" {
		t.Fatalf("source 应回退为 rawKeyword，实际 %v", result["source"])
	}
	candidates, _ := result["candidates"].([]ehTagMatch)
	if len(candidates) != 1 || candidates[0].Key != "milf" {
		t.Fatalf("candidates 应剔除屏蔽项: %+v", candidates)
	}
	warnings, _ := result["warnings"].([]string)
	if len(warnings) == 0 {
		t.Fatal("应追加回退警告")
	}
}

func TestFilterEHTagTranslations(t *testing.T) {
	p := &plugin{ehTagBlocker: newEHTagBlocker([]string{"female:lolicon"})}
	results := []map[string]interface{}{
		{"namespace": "female", "key": "lolicon", "found": true},
		{"namespace": "female", "key": "milf", "found": true},
	}

	kept := p.filterEHTagTranslations(results)
	if len(kept) != 1 || kept[0]["key"] != "milf" {
		t.Fatalf("翻译结果过滤错误: %+v", kept)
	}
}

func TestEHTagBlockerDisabledByDefault(t *testing.T) {
	p := &plugin{ehTagBlocker: newEHTagBlocker(nil)}
	body := loadEHSample(t, "eh_gallery_raw.html")
	md := renderEHGalleryMarkdown(body, p.ehTagBlocker)

	if !strings.Contains(md, "lolicon") {
		t.Fatal("未配置屏蔽时不应剔除任何标签")
	}
}
