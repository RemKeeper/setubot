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
	return renderJSON(map[string]interface{}{"tags": results})
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
