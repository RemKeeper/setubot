package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type exaSearchResponse struct {
	RequestID      string            `json:"requestId"`
	SearchType     string            `json:"searchType"`
	Results        []exaSearchResult `json:"results"`
	CostDollars    exaCostDollars    `json:"costDollars"`
	ResolvedLegacy string            `json:"resolvedSearchType"`
}

type exaCostDollars struct {
	Total float64 `json:"total"`
}

type exaSearchResult struct {
	Title           string    `json:"title"`
	URL             string    `json:"url"`
	PublishedDate   string    `json:"publishedDate"`
	Author          string    `json:"author"`
	Highlights      []string  `json:"highlights"`
	HighlightScores []float64 `json:"highlightScores"`
	Summary         string    `json:"summary"`
}

func (p *plugin) callExaSearch(args map[string]interface{}) (string, error) {
	if !p.cfg.Exa.Enabled {
		return "", fmt.Errorf("Exa 搜索未启用")
	}
	apiKey := strings.TrimSpace(p.cfg.Exa.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("EXA_API_KEY"))
	}
	if apiKey == "" {
		return "", fmt.Errorf("Exa API Key 未配置，请在 agent.exa.apiKey 或环境变量 EXA_API_KEY 中设置")
	}

	query := stringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	searchType := stringArg(args, "type")
	if searchType == "" {
		searchType = p.cfg.Exa.DefaultType
	}
	numResults := clamp(numberArg(args, "num_results", p.cfg.Exa.DefaultNumResults), 1, 10)

	contents := map[string]interface{}{"highlights": true}
	if boolArg(args, "live", false) {
		contents["maxAgeHours"] = 0
	}
	payload := map[string]interface{}{
		"query":      query,
		"type":       searchType,
		"numResults": numResults,
		"contents":   contents,
	}
	if category := stringArg(args, "category"); category != "" {
		payload["category"] = category
	}
	if domains := stringSliceArg(args, "include_domains"); len(domains) > 0 {
		payload["includeDomains"] = domains
	}
	if domains := stringSliceArg(args, "exclude_domains"); len(domains) > 0 {
		payload["excludeDomains"] = domains
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	baseURL := strings.TrimRight(p.cfg.Exa.BaseURL, "/")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Exa 搜索返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var searchResp exaSearchResponse
	if err := json.Unmarshal(respBody, &searchResp); err != nil {
		return "", err
	}
	return renderExaSearchResults(query, searchResp), nil
}

func renderExaSearchResults(query string, resp exaSearchResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Exa 搜索结果：%s\n", query)
	if resp.SearchType != "" {
		fmt.Fprintf(&b, "搜索类型：%s\n", resp.SearchType)
	}
	if len(resp.Results) == 0 {
		b.WriteString("未找到结果")
		return b.String()
	}
	for i, result := range resp.Results {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, fallbackText(result.Title, "无标题"))
		fmt.Fprintf(&b, "URL: %s\n", result.URL)
		if result.PublishedDate != "" || result.Author != "" {
			fmt.Fprintf(&b, "来源信息: %s %s\n", strings.TrimSpace(result.PublishedDate), strings.TrimSpace(result.Author))
		}
		if len(result.Highlights) > 0 {
			b.WriteString("Highlights:\n")
			for _, highlight := range result.Highlights {
				highlight = strings.TrimSpace(highlight)
				if highlight != "" {
					fmt.Fprintf(&b, "- %s\n", truncate(highlight, 700))
				}
			}
		} else if strings.TrimSpace(result.Summary) != "" {
			fmt.Fprintf(&b, "Summary: %s\n", truncate(result.Summary, 900))
		}
	}
	if resp.CostDollars.Total > 0 {
		fmt.Fprintf(&b, "\n估算成本：$%.6f", resp.CostDollars.Total)
	}
	return b.String()
}

func fallbackText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func stringSliceArg(args map[string]interface{}, key string) []string {
	if args == nil || args[key] == nil {
		return nil
	}
	values, ok := args[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}
