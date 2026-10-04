// Package rag connects the existing document index to the conversational agent.
package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
)

const TopK = 5
const ContextCharacters = 9000

type Retriever struct {
	Documents *docindex.Service
	LLM       agent.LLM
	Model     string
}

func (r Retriever) Retrieve(ctx context.Context, query string) (*agent.Retrieval, error) {
	return r.RetrieveConfigured(ctx, query, agent.DefaultRetrievalOptions())
}
func (r Retriever) RetrieveConfigured(ctx context.Context, query string, o agent.RetrievalOptions) (*agent.Retrieval, error) {
	start := time.Now()
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if r.Documents == nil || r.Documents.Store == nil {
		return nil, fmt.Errorf("индекс недоступен")
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 8000 {
		return nil, fmt.Errorf("вопрос должен содержать 1–8000 байт UTF-8")
	}
	s := r.Documents
	info, err := s.Store.Info()
	if err != nil {
		return nil, err
	}
	if s.Embedder == nil || s.Embedder.Model() != info.Model || s.Embedder.Dimensions() != info.Dimensions {
		return nil, fmt.Errorf("модель/размерность эмбеддингов не совпадает с индексом")
	}
	result := &agent.Retrieval{Mode: "rag", Options: &o, Query: query, Strategy: docindex.Structured, CorpusHash: info.CorpusHash, EmbeddingModel: info.Model}
	queries := []string{query}
	if o.Mode == "rewrite" || o.Mode == "full" {
		rewritten, step, err := r.rewrite(ctx, query)
		result.Steps = append(result.Steps, step)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			result.Warning = "Переписывание не выполнено; использован исходный вопрос: " + err.Error()
		} else {
			result.RewrittenQuery = rewritten
			if rewritten != query {
				queries = append(queries, rewritten)
			}
		}
	}
	batch, err := s.Embedder.Embed(ctx, queries)
	if err != nil {
		return nil, err
	}
	if len(batch.Vectors) != len(queries) {
		return nil, fmt.Errorf("неполный ответ эмбеддингов")
	}
	result.EmbeddingTokens = batch.Tokens
	lists := make([][]docindex.Match, 0, len(queries))
	for _, v := range batch.Vectors {
		matches, err := s.Store.Search(ctx, docindex.Structured, v, o.TopKBefore)
		if err != nil {
			return nil, err
		}
		lists = append(lists, matches)
	}
	result.Candidates = fuse(lists, o.TopKBefore)
	if (o.Mode == "filter" || o.Mode == "full") && len(result.Candidates) > 0 {
		step, err := r.rerank(ctx, query, result.Candidates)
		result.Steps = append(result.Steps, step)
		if err != nil {
			return nil, fmt.Errorf("оценка релевантности не выполнена: %w", err)
		}
	}
	selectSources(result, o)
	after, err := s.Store.Info()
	if err != nil {
		return nil, err
	}
	if after.CorpusHash != info.CorpusHash || after.CreatedAt != info.CreatedAt {
		return nil, fmt.Errorf("индекс изменился во время поиска; повторите запрос")
	}
	result.DurationMS = time.Since(start).Milliseconds()
	return result, nil
}

// Reciprocal-rank fusion avoids comparing cosine scores from different queries.
// With one list, its original order is preserved. Ties use original rank then ID.
func fuse(lists [][]docindex.Match, limit int) []agent.Candidate {
	byID := map[string]*agent.Candidate{}
	for li, list := range lists {
		for rank, m := range list {
			c, ok := byID[m.Chunk.ID]
			if !ok {
				c = &agent.Candidate{Source: agent.Source{ChunkID: m.Chunk.ID, Source: m.Chunk.Source, Title: m.Chunk.Title, Section: m.Chunk.Section, Text: m.Chunk.Text, Score: m.Score}}
				byID[m.Chunk.ID] = c
			}
			if li == 0 {
				c.OriginalRank = rank + 1
			} else {
				c.RewriteRank = rank + 1
			}
			c.FusionScore += 1 / float64(60+rank+1)
		}
	}
	out := make([]agent.Candidate, 0, len(byID))
	for _, c := range byID {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FusionScore != out[j].FusionScore {
			return out[i].FusionScore > out[j].FusionScore
		}
		a, b := out[i].OriginalRank, out[j].OriginalRank
		if a == 0 {
			a = 1000
		}
		if b == 0 {
			b = 1000
		}
		if a != b {
			return a < b
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
func selectSources(r *agent.Retrieval, o agent.RetrievalOptions) {
	r.Options = &o
	r.Sources = nil
	filtered := o.Mode == "filter" || o.Mode == "full"
	order := make([]int, len(r.Candidates))
	for i := range order {
		order[i] = i
	}
	if filtered {
		sort.SliceStable(order, func(i, j int) bool { return r.Candidates[order[i]].Relevance > r.Candidates[order[j]].Relevance })
	}
	remaining := ContextCharacters
	for _, i := range order {
		c := &r.Candidates[i]
		c.Ref = ""
		switch {
		case filtered && c.Relevance < o.RelevanceThreshold:
			c.Decision = "below_threshold"
		case len(r.Sources) >= o.TopKAfter:
			c.Decision = "top_k"
		case remaining <= 0:
			c.Decision = "context_budget"
		default:
			c.Decision = "selected"
			src := c.Source
			text := []rune(src.Text)
			if len(text) > remaining {
				text = text[:remaining]
			}
			src.Text = string(text)
			remaining -= len(text)
			src.Ref = fmt.Sprintf("S%d", len(r.Sources)+1)
			c.Ref = src.Ref
			r.Sources = append(r.Sources, src)
		}
	}
	r.Empty = len(r.Sources) == 0
}
