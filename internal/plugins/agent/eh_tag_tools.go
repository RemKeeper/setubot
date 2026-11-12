package agent

import (
	"context"
	"fmt"
	"time"
)

func (p *plugin) callEHTagLoad(args map[string]interface{}) (string, error) {
	if p.ehTags == nil {
		return "", fmt.Errorf("EH 标签工具未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.cfg.Timeout)*time.Second)
	defer cancel()
	if err := p.ehTags.Load(ctx, boolArg(args, "force_refresh", false)); err != nil {
		return "", err
	}
	return renderJSON(p.ehTags.Status())
}

func (p *plugin) callEHTagSearch(args map[string]interface{}) (string, error) {
	if p.ehTags == nil {
		return "", fmt.Errorf("EH 标签工具未初始化")
	}
	matches, err := p.ehTags.Search(
		stringArg(args, "query"),
		stringArg(args, "namespace"),
		numberArg(args, "limit", 10),
		boolArg(args, "include_intro", false),
	)
	if err != nil {
		return "", err
	}
	// 命中屏蔽规则的标签不返回给 agent。
	matches = p.filterEHTagMatches(matches)
	return renderJSON(map[string]interface{}{
		"query":   stringArg(args, "query"),
		"matches": matches,
	})
}

func (p *plugin) callEHTagResolveKeyword(args map[string]interface{}) (string, error) {
	if p.ehTags == nil {
		return "", fmt.Errorf("EH 标签工具未初始化")
	}
	autoSelect := true
	if args != nil && args["auto_select"] != nil {
		autoSelect = boolArg(args, "auto_select", true)
	}
	result, err := p.ehTags.ResolveKeyword(stringArg(args, "keyword"), autoSelect, numberArg(args, "limit", 10))
	if err != nil {
		return "", err
	}
	p.filterEHTagResolveResult(result)
	return renderJSON(result)
}

func (p *plugin) callEHTagTranslate(args map[string]interface{}) (string, error) {
	if p.ehTags == nil {
		return "", fmt.Errorf("EH 标签工具未初始化")
	}
	tags := tagObjectSliceArg(args, "tags")
	results, err := p.ehTags.Translate(tags)
	if err != nil {
		return "", err
	}
	// 命中屏蔽规则的标签不返回给 agent。
	results = p.filterEHTagTranslations(results)
	return renderJSON(map[string]interface{}{"tags": results})
}

// filterEHTagMatches 剔除命中屏蔽规则的标签候选。
func (p *plugin) filterEHTagMatches(matches []ehTagMatch) []ehTagMatch {
	if p.ehTagBlocker.empty() || len(matches) == 0 {
		return matches
	}
	kept := make([]ehTagMatch, 0, len(matches))
	for _, match := range matches {
		if p.ehTagBlocker.blocked(match.Namespace, match.Key) {
			continue
		}
		kept = append(kept, match)
	}

	return kept
}

// filterEHTagResolveResult 就地剔除 resolve 结果中的屏蔽标签，并同步修正 resolvedKeyword。
func (p *plugin) filterEHTagResolveResult(result map[string]interface{}) {
	if p.ehTagBlocker.empty() || result == nil {
		return
	}
	if candidates, ok := result["candidates"].([]ehTagMatch); ok {
		result["candidates"] = p.filterEHTagMatches(candidates)
	}
	if selected, ok := result["selected"].(ehTagMatch); ok {
		if p.ehTagBlocker.blocked(selected.Namespace, selected.Key) {
			delete(result, "selected")
			// 选中的标签被屏蔽时不能把它的搜索表达式交给 agent。
			if resolved, _ := result["resolvedKeyword"].(string); p.ehTagBlocker.blockedSearchValue(resolved) {
				if original, _ := result["originalKeyword"].(string); original != "" {
					result["resolvedKeyword"] = original
				}
				result["source"] = "rawKeyword"
				result["warnings"] = appendStringWarning(result["warnings"], "解析结果命中屏蔽标签，已回退为原始关键词")
			}
		}
	}
}

// filterEHTagTranslations 剔除命中屏蔽规则的翻译结果。
func (p *plugin) filterEHTagTranslations(results []map[string]interface{}) []map[string]interface{} {
	if p.ehTagBlocker.empty() || len(results) == 0 {
		return results
	}
	kept := make([]map[string]interface{}, 0, len(results))
	for _, item := range results {
		namespace, _ := item["namespace"].(string)
		key, _ := item["key"].(string)
		if p.ehTagBlocker.blocked(namespace, key) {
			continue
		}
		kept = append(kept, item)
	}

	return kept
}

func appendStringWarning(existing interface{}, warning string) []string {
	warnings, _ := existing.([]string)
	return append(warnings, warning)
}

func tagObjectSliceArg(args map[string]interface{}, key string) []map[string]string {
	if args == nil || args[key] == nil {
		return nil
	}
	values, ok := args[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]string, 0, len(values))
	for _, value := range values {
		item, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		result = append(result, map[string]string{
			"namespace": fmt.Sprint(item["namespace"]),
			"key":       fmt.Sprint(item["key"]),
		})
	}
	return result
}
