package rag

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/docindex"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const EvaluationPrompt = "Ответь по-русски на вопрос о проекте codex-chat-cli. Ответ до 150 слов. Если факты о проекте неизвестны, явно признай недостаток информации. Не выдумывай детали."

type Question struct {
	ID           string              `json:"id"`
	Query        string              `json:"query"`
	Expectations []string            `json:"expectations"`
	Relevant     []docindex.Evidence `json:"relevant"`
}
type Review struct {
	AnswerHash      string `json:"answerHash"`
	Correctness     int    `json:"correctness"`
	Completeness    int    `json:"completeness"`
	Hallucination   bool   `json:"hallucination"`
	Abstention      bool   `json:"abstention"`
	CitationSupport *bool  `json:"citationSupport,omitempty"`
	Rationale       string `json:"rationale"`
}
type Answer struct {
	Text       string           `json:"text"`
	Model      string           `json:"model"`
	Usage      agent.Usage      `json:"usage"`
	DurationMS int64            `json:"durationMs"`
	Retrieval  *agent.Retrieval `json:"retrieval"`
	Review     *Review          `json:"review,omitempty"`
}
type Pair struct {
	Question    Question `json:"question"`
	Without     Answer   `json:"without"`
	With        Answer   `json:"with"`
	EvidenceHit bool     `json:"evidenceHit"`
}
type Report struct {
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"createdAt"`
	Model             string    `json:"model"`
	Prompt            string    `json:"prompt"`
	Temperature       *float64  `json:"temperature"`
	CorpusHash        string    `json:"corpusHash"`
	Strategy          string    `json:"strategy"`
	TopK              int       `json:"topK"`
	ContextCharacters int       `json:"contextCharacters"`
	Complete          bool      `json:"complete"`
	Evaluation        string    `json:"evaluation"`
	Pairs             []Pair    `json:"pairs"`
}

// This wrapper intentionally exposes only Complete: comparison uses one answering
// call per mode, without token-count API calls, memory, tools, profiles or history.
type comparisonLLM struct{ agent.LLM }

func (l comparisonLLM) Complete(ctx context.Context, r agent.CompletionRequest) (agent.CompletionResponse, error) {
	if !r.Internal {
		r.Instructions = EvaluationPrompt
	}
	return l.LLM.Complete(ctx, r)
}
func AnswerQuestion(ctx context.Context, llm agent.LLM, retriever agent.Retriever, model, query string, enabled bool) (Answer, error) {
	a := agent.New(comparisonLLM{llm}, model, agent.WithRetriever(retriever))
	response, err := a.Ask(ctx, agent.Request{Message: query, RAG: enabled})
	if err != nil {
		return Answer{}, err
	}
	return Answer{Text: response.Text, Model: response.Model, Usage: response.Usage, DurationMS: response.Duration.Milliseconds(), Retrieval: response.Retrieval}, nil
}
func Compare(ctx context.Context, llm agent.LLM, retriever agent.Retriever, model string, q Question) (Pair, error) {
	p := Pair{Question: q}
	var err error
	p.Without, err = AnswerQuestion(ctx, llm, retriever, model, q.Query, false)
	if err != nil {
		return p, err
	}
	p.With, err = AnswerQuestion(ctx, llm, retriever, model, q.Query, true)
	if err != nil {
		return p, err
	}
	for _, expected := range q.Relevant {
		for _, source := range p.With.Retrieval.Sources {
			if source.Source == expected.Source && strings.Contains(source.Text, expected.Quote) {
				p.EvidenceHit = true
			}
		}
	}
	return p, nil
}
func LoadQuestions(path string) ([]Question, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var q []Question
	if err = json.Unmarshal(b, &q); err != nil {
		return nil, err
	}
	if len(q) != 10 {
		return nil, fmt.Errorf("требуется ровно 10 контрольных вопросов")
	}
	ids := map[string]bool{}
	for _, v := range q {
		if v.ID == "" || ids[v.ID] || strings.TrimSpace(v.Query) == "" || len(v.Query) > 8000 || len(v.Expectations) == 0 || len(v.Relevant) == 0 {
			return nil, fmt.Errorf("некорректный вопрос %q", v.ID)
		}
		ids[v.ID] = true
	}
	return q, nil
}
func AnswerHash(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }
func ApplyReviews(report *Report, reviews map[string]map[string]Review) error {
	if !report.Complete || len(report.Pairs) != 10 || len(reviews) != 10 {
		return fmt.Errorf("оценка требует полный набор из 10 пар")
	}
	for i := range report.Pairs {
		p := &report.Pairs[i]
		grades := reviews[p.Question.ID]
		if len(grades) != 2 {
			return fmt.Errorf("нужны две оценки %s", p.Question.ID)
		}
		for mode, a := range map[string]*Answer{"without": &p.Without, "with": &p.With} {
			r, ok := grades[mode]
			if !ok || r.AnswerHash != AnswerHash(a.Text) || r.Correctness < 0 || r.Correctness > 2 || r.Completeness < 0 || r.Completeness > 2 || strings.TrimSpace(r.Rationale) == "" || (mode == "with" && r.CitationSupport == nil) {
				return fmt.Errorf("некорректная/устаревшая оценка %s/%s", p.Question.ID, mode)
			}
			a.Review = &r
		}
	}
	report.Evaluation = "Экспертная оценка Codex по зафиксированным ожиданиям и текстам источников; без независимого второго оценщика. Корректность и полнота: 0–2. Отказ без фактов: 0 по обоим критериям, но не галлюцинация."
	return nil
}
func WriteReport(dir string, r Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = writeAtomic(filepath.Join(dir, "comparison.json"), append(b, '\n')); err != nil {
		return err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "# Первый RAG-запрос\n\nМодель: `%s`. Корпус: `%s`.\n\nСтратегия: %s, top-%d, максимум %d символов текста.\n\nКаждый ответ: новая сессия, один запрос к LLM, одинаковая инструкция; температура не задаётся (значение по умолчанию модели). Нет памяти, профиля, MCP и истории. Ожидания и эталонные цитаты не передаются отвечающей модели.\n\n%s\n\n", r.Model, r.CorpusHash, r.Strategy, r.TopK, r.ContextCharacters, r.Evaluation)
	fmt.Fprintln(&s, "| Вопрос | Без RAG: корректность / полнота | RAG: корректность / полнота | Эталон в top-5 |\n|---|---|---|---|")
	for _, p := range r.Pairs {
		fmt.Fprintf(&s, "| %s | %s | %s | %t |\n", p.Question.ID, grade(p.Without.Review), grade(p.With.Review), p.EvidenceHit)
	}
	for _, p := range r.Pairs {
		fmt.Fprintf(&s, "\n## %s — %s\n\nОжидания: %s\n\n", p.Question.ID, p.Question.Query, strings.Join(p.Question.Expectations, "; "))
		for _, e := range p.Question.Relevant {
			fmt.Fprintf(&s, "Эталон: %s — %q\n\n", e.Source, e.Quote)
		}
		for _, entry := range []struct {
			name string
			a    Answer
		}{{"Без RAG", p.Without}, {"С RAG", p.With}} {
			a := entry.a
			fmt.Fprintf(&s, "### %s\n\n%s\n\nВремя: %d мс; LLM tokens in/out/total: %d/%d/%d.\n\n", entry.name, a.Text, a.DurationMS, a.Usage.InputTokens, a.Usage.OutputTokens, a.Usage.TotalTokens)
			if a.Review != nil {
				fmt.Fprintf(&s, "Оценка: %s. Галлюцинация: %t. Воздержание: %t. %s\n\n", grade(a.Review), a.Review.Hallucination, a.Review.Abstention, a.Review.Rationale)
			}
			if a.Retrieval != nil && a.Retrieval.Mode == "rag" {
				fmt.Fprintf(&s, "Поиск: %d мс, embedding tokens: %d. Цитаты: %v; неизвестные ID: %v.\n\n", a.Retrieval.DurationMS, a.Retrieval.EmbeddingTokens, a.Retrieval.Citations, a.Retrieval.InvalidCitations)
				for _, src := range a.Retrieval.Sources {
					fmt.Fprintf(&s, "- [%s] %s / %s; chunk `%s`; cosine %.4f\n", src.Ref, src.Source, src.Section, src.ChunkID, src.Score)
				}
			}
		}
	}
	return writeAtomic(filepath.Join(dir, "comparison.md"), []byte(s.String()))
}
func grade(r *Review) string {
	if r == nil {
		return "не оценено"
	}
	return fmt.Sprintf("%d / %d", r.Correctness, r.Completeness)
}
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rag-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
