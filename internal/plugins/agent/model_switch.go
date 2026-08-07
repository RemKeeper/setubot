package agent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"setubot/internal/config"

	zero "github.com/wdvxdr1123/ZeroBot"
)

const (
	modelSelectionTTL = 2 * time.Minute
	modelDisplayLimit = 30
)

type pendingModelSelection struct {
	models    []string
	expiresAt time.Time
}

func (p *plugin) switchModelCommand(ctx *zero.Ctx, command string) {
	if !p.isSuperUser(ctx.Event.UserID) {
		ctx.Send("仅管理员可以切换模型")
		return
	}
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		ctx.Send("Agent 接口 API Key 未配置")
		return
	}

	ctx.Send("正在获取可用模型列表...")
	models, err := p.fetchModels()
	if err != nil {
		ctx.Send(fmt.Sprintf("获取模型列表失败：%v", err))
		return
	}

	matches := modelSwitchPattern.FindStringSubmatch(command)
	query := ""
	if len(matches) > 1 {
		query = strings.TrimSpace(matches[1])
	}
	if query != "" {
		models = matchModels(models, query)
		if len(models) == 0 {
			ctx.Send(fmt.Sprintf("未找到匹配 %q 的模型，请换个关键词", query))
			return
		}
		if len(models) == 1 {
			p.applyModel(ctx, models[0])
			return
		}
	}

	promptModels := models
	if len(promptModels) > modelDisplayLimit {
		promptModels = promptModels[:modelDisplayLimit]
	}
	p.setPendingModelSelection(p.sessionKey(ctx), promptModels)
	ctx.Send(formatModelChoices(promptModels, len(models), p.currentModel()))
}

func (p *plugin) handlePendingModelSelection(ctx *zero.Ctx, command string) bool {
	if modelSwitchPattern.MatchString(command) {
		return false
	}

	selection, ok := p.pendingModelSelection(p.sessionKey(ctx))
	if !ok {
		return false
	}
	if !p.isSuperUser(ctx.Event.UserID) {
		p.clearPendingModelSelection(p.sessionKey(ctx))
		return false
	}

	command = strings.TrimSpace(command)
	if command == "取消" || strings.EqualFold(command, "cancel") {
		p.clearPendingModelSelection(p.sessionKey(ctx))
		ctx.Send("已取消模型切换")
		return true
	}

	if number, err := strconv.Atoi(command); err == nil {
		if number < 1 || number > len(selection.models) {
			ctx.Send(fmt.Sprintf("请输入 1-%d 的编号，或回复模型名关键词；回复“取消”退出", len(selection.models)))
			return true
		}
		p.clearPendingModelSelection(p.sessionKey(ctx))
		p.applyModel(ctx, selection.models[number-1])
		return true
	}

	matches := matchModels(selection.models, command)
	switch len(matches) {
	case 0:
		ctx.Send("当前列表中没有匹配模型，请回复编号、其他关键词或“取消”")
	case 1:
		p.clearPendingModelSelection(p.sessionKey(ctx))
		p.applyModel(ctx, matches[0])
	default:
		p.setPendingModelSelection(p.sessionKey(ctx), matches)
		ctx.Send(formatModelChoices(matches, len(matches), p.currentModel()))
	}
	return true
}

func (p *plugin) fetchModels() ([]string, error) {
	timeout := time.Duration(p.cfg.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	list, err := p.aiClient.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(list.Models))
	seen := make(map[string]struct{}, len(list.Models))
	for _, model := range list.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("接口未返回可用模型")
	}
	sort.Slice(models, func(i, j int) bool {
		return strings.ToLower(models[i]) < strings.ToLower(models[j])
	})
	return models, nil
}

func matchModels(models []string, query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return append([]string(nil), models...)
	}

	exact := make([]string, 0, 1)
	prefix := make([]string, 0)
	contains := make([]string, 0)
	fuzzy := make([]string, 0)
	for _, model := range models {
		candidate := strings.ToLower(model)
		switch {
		case candidate == query:
			exact = append(exact, model)
		case strings.HasPrefix(candidate, query):
			prefix = append(prefix, model)
		case strings.Contains(candidate, query):
			contains = append(contains, model)
		case isSubsequence(query, candidate):
			fuzzy = append(fuzzy, model)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if len(prefix) > 0 {
		return prefix
	}
	if len(contains) > 0 {
		return contains
	}
	return fuzzy
}

func isSubsequence(query string, candidate string) bool {
	queryRunes := []rune(query)
	if len(queryRunes) == 0 {
		return true
	}
	index := 0
	for _, char := range candidate {
		if char == queryRunes[index] {
			index++
			if index == len(queryRunes) {
				return true
			}
		}
	}
	return false
}

func formatModelChoices(models []string, total int, current string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "当前模型：%s\n可用模型（显示 %d/%d）：", current, len(models), total)
	for index, model := range models {
		fmt.Fprintf(&builder, "\n%d. %s", index+1, model)
	}
	builder.WriteString("\n请在 2 分钟内回复编号或模型名关键词；回复“取消”退出")
	if total > len(models) {
		builder.WriteString("\n模型较多，可重新发送“切换模型 <关键词>”缩小范围")
	}
	return builder.String()
}

func (p *plugin) applyModel(ctx *zero.Ctx, model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		ctx.Send("模型名不能为空")
		return
	}
	if err := config.UpdateAgentModel(p.configPath, model); err != nil {
		ctx.Send(fmt.Sprintf("保存模型配置失败：%v", err))
		return
	}

	p.runtimeM.Lock()
	p.cfg.Model = model
	p.runtimeM.Unlock()
	p.clearSession(p.sessionKey(ctx))
	ctx.Send(fmt.Sprintf("已切换模型为 %s，并保存到配置文件；当前会话上下文已重置", model))
}

func (p *plugin) currentModel() string {
	p.runtimeM.RLock()
	defer p.runtimeM.RUnlock()
	return p.cfg.Model
}

func (p *plugin) setPendingModelSelection(key string, models []string) {
	p.modelM.Lock()
	defer p.modelM.Unlock()
	p.modelPicks[key] = pendingModelSelection{
		models:    append([]string(nil), models...),
		expiresAt: time.Now().Add(modelSelectionTTL),
	}
}

func (p *plugin) pendingModelSelection(key string) (pendingModelSelection, bool) {
	p.modelM.Lock()
	defer p.modelM.Unlock()
	selection, ok := p.modelPicks[key]
	if !ok {
		return pendingModelSelection{}, false
	}
	if time.Now().After(selection.expiresAt) {
		delete(p.modelPicks, key)
		return pendingModelSelection{}, false
	}
	return selection, true
}

func (p *plugin) clearPendingModelSelection(key string) {
	p.modelM.Lock()
	defer p.modelM.Unlock()
	delete(p.modelPicks, key)
}
