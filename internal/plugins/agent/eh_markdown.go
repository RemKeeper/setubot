package agent

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

const (
	ehFormatRaw      = "raw"
	ehFormatMarkdown = "markdown"

	// ehMarkdownMaxThumbs 限制详情页 markdown 中列出的缩略图入口数量，避免超长作品撑爆上下文。
	ehMarkdownMaxThumbs = 40
	// ehMarkdownMaxComments 限制详情页 markdown 中列出的评论条数。
	ehMarkdownMaxComments = 20
	// ehMarkdownMaxCommentChars 限制单条评论正文长度，超出截断。
	ehMarkdownMaxCommentChars = 500
)

var (
	// 搜索页：布局探测（对应 JHenTai 的 .itg.gltm/.gltc/.glte/.gld）
	ehSearchLayoutPattern = regexp.MustCompile(`(?i)class="itg (gltm|gltc|glte|gld)"`)

	ehSearchQueryPattern    = regexp.MustCompile(`(?is)<input[^>]*name="f_search"[^>]*value="([^"]*)"`)
	ehSearchRowPattern      = regexp.MustCompile(`(?is)<tr>(.*?)</tr>`)
	ehSearchGLinkPattern    = regexp.MustCompile(`(?is)<a href="(https?://[^"]*/g/(\d+)/([0-9a-zA-Z]+)/)"><div class="glink">(.*?)</div>`)
	ehSearchCatPattern      = regexp.MustCompile(`(?is)<div class="(?:cn|cs)[^"]*"[^>]*>([^<]*)</div>`)
	ehSearchTagPattern      = regexp.MustCompile(`(?is)<div class="gt" title="([^"]*)">`)
	ehSearchPagesPattern    = regexp.MustCompile(`(?is)<div>\s*([0-9,]+)\s+pages?\s*</div>`)
	ehSearchPostedPattern   = regexp.MustCompile(`(?is)id="posted_\d+">([^<]*)</div>`)
	ehSearchUploaderPattern = regexp.MustCompile(`(?is)/uploader/[^"]*">([^<]*)</a>`)
	ehSearchRatingPattern   = regexp.MustCompile(`(?is)<div class="ir" style="background-position:(-?\d+)px (-?\d+)px`)
	ehSearchThumbPattern    = regexp.MustCompile(`(?is)<img[^>]*?(?:data-src|src)="(https?://[^"]+)"`)
	ehSearchNoHitsPattern   = regexp.MustCompile(`(?i)No hits found`)
	ehSearchTotalPattern    = regexp.MustCompile(`(?is)class="searchtext"[^>]*>\s*<p>\s*([^<]+)</p>`)
	ehSearchNextPattern     = regexp.MustCompile(`(?is)id="unext"[^>]*href="([^"]*)"`)
	ehSearchPrevPattern     = regexp.MustCompile(`(?is)id="uprev"[^>]*href="([^"]*)"`)

	// 详情页
	ehGalleryCatPattern       = regexp.MustCompile(`(?is)<div id="gdc"><div class="cs[^"]*"[^>]*>([^<]*)</div>`)
	ehGalleryUploaderPattern  = regexp.MustCompile(`(?is)<div id="gdn"><a href="[^"]*">([^<]*)</a>`)
	ehGalleryMetaPattern      = regexp.MustCompile(`(?is)<td class="gdt1">([^<]*)</td><td class="gdt2"[^>]*>(.*?)</td>`)
	ehGalleryAvgPattern       = regexp.MustCompile(`(?is)Average:\s*([0-9.]+)`)
	ehGalleryRatingCntPattern = regexp.MustCompile(`(?is)id="rating_count">([^<]*)<`)
	ehGalleryFavCountPattern  = regexp.MustCompile(`(?is)id="favcount"[^>]*>([^<]*)<`)
	// 详情页标签：id="td_{namespace}:{key}" class="gt|gtl|gtw"，class 表示标签状态。
	ehGalleryTagPattern = regexp.MustCompile(`(?is)<div id="td_([^"]+)" class="(gt|gtl|gtw)"`)
	// 详情页评论：每个 <div class="c1"> 是一条评论，c3=时间/作者，c5=评分，c6=正文，c8=编辑时间。
	// RE2 不支持 lookahead，这里只定位起始位置，正文按索引切片。
	ehGalleryCommentStartPattern = regexp.MustCompile(`(?is)<div class="c1">`)
	ehCommentAuthorPattern       = regexp.MustCompile(`(?is)<div class="c3">(.*?)</div>`)
	ehCommentAuthorLinkPattern   = regexp.MustCompile(`(?is)<a href="[^"]*">([^<]*)</a>`)
	ehCommentScorePattern        = regexp.MustCompile(`(?is)<div class="c5[^"]*"[^>]*>.*?<span[^>]*>([^<]*)</span>`)
	ehCommentBodyPattern         = regexp.MustCompile(`(?is)<div class="c6"[^>]*>(.*?)</div>`)
	ehCommentEditedPattern       = regexp.MustCompile(`(?is)<div class="c8"[^>]*>.*?<strong>([^<]*)</strong>`)
	ehCommentUploaderPattern     = regexp.MustCompile(`(?i)Uploader Comment`)
	// 详情页缩略图（2024-10-15 后的新结构）：a[href] > div[title][style: url(...) offset]
	// offset 存在时表示雪碧图切片，url 是整张雪碧图而非单页缩略图。
	ehGalleryThumbNewPattern = regexp.MustCompile(`(?is)<a href="([^"]+)"><div title="([^"]*)" style="[^"]*url\(([^)]+)\)\s*(-?\d+)px`)
	ehThumbPageNoPattern     = regexp.MustCompile(`-(\d+)$`)

	ehQuotaMarkers = []string{
		"exceeded the image viewing limit",
		"temporarily banned",
		"image viewing quota",
		"509 bandwidth limit",
	}
	ehLoginMarkers = []string{
		"this page requires you to be logged in",
		"you must be logged in",
		"please log in",
	}
)

// ehMarkdownResult 是 format=markdown 时的返回结构，用 markdown 字段替代原始 body。
type ehMarkdownResult struct {
	OK           bool   `json:"ok"`
	Site         string `json:"site"`
	URL          string `json:"url"`
	StatusCode   int    `json:"statusCode"`
	Format       string `json:"format"`
	CookieLoaded bool   `json:"cookieLoaded"`
	ProxyEnabled bool   `json:"proxyEnabled"`
	Truncated    bool   `json:"truncated,omitempty"`
	Markdown     string `json:"markdown"`
}

func normalizeEHFormat(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), ehFormatMarkdown) {
		return ehFormatMarkdown
	}

	return ehFormatRaw
}

// renderEHMarkdownResult 把一次 EH 请求结果转成 agent 友好的 markdown 结构。
func renderEHMarkdownResult(res ehReqResult, kind string, blocker *ehTagBlocker) (string, error) {
	return renderJSON(ehMarkdownResult{
		OK:           res.OK,
		Site:         res.Site,
		URL:          res.URL,
		StatusCode:   res.StatusCode,
		Format:       ehFormatMarkdown,
		CookieLoaded: res.CookieLoaded,
		ProxyEnabled: res.ProxyEnabled,
		Truncated:    res.Truncated,
		Markdown:     renderEHMarkdown(res, kind, blocker),
	})
}

func renderEHMarkdown(res ehReqResult, kind string, blocker *ehTagBlocker) string {
	if !res.OK {
		return fmt.Sprintf("> 请求失败：HTTP %d\n", res.StatusCode)
	}
	lower := strings.ToLower(res.Body)
	for _, marker := range ehQuotaMarkers {
		if strings.Contains(lower, marker) {
			return "> 触发 EH 配额限制（509 / image viewing limit），请稍后重试\n"
		}
	}
	for _, marker := range ehLoginMarkers {
		if strings.Contains(lower, marker) {
			return "> 需要登录态：Cookie 无效或已过期\n"
		}
	}

	var md string
	if kind == "gallery" {
		md = renderEHGalleryMarkdown(res.Body, blocker)
	} else {
		md = renderEHSearchMarkdown(res.Body, blocker)
	}
	if res.Truncated {
		md += "\n> 注意：原始 HTML 已被截断，结果可能不完整\n"
	}

	return md
}

// ehRatingFromSprite 按 JHenTai 的算法把星级雪碧图坐标换算成 0-5 分。
// x 每 -16px 减 1 星；y=-21px 表示半星。
func ehRatingFromSprite(x int, y int) float64 {
	rating := 5.0
	rating -= float64(-x) / 16.0
	if y == -21 {
		rating -= 0.5
	}
	if rating < 0 {
		rating = 0
	}

	return rating
}

func formatEHRating(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func renderEHSearchMarkdown(body string, blocker *ehTagBlocker) string {
	var sb strings.Builder
	query := stripHTMLText(firstSubmatch(ehSearchQueryPattern, body))
	if query != "" {
		fmt.Fprintf(&sb, "# EH 搜索结果：`%s`\n\n", query)
	} else {
		sb.WriteString("# EH 搜索结果\n\n")
	}
	if ehSearchNoHitsPattern.MatchString(body) {
		sb.WriteString("> 无结果（No hits found）\n")
		return sb.String()
	}

	layout := "compact"
	if m := ehSearchLayoutPattern.FindStringSubmatch(body); m != nil {
		layout = strings.ToLower(m[1])
	}
	if total := stripHTMLText(firstSubmatch(ehSearchTotalPattern, body)); total != "" {
		fmt.Fprintf(&sb, "%s · 布局 %s\n\n", total, layout)
	}

	count := 0
	for _, row := range ehSearchRowPattern.FindAllStringSubmatch(body, -1) {
		content := row[1]
		// 跳过表头与广告行（对应 JHenTai 的 children.length != 1 && querySelector('th') == null）。
		if strings.Contains(strings.ToLower(content), "<th") {
			continue
		}
		link := ehSearchGLinkPattern.FindStringSubmatch(content)
		if link == nil {
			continue
		}
		count++
		gid, token, galleryURL := link[2], link[3], link[1]

		fmt.Fprintf(&sb, "## %d. %s\n\n", count, stripHTMLText(link[4]))
		fmt.Fprintf(&sb, "- gid/token: `%s` / `%s`\n", gid, token)
		fmt.Fprintf(&sb, "- 链接: %s\n", galleryURL)

		meta := make([]string, 0, 4)
		if m := ehSearchCatPattern.FindStringSubmatch(content); m != nil {
			if cat := stripHTMLText(m[1]); cat != "" {
				meta = append(meta, cat)
			}
		}
		if m := ehSearchPagesPattern.FindStringSubmatch(content); m != nil {
			meta = append(meta, strings.ReplaceAll(m[1], ",", "")+"p")
		}
		if m := ehSearchRatingPattern.FindStringSubmatch(content); m != nil {
			x, _ := strconv.Atoi(m[1])
			y, _ := strconv.Atoi(m[2])
			meta = append(meta, "★"+formatEHRating(ehRatingFromSprite(x, y)))
		}
		if len(meta) > 0 {
			fmt.Fprintf(&sb, "- 分类/页数/评分: %s\n", strings.Join(meta, " · "))
		}

		meta = meta[:0]
		if m := ehSearchPostedPattern.FindStringSubmatch(content); m != nil {
			if posted := stripHTMLText(m[1]); posted != "" {
				meta = append(meta, posted)
			}
		}
		if m := ehSearchUploaderPattern.FindStringSubmatch(content); m != nil {
			if uploader := stripHTMLText(m[1]); uploader != "" {
				meta = append(meta, uploader)
			}
		}
		if len(meta) > 0 {
			fmt.Fprintf(&sb, "- 发布/上传者: %s\n", strings.Join(meta, " · "))
		}

		if m := ehSearchThumbPattern.FindStringSubmatch(content); m != nil {
			fmt.Fprintf(&sb, "- 封面: %s\n", m[1])
		}

		tags := make([]string, 0, 12)
		for _, t := range ehSearchTagPattern.FindAllStringSubmatch(content, -1) {
			tag := stripHTMLText(t[1])
			if tag == "" {
				continue
			}
			// 命中屏蔽规则的标签不返回给 agent。
			if blocker.blockedSearchValue(tag) {
				continue
			}
			tags = append(tags, tag)
		}
		if len(tags) > 0 {
			fmt.Fprintf(&sb, "- 标签: %s\n", strings.Join(tags, ", "))
		}
		sb.WriteString("\n")
	}

	if count == 0 {
		sb.WriteString("> 未解析到结果条目，页面结构可能已变化或需要登录\n")
		return sb.String()
	}

	sb.WriteString("---\n\n")
	if m := ehSearchNextPattern.FindStringSubmatch(body); m != nil {
		fmt.Fprintf(&sb, "- 下一页: %s\n", html.UnescapeString(m[1]))
	} else {
		sb.WriteString("- 下一页: 无\n")
	}
	if m := ehSearchPrevPattern.FindStringSubmatch(body); m != nil {
		fmt.Fprintf(&sb, "- 上一页: %s\n", html.UnescapeString(m[1]))
	}

	return sb.String()
}

func renderEHGalleryMarkdown(body string, blocker *ehTagBlocker) string {
	var sb strings.Builder
	title := stripHTMLText(firstSubmatch(ehGalleryTitlePattern, body))
	if title == "" {
		title = "EH 作品详情"
	}
	fmt.Fprintf(&sb, "# %s\n\n", title)
	if jp := stripHTMLText(firstSubmatch(ehGalleryJapanesePattern, body)); jp != "" {
		fmt.Fprintf(&sb, "> %s\n\n", jp)
	}

	if m := ehGalleryCatPattern.FindStringSubmatch(body); m != nil {
		if cat := stripHTMLText(m[1]); cat != "" {
			fmt.Fprintf(&sb, "- 分类: %s\n", cat)
		}
	}
	if m := ehGalleryUploaderPattern.FindStringSubmatch(body); m != nil {
		if uploader := stripHTMLText(m[1]); uploader != "" {
			fmt.Fprintf(&sb, "- 上传者: %s\n", uploader)
		}
	}
	for _, m := range ehGalleryMetaPattern.FindAllStringSubmatch(body, -1) {
		key := strings.TrimSuffix(stripHTMLText(m[1]), ":")
		value := stripHTMLText(m[2])
		if key == "" || value == "" {
			continue
		}
		fmt.Fprintf(&sb, "- %s: %s\n", key, value)
	}
	if m := ehGalleryAvgPattern.FindStringSubmatch(body); m != nil {
		line := fmt.Sprintf("- 平均评分: %s/5", m[1])
		if c := stripHTMLText(firstSubmatch(ehGalleryRatingCntPattern, body)); c != "" {
			line += "（" + c + " 人评分）"
		}
		sb.WriteString(line + "\n")
	}
	if c := stripHTMLText(firstSubmatch(ehGalleryFavCountPattern, body)); c != "" {
		// gdd 里已有 Favorited 行时不再重复输出。
		if !strings.Contains(sb.String(), "Favorited:") {
			fmt.Fprintf(&sb, "- 收藏: %s\n", c)
		}
	}

	sb.WriteString("\n## 标签\n\n")
	// 按命名空间分组并保持首次出现顺序（对应 JHenTai 的 LinkedHashMap）。
	namespaces := make([]string, 0, 8)
	grouped := make(map[string][]string)
	for _, m := range ehGalleryTagPattern.FindAllStringSubmatch(body, -1) {
		pair := m[1]
		namespace, key, found := strings.Cut(pair, ":")
		if !found {
			namespace, key = "temp", pair
		}
		// 命中屏蔽规则的标签不返回给 agent。
		if blocker.blocked(namespace, key) {
			continue
		}
		key = strings.ReplaceAll(key, "_", " ")
		// class: gt=正常, gtl=低置信, gtw=被标记错误。gtl 在翻译本里极常见，标注会变成噪音，只标 gtw。
		if m[2] == "gtw" {
			key += " (错误)"
		}
		if _, ok := grouped[namespace]; !ok {
			namespaces = append(namespaces, namespace)
		}
		grouped[namespace] = append(grouped[namespace], key)
	}
	for _, namespace := range namespaces {
		fmt.Fprintf(&sb, "- **%s**: %s\n", namespace, strings.Join(grouped[namespace], ", "))
	}
	if len(namespaces) == 0 {
		sb.WriteString("- 未解析到标签\n")
	}

	thumbs := ehGalleryThumbNewPattern.FindAllStringSubmatch(body, -1)
	if len(thumbs) == 0 {
		// 旧结构回退：直接取 /s/{hash}/{gid}-{page} 入口。
		thumbs = ehGalleryThumbPattern.FindAllStringSubmatch(body, -1)
		fmt.Fprintf(&sb, "\n## 缩略图入口（%d 个）\n\n", len(thumbs))
		for i, t := range thumbs {
			if i >= ehMarkdownMaxThumbs {
				fmt.Fprintf(&sb, "- … 其余 %d 个省略\n", len(thumbs)-ehMarkdownMaxThumbs)
				break
			}
			fmt.Fprintf(&sb, "- p%s: %s\n", t[4], html.UnescapeString(t[1]))
		}
	} else {
		// 新结构下多页共用一张雪碧图，逐页输出 url 会产生大量重复，只保留图片页入口。
		unique := make(map[string]struct{}, len(thumbs))
		lines := make([]string, 0, len(thumbs))
		for _, t := range thumbs {
			href := html.UnescapeString(t[1])
			if _, ok := unique[href]; ok {
				continue
			}
			unique[href] = struct{}{}
			pageNo := ""
			if m := ehThumbPageNoPattern.FindStringSubmatch(href); m != nil {
				pageNo = m[1]
			}
			if pageNo != "" {
				lines = append(lines, fmt.Sprintf("- p%s: %s", pageNo, href))
			} else {
				lines = append(lines, "- "+href)
			}
		}
		fmt.Fprintf(&sb, "\n## 缩略图入口（%d 个）\n\n", len(lines))
		for i, line := range lines {
			if i >= ehMarkdownMaxThumbs {
				fmt.Fprintf(&sb, "- … 其余 %d 个省略\n", len(lines)-ehMarkdownMaxThumbs)
				break
			}
			sb.WriteString(line + "\n")
		}
	}
	if len(thumbs) == 0 {
		sb.WriteString("- 未解析到缩略图入口\n")
	}

	sb.WriteString(renderEHCommentsMarkdown(body))

	return sb.String()
}

// renderEHCommentsMarkdown 解析 #cdiv 下的评论列表，输出作者、评分、时间与正文。
func renderEHCommentsMarkdown(body string) string {
	// 只在 #cdiv 范围内找评论，避免命中页面其它 c1 元素。
	start := strings.Index(body, `id="cdiv"`)
	if start < 0 {
		return ""
	}
	section := body[start:]
	if end := strings.Index(section, `id="csp"`); end > 0 {
		section = section[:end]
	}

	// 按 <div class="c1"> 起始位置切分，每段到下一个起始位置为止。
	starts := ehGalleryCommentStartPattern.FindAllStringIndex(section, -1)
	if len(starts) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(starts))
	for i, loc := range starts {
		end := len(section)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		blocks = append(blocks, section[loc[0]:end])
	}

	var sb strings.Builder
	sb.WriteString("\n## 评论\n\n")

	shown := 0
	for _, content := range blocks {
		bodyMatch := ehCommentBodyPattern.FindStringSubmatch(content)
		if bodyMatch == nil {
			continue
		}
		text := stripHTMLText(bodyMatch[1])
		if text == "" {
			continue
		}
		if shown >= ehMarkdownMaxComments {
			fmt.Fprintf(&sb, "- … 其余 %d 条评论省略\n", len(blocks)-shown)
			break
		}
		shown++

		author := ""
		if m := ehCommentAuthorPattern.FindStringSubmatch(content); m != nil {
			raw := stripHTMLText(m[1])
			if link := ehCommentAuthorLinkPattern.FindStringSubmatch(m[1]); link != nil {
				author = stripHTMLText(link[1])
			} else if raw != "" {
				author = raw
			}
		}
		if author == "" {
			author = "匿名"
		}
		if ehCommentUploaderPattern.MatchString(content) {
			author += "（上传者）"
		}

		meta := make([]string, 0, 3)
		if m := ehCommentScorePattern.FindStringSubmatch(content); m != nil {
			if score := stripHTMLText(m[1]); score != "" {
				meta = append(meta, "评分 "+score)
			}
		}
		if m := ehCommentAuthorPattern.FindStringSubmatch(content); m != nil {
			if posted := parseEHCommentTime(stripHTMLText(m[1])); posted != "" {
				meta = append(meta, posted)
			}
		}
		if m := ehCommentEditedPattern.FindStringSubmatch(content); m != nil {
			if edited := stripHTMLText(m[1]); edited != "" {
				meta = append(meta, "编辑于 "+edited)
			}
		}

		fmt.Fprintf(&sb, "### %s\n\n", author)
		if len(meta) > 0 {
			fmt.Fprintf(&sb, "%s\n\n", strings.Join(meta, " · "))
		}
		if len([]rune(text)) > ehMarkdownMaxCommentChars {
			text = string([]rune(text)[:ehMarkdownMaxCommentChars]) + "…"
		}
		fmt.Fprintf(&sb, "%s\n\n", text)
	}

	if shown == 0 {
		return ""
	}

	return sb.String()
}

// parseEHCommentTime 从 "Posted on 12 November 2026, 06:22 by: xxx" 中提取时间部分。
func parseEHCommentTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, "Posted on "); idx >= 0 {
		raw = raw[idx+len("Posted on "):]
	}
	if idx := strings.Index(raw, " by:"); idx >= 0 {
		raw = raw[:idx]
	}

	return strings.TrimSpace(raw)
}
