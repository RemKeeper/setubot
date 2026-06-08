package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (p *plugin) readSkillFile(name string) (string, error) {
	return readTextFileInDir(p.cfg.SkillDir, name)
}

func (p *plugin) listSkillNames() ([]string, error) {
	return listFileNames(p.cfg.SkillDir)
}

func (p *plugin) writeMemoryFile(key string, content string) error {
	key = safeName(key)
	content = strings.TrimSpace(content)
	if key == "" {
		return fmt.Errorf("记忆键不能为空")
	}
	if content == "" {
		return fmt.Errorf("记忆内容不能为空")
	}
	if err := ensureDir(p.cfg.MemoryDir); err != nil {
		return err
	}
	if filepath.Ext(key) == "" {
		key += ".md"
	}

	text := fmt.Sprintf("# %s\n\n%s\n\n更新时间：%s\n", strings.TrimSuffix(key, filepath.Ext(key)), content, time.Now().Format(time.RFC3339))
	return os.WriteFile(filepath.Join(p.cfg.MemoryDir, key), []byte(text), 0644)
}

func (p *plugin) readMemoryFile(key string) (string, error) {
	key = safeName(key)
	if filepath.Ext(key) == "" {
		key += ".md"
	}
	return readTextFileInDir(p.cfg.MemoryDir, key)
}

func (p *plugin) listMemoryNames() ([]string, error) {
	return listFileNames(p.cfg.MemoryDir)
}

func (p *plugin) readAllMemories() (string, error) {
	names, err := p.listMemoryNames()
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		content, err := p.readMemoryFile(name)
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s]\n%s", name, strings.TrimSpace(content)))
	}
	return strings.Join(parts, "\n\n"), nil
}
