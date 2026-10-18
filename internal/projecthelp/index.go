// Package projecthelp indexes only public documentation of this project.
package projecthelp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
)

type Snapshot struct {
	Version   int              `json:"version"`
	Algorithm string           `json:"algorithm"`
	Hash      string           `json:"corpusHash"`
	Documents int              `json:"documents"`
	Chunks    []docindex.Chunk `json:"chunks"`
}
type Index struct {
	Root, Path string
	mu         sync.Mutex
	snapshot   *Snapshot
}

// Refresh re-reads only README, curated root docs and docs/**/*.md. os.Root
// confines all reads; symlinks and special files are rejected, not indexed.
func (i *Index) Refresh() (*Snapshot, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	root, err := os.OpenRoot(i.Root)
	if err != nil {
		return nil, fmt.Errorf("project documentation unavailable")
	}
	defer root.Close()
	paths := []string{"README.md"}
	for _, name := range []string{"ARCHITECTURE.md", "MEMORY.md", "PERSONALIZATION.md", "TASK_STATE.md", "INVARIANTS.md", "RAG.md", "RAG23.md", "RAG24.md", "RAG25.md"} {
		if _, e := root.Lstat(name); e == nil {
			paths = append(paths, name)
		}
	}
	if st, e := root.Lstat("docs"); e != nil || !st.IsDir() {
		return nil, fmt.Errorf("project docs directory required")
	}
	err = fs.WalkDir(root.FS(), "docs", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in project docs")
		}
		if d.Name() != "docs" && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cannot read project docs")
	}
	if len(paths) > 256 {
		return nil, fmt.Errorf("too many project documents")
	}
	sort.Strings(paths)
	s := &Snapshot{Version: 1, Algorithm: "BM25", Documents: len(paths), Chunks: []docindex.Chunk{}}
	h := sha256.New()
	total := 0
	for _, path := range paths {
		st, e := root.Lstat(path)
		if e != nil || !st.Mode().IsRegular() || st.Size() > 2<<20 {
			return nil, fmt.Errorf("invalid project document: %s", path)
		}
		f, e := root.Open(path)
		if e != nil {
			return nil, fmt.Errorf("cannot open project document")
		}
		data, e := io.ReadAll(io.LimitReader(f, (2<<20)+1))
		f.Close()
		total += len(data)
		if e != nil || len(data) > 2<<20 || total > 16<<20 {
			return nil, fmt.Errorf("project documentation too large")
		}
		h.Write([]byte(path + "\x00"))
		h.Write(data)
		h.Write([]byte{0})
		d, e := docindex.MarkdownDocument(path, string(data))
		if e != nil {
			return nil, e
		}
		chunks, e := docindex.Split(d, docindex.Structured, docindex.ChunkConfig{Size: 1600, Overlap: 180})
		if e != nil {
			return nil, e
		}
		s.Chunks = append(s.Chunks, chunks...)
	}
	s.Hash = fmt.Sprintf("%x", h.Sum(nil))
	if i.snapshot != nil && i.snapshot.Hash == s.Hash {
		return i.snapshot, nil
	}
	if i.Path != "" {
		if e := os.MkdirAll(filepath.Dir(i.Path), 0700); e != nil {
			return nil, e
		}
		data, _ := json.MarshalIndent(s, "", "  ")
		f, e := os.CreateTemp(filepath.Dir(i.Path), ".project-index-*")
		if e != nil {
			return nil, e
		}
		name := f.Name()
		defer os.Remove(name)
		if _, e = f.Write(data); e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil {
			e = os.Rename(name, i.Path)
		}
		if e != nil {
			return nil, e
		}
	}
	i.snapshot = s
	return s, nil
}

var stop = map[string]bool{}

func init() {
	for _, s := range strings.Fields("а в во и из к ко на о об от по с со у за для как где что это ли или не мне мы вы я the a an is are to of in on how what where project проект проекта пожалуйста расскажи объясни help") {
		stop[s] = true
	}
}
func terms(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(text, "ё", "е")), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	var result []string
	for _, w := range words {
		if stop[w] || len([]rune(w)) < 2 {
			continue
		}
		if len([]rune(w)) > 5 {
			for _, end := range []string{"иями", "ение", "ения", "ами", "ого", "ому", "ией", "ий", "ая", "ые", "ой", "ов", "ам", "ах", "ом", "ы", "а", "я", "у", "е", "и"} {
				if strings.HasSuffix(w, end) {
					w = strings.TrimSuffix(w, end)
					break
				}
			}
		}
		result = append(result, w)
	}
	return result
}
func queryTerms(q string) []string {
	q = strings.ToLower(q)
	for _, pair := range [][2]string{{"устроен", "структура архитектура"}, {"структур", "каталоги файлы"}, {"запуст", "запуск"}, {"запуск", "docker compose"}, {"памят", "memory"}, {"ветк", "git branch"}, {"эндпоинт", "api"}, {"модел", "model"}} {
		if strings.Contains(q, pair[0]) {
			q += " " + pair[1]
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range terms(q) {
		if !seen[t] {
			out = append(out, t)
			seen[t] = true
		}
	}
	return out
}
func Search(s *Snapshot, query string) []agent.Source {
	q := queryTerms(query)
	if len(q) == 0 {
		return nil
	}
	tf := make([]map[string]int, len(s.Chunks))
	lengths := make([]int, len(tf))
	df := map[string]int{}
	avg := 0.0
	for n, c := range s.Chunks {
		tf[n] = map[string]int{}
		for _, t := range terms(c.Source + " " + c.Section + " " + c.Section + " " + c.Text) {
			tf[n][t]++
			lengths[n]++
		}
		for t := range tf[n] {
			df[t]++
		}
		avg += float64(lengths[n])
	}
	if avg == 0 {
		return nil
	}
	avg /= float64(len(tf))
	type candidate struct {
		n     int
		score float64
	}
	var hits []candidate
	for n := range tf {
		score := 0.0
		matched := 0
		heading := map[string]bool{}
		for _, t := range terms(s.Chunks[n].Section) {
			heading[t] = true
		}
		headingMatches := 0
		for _, t := range q {
			f := float64(tf[n][t])
			if f == 0 {
				continue
			}
			matched++
			if heading[t] {
				headingMatches++
			}
			idf := math.Log(1 + (float64(len(tf)-df[t])+0.5)/(float64(df[t])+0.5))
			score += idf * f * 2.2 / (f + 1.2*(0.25+0.75*float64(lengths[n])/avg))
		}
		// Prefer the section named by a question over incidental mentions in
		// long examples. This applies to every heading, not specific documents.
		score *= 1 + 0.35*float64(min(headingMatches, 3))
		if matched > 0 && score >= 0.25 && float64(matched)/float64(len(q)) >= 0.12 {
			hits = append(hits, candidate{n, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	var out []agent.Source
	perFile := map[string]int{}
	budget := 0
	for _, hit := range hits {
		c := s.Chunks[hit.n]
		if perFile[c.Source] >= 2 || budget+len([]rune(c.Text)) > 6800 {
			continue
		}
		out = append(out, agent.Source{Ref: fmt.Sprintf("S%d", len(out)+1), Source: c.Source, Title: c.Title, Section: c.Section, ChunkID: c.ID, Text: c.Text, Score: hit.score})
		perFile[c.Source]++
		budget += len([]rune(c.Text))
		if len(out) == 4 {
			break
		}
	}
	return out
}
