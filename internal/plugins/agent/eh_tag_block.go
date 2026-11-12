package agent

import (
	"strings"
)

// ehTagBlocker 负责判断某个标签是否对 agent 不可见。
// 规则来自 agent.ehTag.blockedTags，支持三种写法：
//   - `namespace:key`：精确匹配，例如 `female:lolicon`
//   - `key`：匹配任意命名空间下的同名 key，例如 `lolicon`
//   - `namespace:*`：屏蔽整个命名空间，例如 `female:*`
//
// 匹配不区分大小写，`_` 与空格视为等价（EH 标签 key 常用下划线，展示时用空格）。
type ehTagBlocker struct {
	exact      map[string]struct{}
	keys       map[string]struct{}
	namespaces map[string]struct{}
}

func newEHTagBlocker(patterns []string) *ehTagBlocker {
	blocker := &ehTagBlocker{
		exact:      make(map[string]struct{}),
		keys:       make(map[string]struct{}),
		namespaces: make(map[string]struct{}),
	}
	for _, raw := range patterns {
		pattern := normalizeEHTagKey(raw)
		if pattern == "" {
			continue
		}
		namespace, key, found := strings.Cut(pattern, ":")
		switch {
		case !found:
			// 无命名空间时 namespace 变量承载整个 pattern。
			blocker.keys[namespace] = struct{}{}
		case key == "*":
			blocker.namespaces[namespace] = struct{}{}
		default:
			blocker.exact[namespace+":"+key] = struct{}{}
		}
	}

	return blocker
}

// normalizeEHTagKey 统一大小写并把下划线还原为空格，便于跨来源比较。
func normalizeEHTagKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", " ")
	return strings.Join(strings.Fields(value), " ")
}

func (b *ehTagBlocker) empty() bool {
	return b == nil || (len(b.exact) == 0 && len(b.keys) == 0 && len(b.namespaces) == 0)
}

// blocked 判断 namespace:key 是否命中屏蔽规则。
func (b *ehTagBlocker) blocked(namespace string, key string) bool {
	if b.empty() {
		return false
	}
	namespace = normalizeEHTagKey(namespace)
	key = normalizeEHTagKey(key)
	if key == "" {
		return false
	}
	if _, ok := b.namespaces[namespace]; ok {
		return true
	}
	if _, ok := b.keys[key]; ok {
		return true
	}
	_, ok := b.exact[namespace+":"+key]

	return ok
}

// blockedSearchValue 判断 `namespace:key` 形式的搜索表达式是否命中屏蔽规则。
func (b *ehTagBlocker) blockedSearchValue(value string) bool {
	if b.empty() {
		return false
	}
	namespace, key, found := strings.Cut(normalizeEHTagKey(value), ":")
	if !found {
		return b.blocked("", key)
	}

	return b.blocked(namespace, key)
}

// filterSearchValues 过滤 `namespace:key` 列表，返回保留项与被剔除数量。
func (b *ehTagBlocker) filterSearchValues(values []string) ([]string, int) {
	if b.empty() {
		return values, 0
	}
	kept := make([]string, 0, len(values))
	removed := 0
	for _, value := range values {
		if b.blockedSearchValue(value) {
			removed++
			continue
		}
		kept = append(kept, value)
	}

	return kept, removed
}
