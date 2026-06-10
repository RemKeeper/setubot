package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

type ehTagStore struct {
	cfg     ehTagRuntimeConfig
	client  *http.Client
	index   *ehTagIndex
	mu      sync.RWMutex
	loadErr error
}

type ehTagRuntimeConfig struct {
	Enabled   bool
	SourceURL string
	CachePath string
}

type ehTagRoot struct {
	Repo string `json:"repo"`
	Head struct {
		SHA       string `json:"sha"`
		Committer struct {
			When string `json:"when"`
		} `json:"committer"`
	} `json:"head"`
	Version int                `json:"version"`
	Data    []ehTagNamespaceDB `json:"data"`
}

type ehTagNamespaceDB struct {
	Namespace string                `json:"namespace"`
	Count     int                   `json:"count"`
	Data      map[string]ehRawTagDB `json:"data"`
}

type ehRawTagDB struct {
	Name  string `json:"name"`
	Intro string `json:"intro"`
	Links string `json:"links"`
}

type ehTagIndex struct {
	Repo              string
	SourceSHA         string
	Timestamp         string
	Version           int
	Records           []ehTagRecord
	ByNamespaceAndKey map[string]ehTagRecord
	ByKey             map[string][]ehTagRecord
	ByNamespace       map[string][]ehTagRecord
	SearchRows        []ehTagSearchRow
}

type ehTagRecord struct {
	Namespace        string `json:"namespace"`
	Key              string `json:"key"`
	NamespaceZh      string `json:"namespaceZh,omitempty"`
	TagName          string `json:"tagName,omitempty"`
	FullTagNameHTML  string `json:"fullTagNameHtml,omitempty"`
	IntroText        string `json:"introText,omitempty"`
	IntroHTML        string `json:"introHtml,omitempty"`
	LinksHTML        string `json:"linksHtml,omitempty"`
	NamespaceWithKey string `json:"namespaceWithKey"`
}

type ehTagSearchRow struct {
	Record           ehTagRecord
	KeyLower         string
	TagNameLower     string
	NamespaceLower   string
	NamespaceZhLower string
	IntroLower       string
}

type ehNamespaceMeta struct {
	Zh      string
	Aliases []string
	Weight  float64
}

type ehTagMatch struct {
	Namespace   string           `json:"namespace"`
	Key         string           `json:"key"`
	NamespaceZh string           `json:"namespaceZh,omitempty"`
	TagName     string           `json:"tagName,omitempty"`
	Intro       string           `json:"intro,omitempty"`
	SearchValue string           `json:"searchValue"`
	Score       float64          `json:"score"`
	Ranges      map[string][]int `json:"ranges,omitempty"`
}

var ehNamespaceMap = map[string]ehNamespaceMeta{
	"rows":      {Zh: "内容索引", Weight: 0, Aliases: []string{"内容索引"}},
	"reclass":   {Zh: "重新分类", Weight: 1, Aliases: []string{"重新分类"}},
	"language":  {Zh: "语言", Weight: 2, Aliases: []string{"lang", "语言"}},
	"group":     {Zh: "团队", Weight: 2.2, Aliases: []string{"团队", "社团"}},
	"artist":    {Zh: "艺术家", Weight: 2.5, Aliases: []string{"艺术家", "作者", "画师"}},
	"character": {Zh: "角色", Weight: 2.8, Aliases: []string{"角色"}},
	"parody":    {Zh: "原作", Weight: 3.3, Aliases: []string{"原作", "作品"}},
	"mixed":     {Zh: "混合", Weight: 8, Aliases: []string{"混合"}},
	"male":      {Zh: "男性", Weight: 8.5, Aliases: []string{"男性", "男"}},
	"female":    {Zh: "女性", Weight: 9, Aliases: []string{"女性", "女"}},
	"other":     {Zh: "其他", Weight: 10, Aliases: []string{"其他"}},
}

func newEHTagStore(cfg ehTagRuntimeConfig, client *http.Client) *ehTagStore {
	return &ehTagStore{cfg: cfg, client: client}
}

func (s *ehTagStore) Load(ctx context.Context, forceRefresh bool) error {
	if s == nil || !s.cfg.Enabled {
		return nil
	}
	s.mu.RLock()
	loaded := s.index != nil
	s.mu.RUnlock()
	if loaded && !forceRefresh {
		return nil
	}

	data, source, err := s.loadBytes(ctx, forceRefresh)
	if err != nil {
		s.mu.Lock()
		s.loadErr = err
		s.mu.Unlock()
		return err
	}
	index, err := buildEHTagIndex(data)
	if err != nil {
		s.mu.Lock()
		s.loadErr = err
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.index = index
	s.loadErr = nil
	s.mu.Unlock()
	log.Printf("[agent/eh_tag] 已加载标签索引 source=%s version=%d sha=%s total=%d", source, index.Version, index.SourceSHA, len(index.Records))
	return nil
}

func (s *ehTagStore) loadBytes(ctx context.Context, forceRefresh bool) ([]byte, string, error) {
	cachePath := strings.TrimSpace(s.cfg.CachePath)
	if cachePath != "" && !forceRefresh {
		if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 {
			return data, "cache", nil
		}
	}
	if strings.TrimSpace(s.cfg.SourceURL) == "" {
		return nil, "", fmt.Errorf("EH 标签数据库 sourceURL 未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.SourceURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if cachePath != "" {
			if data, readErr := os.ReadFile(cachePath); readErr == nil && len(data) > 0 {
				return data, "cache_after_remote_error", nil
			}
		}
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("下载 EH 标签数据库返回 %d：%s", resp.StatusCode, truncate(string(body), 500))
	}
	if cachePath != "" {
		if err := ensureDir(filepath.Dir(cachePath)); err != nil {
			return nil, "", err
		}
		if err := os.WriteFile(cachePath, body, 0644); err != nil {
			return nil, "", err
		}
	}
	return body, "remote", nil
}

func buildEHTagIndex(data []byte) (*ehTagIndex, error) {
	var root ehTagRoot
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	idx := &ehTagIndex{
		Repo:              root.Repo,
		SourceSHA:         root.Head.SHA,
		Timestamp:         root.Head.Committer.When,
		Version:           root.Version,
		ByNamespaceAndKey: make(map[string]ehTagRecord),
		ByKey:             make(map[string][]ehTagRecord),
		ByNamespace:       make(map[string][]ehTagRecord),
	}
	for _, block := range root.Data {
		namespace := normalizeEHTagText(block.Namespace)
		if namespace == "" {
			continue
		}
		namespaceZh := resolveEHNamespaceZh(namespace)
		for key, raw := range block.Data {
			key = normalizeEHTagText(key)
			if key == "" {
				continue
			}
			record := ehTagRecord{
				Namespace:        namespace,
				Key:              key,
				NamespaceZh:      namespaceZh,
				TagName:          stripHTMLText(raw.Name),
				FullTagNameHTML:  raw.Name,
				IntroText:        stripHTMLText(raw.Intro),
				IntroHTML:        raw.Intro,
				LinksHTML:        raw.Links,
				NamespaceWithKey: namespace + ":" + key,
			}
			idx.Records = append(idx.Records, record)
			idx.ByNamespaceAndKey[record.NamespaceWithKey] = record
			idx.ByKey[record.Key] = append(idx.ByKey[record.Key], record)
			idx.ByNamespace[record.Namespace] = append(idx.ByNamespace[record.Namespace], record)
			idx.SearchRows = append(idx.SearchRows, makeEHTagSearchRow(record))
		}
	}
	return idx, nil
}

func makeEHTagSearchRow(record ehTagRecord) ehTagSearchRow {
	return ehTagSearchRow{
		Record:           record,
		KeyLower:         strings.ToLower(record.Key),
		TagNameLower:     strings.ToLower(record.TagName),
		NamespaceLower:   strings.ToLower(record.Namespace),
		NamespaceZhLower: strings.ToLower(record.NamespaceZh),
		IntroLower:       strings.ToLower(record.IntroText),
	}
}

func stripHTMLText(value string) string {
	value = htmlTagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	return strings.Join(strings.Fields(value), " ")
}

func normalizeEHTagText(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func resolveEHNamespaceZh(namespace string) string {
	if meta, ok := ehNamespaceMap[namespace]; ok {
		return meta.Zh
	}
	return ""
}

func resolveEHNamespace(input string) string {
	input = normalizeEHTagText(input)
	if input == "" {
		return ""
	}
	if _, ok := ehNamespaceMap[input]; ok {
		return input
	}
	for namespace, meta := range ehNamespaceMap {
		if normalizeEHTagText(meta.Zh) == input {
			return namespace
		}
		for _, alias := range meta.Aliases {
			if normalizeEHTagText(alias) == input {
				return namespace
			}
		}
	}
	return input
}

func ehNamespaceWeight(namespace string) float64 {
	if meta, ok := ehNamespaceMap[namespace]; ok {
		if meta.Weight > 0 {
			return meta.Weight
		}
	}
	return 1
}

func (s *ehTagStore) Search(query string, namespace string, limit int, includeIntro bool) ([]ehTagMatch, error) {
	idx, err := s.indexOrLoad()
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query 不能为空")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	resolvedNamespace := resolveEHNamespace(namespace)
	if strings.Contains(query, ":") && resolvedNamespace == "" {
		parts := strings.SplitN(query, ":", 2)
		resolvedNamespace = resolveEHNamespace(parts[0])
		query = parts[1]
	}
	queryLower := strings.ToLower(strings.Trim(query, " \t\r\n\""))
	if len([]rune(queryLower)) == 1 && isASCIIAlphaNum(queryLower) {
		return nil, nil
	}

	rows := idx.SearchRows
	if resolvedNamespace != "" {
		records := idx.ByNamespace[resolvedNamespace]
		rows = make([]ehTagSearchRow, 0, len(records))
		for _, record := range records {
			rows = append(rows, makeEHTagSearchRow(record))
		}
	}

	matches := make([]ehTagMatch, 0)
	for _, row := range rows {
		match, ok := scoreEHTagRow(row, queryLower, includeIntro)
		if ok {
			matches = append(matches, match)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].SearchValue < matches[j].SearchValue
		}
		return matches[i].Score > matches[j].Score
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func scoreEHTagRow(row ehTagSearchRow, query string, includeIntro bool) (ehTagMatch, bool) {
	record := row.Record
	ranges := make(map[string][]int)
	score := 0.0
	weight := ehNamespaceWeight(record.Namespace)
	if idx := strings.Index(row.KeyLower, query); idx >= 0 {
		score += fieldEHTagScore(weight, query, row.KeyLower, idx, 1.2)
		ranges["key"] = []int{idx, idx + len(query)}
	}
	if idx := strings.Index(row.TagNameLower, query); idx >= 0 {
		score += fieldEHTagScore(weight, query, row.TagNameLower, idx, 1.4)
		ranges["tagName"] = []int{idx, idx + len(query)}
	}
	if idx := strings.Index(row.NamespaceLower, query); idx >= 0 {
		score += fieldEHTagScore(weight, query, row.NamespaceLower, idx, 0.6)
		ranges["namespace"] = []int{idx, idx + len(query)}
	}
	if idx := strings.Index(row.NamespaceZhLower, query); idx >= 0 {
		score += fieldEHTagScore(weight, query, row.NamespaceZhLower, idx, 0.8)
		ranges["namespaceZh"] = []int{idx, idx + len(query)}
	}
	if includeIntro {
		if idx := strings.Index(row.IntroLower, query); idx >= 0 {
			score += fieldEHTagScore(weight, query, row.IntroLower, idx, 0.35)
			ranges["intro"] = []int{idx, idx + len(query)}
		}
	}
	if score <= 0 {
		return ehTagMatch{}, false
	}
	return ehTagMatch{
		Namespace:   record.Namespace,
		Key:         record.Key,
		NamespaceZh: record.NamespaceZh,
		TagName:     record.TagName,
		Intro:       record.IntroText,
		SearchValue: record.NamespaceWithKey,
		Score:       score,
		Ranges:      ranges,
	}, true
}

func fieldEHTagScore(weight float64, query string, field string, index int, multiplier float64) float64 {
	if field == "" {
		return 0
	}
	score := weight * float64(len([]rune(query))+1) / float64(len([]rune(field))+1) * multiplier
	if index == 0 {
		score *= 2
	}
	if field == query {
		score *= 3
	}
	return score
}

func isASCIIAlphaNum(value string) bool {
	if len(value) != 1 {
		return false
	}
	c := value[0]
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func (s *ehTagStore) ResolveKeyword(keyword string, autoSelect bool, limit int) (map[string]interface{}, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("keyword 不能为空")
	}
	if strings.Contains(keyword, ":") {
		return map[string]interface{}{
			"originalKeyword": keyword,
			"resolvedKeyword": keyword,
			"source":          "rawKeyword",
			"candidates":      []ehTagMatch{},
			"warnings":        []string{"输入已经包含命名空间，按原始 EH 搜索表达式返回"},
		}, nil
	}
	matches, err := s.Search(keyword, "", limit, true)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return map[string]interface{}{
			"originalKeyword": keyword,
			"resolvedKeyword": keyword,
			"source":          "rawKeyword",
			"candidates":      matches,
			"warnings":        []string{"未命中 EhTagTranslation 标签库，保留原关键词"},
		}, nil
	}
	best := matches[0]
	source := "ambiguous"
	resolved := keyword
	warnings := []string{}
	if autoSelect && isHighConfidenceEHTag(matches) {
		source = "tagTranslation"
		resolved = best.SearchValue
	} else {
		warnings = append(warnings, "候选可能存在歧义，请结合 tagName/namespace 选择")
	}
	return map[string]interface{}{
		"originalKeyword": keyword,
		"resolvedKeyword": resolved,
		"source":          source,
		"selected":        best,
		"candidates":      matches,
		"warnings":        warnings,
	}, nil
}

func isHighConfidenceEHTag(matches []ehTagMatch) bool {
	if len(matches) == 0 {
		return false
	}
	if len(matches) == 1 {
		return true
	}
	return matches[0].Score >= matches[1].Score*1.25
}

func (s *ehTagStore) Translate(tags []map[string]string) ([]map[string]interface{}, error) {
	idx, err := s.indexOrLoad()
	if err != nil {
		return nil, err
	}
	results := make([]map[string]interface{}, 0, len(tags))
	for _, tag := range tags {
		namespace := resolveEHNamespace(tag["namespace"])
		key := normalizeEHTagText(tag["key"])
		lookup := namespace + ":" + key
		record, ok := idx.ByNamespaceAndKey[lookup]
		item := map[string]interface{}{
			"namespace": namespace,
			"key":       key,
			"found":     ok,
		}
		if ok {
			item["namespaceZh"] = record.NamespaceZh
			item["tagName"] = record.TagName
			item["intro"] = record.IntroText
		}
		results = append(results, item)
	}
	return results, nil
}

func (s *ehTagStore) indexOrLoad() (*ehTagIndex, error) {
	if s == nil || !s.cfg.Enabled {
		return nil, fmt.Errorf("EH 标签工具未启用")
	}
	s.mu.RLock()
	idx := s.index
	loadErr := s.loadErr
	s.mu.RUnlock()
	if idx != nil {
		return idx, nil
	}
	if loadErr != nil {
		return nil, loadErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.Load(ctx, false); err != nil {
		return nil, err
	}
	s.mu.RLock()
	idx = s.index
	s.mu.RUnlock()
	if idx == nil {
		return nil, fmt.Errorf("EH 标签索引未加载")
	}
	return idx, nil
}

func (s *ehTagStore) Status() map[string]interface{} {
	result := map[string]interface{}{
		"enabled": false,
	}
	if s == nil {
		return result
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result["enabled"] = s.cfg.Enabled
	result["sourceURL"] = s.cfg.SourceURL
	result["cachePath"] = s.cfg.CachePath
	if s.index != nil {
		result["loaded"] = true
		result["version"] = s.index.Version
		result["timestamp"] = s.index.Timestamp
		result["sourceSha"] = s.index.SourceSHA
		result["totalTags"] = len(s.index.Records)
	} else {
		result["loaded"] = false
	}
	if s.loadErr != nil {
		result["error"] = s.loadErr.Error()
	}
	return result
}

func renderJSON(value interface{}) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
