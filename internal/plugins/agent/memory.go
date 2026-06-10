package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blevesearch/bleve/v2"
)

const memoryIndexDirName = ".index"

type MemoryStore struct {
	dir       string
	indexPath string
	index     bleve.Index
	mu        sync.Mutex
}

type memoryDocument struct {
	Key     string `json:"key"`
	Content string `json:"content"`
}

type memorySearchHit struct {
	key     string
	content string
	score   float64
}

func NewMemoryStore(dir string) (*MemoryStore, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	store := &MemoryStore{dir: dir, indexPath: filepath.Join(dir, memoryIndexDirName)}
	index, err := bleve.Open(store.indexPath)
	if err != nil {
		mapping := bleve.NewIndexMapping()
		mapping.DefaultAnalyzer = "standard"
		index, err = bleve.New(store.indexPath, mapping)
	}
	if err != nil {
		return nil, err
	}
	store.index = index

	return store, nil
}

func (s *MemoryStore) Rebuild() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	names, err := listFileNames(s.dir)
	if err != nil {
		return err
	}
	batch := s.index.NewBatch()
	for _, name := range names {
		content, err := readTextFileInDir(s.dir, name)
		if err != nil {
			continue
		}
		batch.Index(name, memoryDocument{Key: name, Content: content})
	}

	return s.index.Batch(batch)
}

func (s *MemoryStore) Index(key string, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.index.Index(key, memoryDocument{Key: key, Content: content})
}

func (s *MemoryStore) Search(query string, limit int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("记忆检索词不能为空")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	hits := make(map[string]memorySearchHit)
	if err := s.searchBleve(query, limit, hits); err != nil {
		return "", err
	}
	if err := s.searchSubstring(query, hits); err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "", nil
	}

	ordered := make([]memorySearchHit, 0, len(hits))
	for _, hit := range hits {
		ordered = append(ordered, hit)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score == ordered[j].score {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].score > ordered[j].score
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}

	parts := make([]string, 0, len(ordered))
	for _, hit := range ordered {
		parts = append(parts, fmt.Sprintf("[%s]\n%s", hit.key, strings.TrimSpace(hit.content)))
	}

	return strings.Join(parts, "\n\n"), nil
}

func (s *MemoryStore) searchBleve(query string, limit int, hits map[string]memorySearchHit) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	searchQuery := bleve.NewQueryStringQuery(query)
	request := bleve.NewSearchRequestOptions(searchQuery, limit, 0, false)
	request.Fields = []string{"key", "content"}
	result, err := s.index.Search(request)
	if err != nil {
		return err
	}
	for _, hit := range result.Hits {
		content := strings.TrimSpace(fieldString(hit.Fields["content"]))
		if content == "" {
			content, _ = readTextFileInDir(s.dir, hit.ID)
		}
		hits[hit.ID] = memorySearchHit{key: hit.ID, content: content, score: hit.Score + 10}
	}

	return nil
}

func (s *MemoryStore) searchSubstring(query string, hits map[string]memorySearchHit) error {
	names, err := listFileNames(s.dir)
	if err != nil {
		return err
	}
	terms := memorySearchTerms(query)
	for _, name := range names {
		content, err := readTextFileInDir(s.dir, name)
		if err != nil {
			continue
		}
		score := substringMemoryScore(name, content, terms)
		if score <= 0 {
			continue
		}
		if existing, ok := hits[name]; ok && existing.score >= score {
			continue
		}
		hits[name] = memorySearchHit{key: name, content: content, score: score}
	}

	return nil
}

func fieldString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, "\n")
	case json.RawMessage:
		return string(v)
	case []byte:
		return string(v)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func memorySearchTerms(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == '，' || r == '.' || r == '。' || r == ':' || r == '：' || r == ';' || r == '；' || r == '?' || r == '？' || r == '!' || r == '！'
	})
	if utf8.RuneCountInString(query) >= 2 {
		terms = append(terms, query)
	}
	return dedupeStrings(terms)
}

func substringMemoryScore(key string, content string, terms []string) float64 {
	if len(terms) == 0 {
		return 0
	}
	haystack := strings.ToLower(key + "\n" + content)
	score := 0.0
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		count := bytes.Count([]byte(haystack), []byte(term))
		if count > 0 {
			score += float64(count) * float64(utf8.RuneCountInString(term))
		}
	}

	return score
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

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
	if err := os.WriteFile(filepath.Join(p.cfg.MemoryDir, key), []byte(text), 0644); err != nil {
		return err
	}
	if p.memory != nil {
		return p.memory.Index(key, text)
	}

	return nil
}

func (p *plugin) SearchMemory(query string, limit int) (string, error) {
	if p.memory != nil {
		return p.memory.Search(query, limit)
	}

	return p.searchMemoryFiles(query, limit)
}

func (p *plugin) searchMemoryFiles(query string, limit int) (string, error) {
	store := &MemoryStore{dir: p.cfg.MemoryDir}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("记忆检索词不能为空")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	hits := make(map[string]memorySearchHit)
	if err := store.searchSubstring(query, hits); err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "", nil
	}

	ordered := make([]memorySearchHit, 0, len(hits))
	for _, hit := range hits {
		ordered = append(ordered, hit)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score == ordered[j].score {
			return ordered[i].key < ordered[j].key
		}
		return ordered[i].score > ordered[j].score
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}

	parts := make([]string, 0, len(ordered))
	for _, hit := range ordered {
		parts = append(parts, fmt.Sprintf("[%s]\n%s", hit.key, strings.TrimSpace(hit.content)))
	}

	return strings.Join(parts, "\n\n"), nil
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
