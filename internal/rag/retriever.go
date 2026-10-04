// Package rag connects the existing document index to the conversational agent.
package rag

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
	"context"
	"fmt"
	"strings"
	"time"
)

const TopK = 5
const ContextCharacters = 9000

type Retriever struct{ Documents *docindex.Service }

func (r Retriever) Retrieve(ctx context.Context, query string) (*agent.Retrieval, error) {
	start := time.Now()
	if r.Documents == nil || r.Documents.Store == nil {
		return nil, fmt.Errorf("индекс недоступен")
	}
	if strings.TrimSpace(query) == "" || len(query) > 8000 {
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
	batch, err := s.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(batch.Vectors) != 1 {
		return nil, fmt.Errorf("неполный ответ эмбеддингов")
	}
	matches, err := s.Store.Search(ctx, docindex.Structured, batch.Vectors[0], TopK)
	if err != nil {
		return nil, err
	}
	result := &agent.Retrieval{Mode: "rag", Strategy: docindex.Structured, CorpusHash: info.CorpusHash, EmbeddingModel: info.Model, EmbeddingTokens: batch.Tokens}
	remaining := ContextCharacters
	for _, m := range matches {
		if remaining <= 0 {
			break
		}
		text := []rune(m.Chunk.Text)
		if len(text) > remaining {
			text = text[:remaining]
		}
		remaining -= len(text)
		result.Sources = append(result.Sources, agent.Source{Ref: fmt.Sprintf("S%d", len(result.Sources)+1), ChunkID: m.Chunk.ID, Source: m.Chunk.Source, Title: m.Chunk.Title, Section: m.Chunk.Section, Text: string(text), Score: m.Score})
	}
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
