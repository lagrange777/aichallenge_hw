package docindex

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotBuilt = errors.New("индекс ещё не построен: запустите doc-index build")

type Store struct{ db *sql.DB }
type Stats struct {
	Strategy           string  `json:"strategy"`
	Chunks             int     `json:"chunks"`
	Min                int     `json:"min_characters"`
	Median             int     `json:"median_characters"`
	Max                int     `json:"max_characters"`
	Mean               float64 `json:"mean_characters"`
	IndexedCharacters  int     `json:"indexed_characters"`
	CrossSectionChunks int     `json:"cross_section_chunks"`
	VectorBytes        int     `json:"vector_bytes"`
	EmbeddingTokens    int     `json:"embedding_tokens_billed_this_run"`
	CacheHits          int     `json:"cached_chunks"`
	DurationMS         int64   `json:"duration_ms"`
}
type Info struct {
	Version         int         `json:"version"`
	CreatedAt       time.Time   `json:"created_at"`
	CorpusHash      string      `json:"corpus_hash"`
	Model           string      `json:"embedding_model"`
	Dimensions      int         `json:"embedding_dimensions"`
	Config          ChunkConfig `json:"chunking"`
	Documents       []Document  `json:"documents"`
	Words           int         `json:"words"`
	Characters      int         `json:"characters"`
	PagesAt400Words float64     `json:"estimated_pages_at_400_words"`
	Strategies      []Stats     `json:"strategies"`
}

func Open(path string) (*Store, error)         { return openStore(path, false) }
func OpenReadOnly(path string) (*Store, error) { return openStore(path, true) }
func openStore(path string, readOnly bool) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if !readOnly {
		if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		f.Close()
	} else {
		if _, err = os.Stat(abs); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: abs}
	if readOnly {
		u.RawQuery = "mode=ro"
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	if !readOnly {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS document_index_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS document_chunks(id TEXT PRIMARY KEY,strategy TEXT NOT NULL,source TEXT NOT NULL,start INTEGER NOT NULL,payload TEXT NOT NULL,vector BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS document_chunks_strategy ON document_chunks(strategy,source,start);
CREATE TABLE IF NOT EXISTS embedding_cache(model TEXT NOT NULL,dimensions INTEGER NOT NULL,hash TEXT NOT NULL,vector BLOB NOT NULL,PRIMARY KEY(model,dimensions,hash));`)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Info() (Info, error) {
	var raw string
	err := s.db.QueryRow("SELECT value FROM document_index_meta WHERE key='info'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Info{}, ErrNotBuilt
	}
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err = json.Unmarshal([]byte(raw), &info); err != nil {
		return info, err
	}
	if info.Version != 1 || info.Dimensions < 1 || info.Dimensions > 3072 {
		return info, fmt.Errorf("неподдерживаемый формат индекса")
	}
	return info, nil
}
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}
func decodeVector(b []byte, dims int) ([]float32, error) {
	if len(b) != dims*4 {
		return nil, fmt.Errorf("повреждён вектор индекса")
	}
	v := make([]float32, dims)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	if err := normalize(v, dims); err != nil {
		return nil, err
	}
	return v, nil
}

// vectors caches only validated real embeddings, one transaction per successful
// batch. Interrupted builds can resume without paying for completed batches again.
func (s *Store) vectors(ctx context.Context, e Embedder, texts []string, progress func(string)) ([][]float32, int, int, error) {
	result := make([][]float32, len(texts))
	pending := map[string][]int{}
	order := []string{}
	cacheHits := 0
	tokens := 0
	for i, t := range texts {
		hash := digest(t)
		var b []byte
		err := s.db.QueryRowContext(ctx, "SELECT vector FROM embedding_cache WHERE model=? AND dimensions=? AND hash=?", e.Model(), e.Dimensions(), hash).Scan(&b)
		if err == nil {
			v, err := decodeVector(b, e.Dimensions())
			if err != nil {
				return nil, 0, 0, err
			}
			result[i] = v
			cacheHits++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, 0, 0, err
		}
		if _, ok := pending[hash]; !ok {
			order = append(order, hash)
		}
		pending[hash] = append(pending[hash], i)
	}
	for start := 0; start < len(order); start += 32 {
		end := min(start+32, len(order))
		input := []string{}
		for _, hash := range order[start:end] {
			input = append(input, texts[pending[hash][0]])
		}
		if progress != nil {
			progress(fmt.Sprintf("эмбеддинги %d–%d / %d; кэш: %d", start+1, end, len(order), cacheHits))
		}
		batch, err := e.Embed(ctx, input)
		if err != nil {
			return nil, 0, 0, err
		}
		if len(batch.Vectors) != len(input) || batch.Tokens < 0 {
			return nil, 0, 0, fmt.Errorf("неполный пакет эмбеддингов")
		}
		for _, v := range batch.Vectors {
			if err := normalize(v, e.Dimensions()); err != nil {
				return nil, 0, 0, err
			}
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, 0, 0, err
		}
		for i, hash := range order[start:end] {
			v := batch.Vectors[i]
			if _, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO embedding_cache VALUES(?,?,?,?)", e.Model(), e.Dimensions(), hash, encodeVector(v)); err != nil {
				tx.Rollback()
				return nil, 0, 0, err
			}
			for _, n := range pending[hash] {
				result[n] = v
			}
		}
		if err = tx.Commit(); err != nil {
			return nil, 0, 0, err
		}
		tokens += batch.Tokens
	}
	return result, tokens, cacheHits, nil
}

func (s *Store) Build(ctx context.Context, docs []Document, e Embedder, cfg ChunkConfig, progress func(string)) (Info, error) {
	if err := cfg.Validate(); err != nil {
		return Info{}, err
	}
	if len(docs) == 0 {
		return Info{}, fmt.Errorf("пустой корпус")
	}
	info := Info{Version: 1, CreatedAt: time.Now().UTC(), Model: e.Model(), Dimensions: e.Dimensions(), Config: cfg, Documents: docs}
	var hashes strings.Builder
	for _, d := range docs {
		fmt.Fprintf(&hashes, "%s\x00%s\n", d.Source, d.Hash)
		info.Words += d.Words
		info.Characters += d.Characters
	}
	info.CorpusHash = digest(hashes.String())
	info.PagesAt400Words = float64(info.Words) / 400
	var all []Chunk
	for _, strategy := range []string{Fixed, Structured} {
		started := time.Now()
		var chunks []Chunk
		for _, d := range docs {
			c, err := Split(d, strategy, cfg)
			if err != nil {
				return Info{}, err
			}
			chunks = append(chunks, c...)
		}
		texts := make([]string, len(chunks))
		for i := range chunks {
			texts[i] = chunks[i].Text
		}
		if progress != nil {
			progress(fmt.Sprintf("%s: %d чанков", strategy, len(chunks)))
		}
		vectors, tokens, hits, err := s.vectors(ctx, e, texts, progress)
		if err != nil {
			return Info{}, err
		}
		stat := Stats{Strategy: strategy, Chunks: len(chunks), EmbeddingTokens: tokens, CacheHits: hits, VectorBytes: len(chunks) * e.Dimensions() * 4}
		sizes := []int{}
		for i := range chunks {
			chunks[i].Vector = vectors[i]
			n := chunks[i].End - chunks[i].Start
			sizes = append(sizes, n)
			stat.IndexedCharacters += n
			if strings.Contains(chunks[i].Section, " | ") {
				stat.CrossSectionChunks++
			}
		}
		if len(sizes) > 0 {
			sort.Ints(sizes)
			stat.Min = sizes[0]
			stat.Max = sizes[len(sizes)-1]
			stat.Median = sizes[len(sizes)/2]
			stat.Mean = float64(stat.IndexedCharacters) / float64(len(sizes))
		}
		stat.DurationMS = time.Since(started).Milliseconds()
		info.Strategies = append(info.Strategies, stat)
		all = append(all, chunks...)
	}
	// Publish both strategies and their metadata atomically. A failed build
	// leaves the previous searchable index and evaluation intact.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Info{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM document_chunks; DELETE FROM document_index_meta;"); err != nil {
		return Info{}, err
	}
	for _, c := range all {
		payload, _ := json.Marshal(c)
		if _, err = tx.ExecContext(ctx, "INSERT INTO document_chunks VALUES(?,?,?,?,?,?)", c.ID, c.Strategy, c.Source, c.Start, string(payload), encodeVector(c.Vector)); err != nil {
			return Info{}, err
		}
	}
	raw, _ := json.Marshal(info)
	if _, err = tx.ExecContext(ctx, "INSERT INTO document_index_meta VALUES('info',?)", string(raw)); err != nil {
		return Info{}, err
	}
	if err = tx.Commit(); err != nil {
		return Info{}, err
	}
	return info, nil
}

func (s *Store) Chunks(strategy string, limit, offset int) ([]Chunk, error) {
	if strategy != Fixed && strategy != Structured {
		return nil, fmt.Errorf("неизвестная стратегия")
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("некорректная страница")
	}
	rows, err := s.db.Query("SELECT payload FROM document_chunks WHERE strategy=? ORDER BY source,start LIMIT ? OFFSET ?", strategy, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c Chunk
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type Match struct {
	Chunk Chunk   `json:"chunk"`
	Score float64 `json:"score"`
}

func (s *Store) Search(ctx context.Context, strategy string, query []float32, k int) ([]Match, error) {
	if strategy != Fixed && strategy != Structured {
		return nil, fmt.Errorf("неизвестная стратегия")
	}
	if k < 1 || k > 20 {
		return nil, fmt.Errorf("k: 1–20")
	}
	info, err := s.Info()
	if err != nil {
		return nil, err
	}
	q := append([]float32(nil), query...)
	if err = normalize(q, info.Dimensions); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload,vector FROM document_chunks WHERE strategy=?", strategy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Match{}
	for rows.Next() {
		var raw string
		var b []byte
		if err = rows.Scan(&raw, &b); err != nil {
			return nil, err
		}
		v, err := decodeVector(b, info.Dimensions)
		if err != nil {
			return nil, err
		}
		var score float64
		for i := range q {
			score += float64(q[i]) * float64(v[i])
		}
		var c Chunk
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		out = append(out, Match{c, score})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Chunk.ID < out[j].Chunk.ID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

type SearchResult struct {
	Query   string             `json:"query"`
	Model   string             `json:"model"`
	Results map[string][]Match `json:"results"`
}
type Service struct {
	Store    *Store
	Embedder Embedder
}

func (s *Service) Search(ctx context.Context, query string, k int) (SearchResult, error) {
	info, err := s.Store.Info()
	if err != nil {
		return SearchResult{}, err
	}
	if s.Embedder == nil || s.Embedder.Model() != info.Model || s.Embedder.Dimensions() != info.Dimensions {
		return SearchResult{}, fmt.Errorf("модель или размерность поиска не совпадает с индексом")
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 8000 {
		return SearchResult{}, fmt.Errorf("запрос: 1–8000 байт")
	}
	if k < 1 || k > 20 {
		return SearchResult{}, fmt.Errorf("k: 1–20")
	}
	batch, err := s.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return SearchResult{}, err
	}
	if len(batch.Vectors) != 1 {
		return SearchResult{}, fmt.Errorf("неполный ответ эмбеддингов")
	}
	out := SearchResult{Query: query, Model: info.Model, Results: map[string][]Match{}}
	for _, strategy := range []string{Fixed, Structured} {
		matches, err := s.Store.Search(ctx, strategy, batch.Vectors[0], k)
		if err != nil {
			return SearchResult{}, err
		}
		out.Results[strategy] = matches
	}
	return out, nil
}
