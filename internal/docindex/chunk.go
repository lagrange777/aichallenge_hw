// Package docindex builds a local, reproducible document retrieval index.
package docindex

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const Fixed = "fixed"
const Structured = "structured"

type Section struct {
	Title string `json:"title"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}
type Document struct {
	Source     string    `json:"source"`
	Title      string    `json:"title"`
	Hash       string    `json:"sha256"`
	Characters int       `json:"characters"`
	Words      int       `json:"words"`
	Text       string    `json:"-"`
	Sections   []Section `json:"sections"`
}
type Chunk struct {
	ID       string    `json:"chunk_id"`
	Source   string    `json:"source"`
	Title    string    `json:"title"`
	Section  string    `json:"section"`
	Strategy string    `json:"strategy"`
	Start    int       `json:"start"`
	End      int       `json:"end"`
	Text     string    `json:"text"`
	Hash     string    `json:"content_hash"`
	Vector   []float32 `json:"-"`
}
type ChunkConfig struct {
	Size    int `json:"size_characters"`
	Overlap int `json:"overlap_characters"`
}

func DefaultChunkConfig() ChunkConfig { return ChunkConfig{1800, 240} }
func (c ChunkConfig) Validate() error {
	// <= 2000 Unicode scalars implies <= 8000 UTF-8 bytes, below the
	// 8192-token input limit even in the worst case of byte-level tokens.
	if c.Size < 100 || c.Size > 2000 || c.Overlap < 0 || c.Overlap >= c.Size/2 {
		return fmt.Errorf("размер: 100–2000 символов; перекрытие: от 0 до размера/2 (не включая)")
	}
	return nil
}
func digest(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

// LoadCorpus reads only UTF-8 Markdown, text and Go files. Symbolic links are
// rejected; the explicit corpus directory must not contain secrets or data stores.
func LoadCorpus(root string) ([]Document, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("корпус должен быть каталогом")
	}
	var docs []Document
	total := 0
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("символическая ссылка в корпусе: %s", path)
		}
		if e.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if strings.HasSuffix(path, ".go.txt") {
			ext = ".go"
		}
		if ext != ".md" && ext != ".txt" && ext != ".go" {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Size() > 2<<20 || len(docs) >= 256 {
			return fmt.Errorf("слишком большой корпус")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(b)
		if total > 16<<20 {
			return fmt.Errorf("корпус больше 16 МиБ")
		}
		if !utf8.Valid(b) {
			return fmt.Errorf("%s: требуется UTF-8", path)
		}
		text := strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n")
		if strings.TrimSpace(text) == "" {
			return nil
		}
		source, _ := filepath.Rel(root, path)
		source = filepath.ToSlash(source)
		doc := Document{Source: source, Title: filepath.Base(path), Hash: digest(text), Text: text, Characters: utf8.RuneCountInString(text), Words: len(strings.Fields(text))}
		doc.Sections, err = sections(text, ext)
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if ext == ".md" {
			for _, s := range doc.Sections {
				if s.Title != "Преамбула" {
					doc.Title = strings.Split(s.Title, " > ")[0]
					break
				}
			}
		}
		docs = append(docs, doc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("в корпусе нет непустых .md, .txt или .go файлов")
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Source < docs[j].Source })
	return docs, nil
}

func sections(text, ext string) ([]Section, error) {
	result := []Section{{Title: "Преамбула", Start: 0}}
	add := func(pos int, title string) {
		if pos == result[len(result)-1].Start {
			result[len(result)-1].Title = title
		} else {
			result = append(result, Section{Title: title, Start: pos})
		}
	}
	if ext == ".go" {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, "", text, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			pos := decl.Pos()
			name := ""
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name = "func " + d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					t := d.Recv.List[0].Type
					if star, ok := t.(*ast.StarExpr); ok {
						t = star.X
					}
					if ident, ok := t.(*ast.Ident); ok {
						name = "func " + ident.Name + "." + d.Name.Name
					}
				}
				if d.Doc != nil {
					pos = d.Doc.Pos()
				}
			case *ast.GenDecl:
				name = d.Tok.String()
				if d.Doc != nil {
					pos = d.Doc.Pos()
				}
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						name += " " + s.Name.Name
					case *ast.ValueSpec:
						for _, n := range s.Names {
							name += " " + n.Name
						}
					}
				}
			}
			add(utf8.RuneCountInString(text[:set.Position(pos).Offset]), name)
		}
	} else if ext == ".md" {
		pos := 0
		var hierarchy [6]string
		fence := byte(0)
		fenceLen := 0
		for _, line := range strings.SplitAfter(text, "\n") {
			trim := strings.TrimSpace(line)
			if len(trim) > 0 && (trim[0] == '`' || trim[0] == '~') {
				n := 0
				for n < len(trim) && trim[n] == trim[0] {
					n++
				}
				if n >= 3 {
					if fence == 0 {
						fence = trim[0]
						fenceLen = n
					} else if trim[0] == fence && n >= fenceLen && strings.TrimSpace(trim[n:]) == "" {
						fence = 0
					}
					pos += utf8.RuneCountInString(line)
					continue
				}
			}
			if fence == 0 {
				n := 0
				for n < len(trim) && trim[n] == '#' {
					n++
				}
				if n >= 1 && n <= 6 && len(trim) > n && (trim[n] == ' ' || trim[n] == '\t') {
					title := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(trim[n:]), "#"))
					hierarchy[n-1] = title
					for i := n; i < 6; i++ {
						hierarchy[i] = ""
					}
					var names []string
					for _, v := range hierarchy {
						if v != "" {
							names = append(names, v)
						}
					}
					add(pos, strings.Join(names, " > "))
				}
			}
			pos += utf8.RuneCountInString(line)
		}
	}
	for i := range result {
		result[i].End = utf8.RuneCountInString(text)
		if i+1 < len(result) {
			result[i].End = result[i+1].Start
		}
	}
	return result, nil
}

func Split(d Document, strategy string, cfg ChunkConfig) ([]Chunk, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strategy != Fixed && strategy != Structured {
		return nil, fmt.Errorf("неизвестная стратегия %q", strategy)
	}
	runes := []rune(d.Text)
	out := []Chunk{}
	emit := func(start, end int) {
		text := string(runes[start:end])
		if strings.TrimSpace(text) == "" {
			return
		}
		var names []string
		for _, s := range d.Sections {
			if s.Start < end && s.End > start {
				names = append(names, s.Title)
			}
		}
		c := Chunk{Source: d.Source, Title: d.Title, Section: strings.Join(names, " | "), Strategy: strategy, Start: start, End: end, Text: text, Hash: digest(text)}
		c.ID = digest(fmt.Sprintf("v1\x00%s\x00%s\x00%d\x00%d\x00%s", d.Source, strategy, start, end, c.Hash))
		out = append(out, c)
	}
	spans := []Section{{Start: 0, End: len(runes)}}
	if strategy == Structured {
		spans = d.Sections
	}
	for _, s := range spans {
		for start := s.Start; start < s.End; {
			end := min(start+cfg.Size, s.End)
			if strategy == Structured && end < s.End {
				// Prefer a paragraph boundary in the last half of a full chunk.
				for i := end - 1; i > start+cfg.Size/2; i-- {
					if runes[i] == '\n' && runes[i-1] == '\n' {
						end = i + 1
						break
					}
				}
			}
			emit(start, end)
			if end == s.End {
				break
			}
			start = end - cfg.Overlap
		}
	}
	return out, nil
}

// Preview measures the exact corpus and proposed chunks without API calls or
// creating an index. It is intentionally distinct from a completed build.
func Preview(docs []Document, cfg ChunkConfig) (map[string]any, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	words, characters, bytes := 0, 0, 0
	unique := map[string]string{}
	stats := map[string]int{}
	for _, d := range docs {
		words += d.Words
		characters += d.Characters
		for _, strategy := range []string{Fixed, Structured} {
			chunks, err := Split(d, strategy, cfg)
			if err != nil {
				return nil, err
			}
			stats[strategy] += len(chunks)
			for _, c := range chunks {
				unique[c.Hash] = c.Text
			}
		}
	}
	for _, text := range unique {
		bytes += len(text)
	}
	return map[string]any{"status": "prepared_without_embeddings", "documents": docs, "words": words, "characters": characters, "estimated_pages_at_400_words": float64(words) / 400, "chunking": cfg, "chunks": stats, "unique_embedding_inputs": len(unique), "embedding_input_utf8_bytes": bytes, "token_upper_bound_from_utf8_bytes": bytes}, nil
}
