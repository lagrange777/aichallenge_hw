package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codex-chat-cli/internal/agent"
)

var Modes = []string{"baseline", "filter", "rewrite", "full"}

type BenchmarkQuestion struct {
	Question
	Answerable bool   `json:"answerable"`
	Group      string `json:"group"`
}
type ModeResult struct {
	Mode           string `json:"mode"`
	Answer         Answer `json:"answer"`
	EvidenceBefore bool   `json:"evidenceBefore"`
	EvidenceAfter  bool   `json:"evidenceAfter"`
}
type Comparison struct {
	Question BenchmarkQuestion `json:"question"`
	Results  []ModeResult      `json:"results"`
}
type Experiment struct {
	Version       int                    `json:"version"`
	CreatedAt     time.Time              `json:"createdAt"`
	Model         string                 `json:"model"`
	Prompt        string                 `json:"prompt"`
	RewritePrompt string                 `json:"rewritePrompt"`
	RerankPrompt  string                 `json:"rerankPrompt"`
	CorpusHash    string                 `json:"corpusHash"`
	DatasetHash   string                 `json:"datasetHash"`
	Settings      agent.RetrievalOptions `json:"settings"`
	Complete      bool                   `json:"complete"`
	Evaluation    string                 `json:"evaluation"`
	Comparisons   []Comparison           `json:"comparisons"`
	Calibration   *Calibration           `json:"calibration,omitempty"`
}
type CalibrationScore struct {
	Threshold         int `json:"threshold"`
	AnswerableHits    int `json:"answerableHits"`
	UnanswerableEmpty int `json:"unanswerableEmpty"`
	Selected          int `json:"selected"`
}
type Calibration struct {
	CreatedAt   time.Time              `json:"createdAt"`
	Model       string                 `json:"model"`
	CorpusHash  string                 `json:"corpusHash"`
	DatasetHash string                 `json:"datasetHash"`
	Settings    agent.RetrievalOptions `json:"settings"`
	Scores      []CalibrationScore     `json:"scores"`
	Questions   []BenchmarkQuestion    `json:"questions"`
	Retrievals  []*agent.Retrieval     `json:"retrievals"`
	Rationale   string                 `json:"rationale"`
}

func LoadBenchmark(path string) ([]BenchmarkQuestion, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var qs []BenchmarkQuestion
	if err = json.Unmarshal(b, &qs); err != nil {
		return nil, "", err
	}
	if len(qs) < 1 || len(qs) > 50 {
		return nil, "", fmt.Errorf("нужно 1–50 вопросов")
	}
	ids := map[string]bool{}
	for _, q := range qs {
		if q.ID == "" || ids[q.ID] || strings.TrimSpace(q.Query) == "" || len(q.Query) > 8000 || len(q.Expectations) == 0 || q.Group == "" || q.Answerable != (len(q.Relevant) > 0) {
			return nil, "", fmt.Errorf("некорректный вопрос %s", q.ID)
		}
		ids[q.ID] = true
	}
	return qs, AnswerHash(string(b)), nil
}
func hit(sources []agent.Source, q Question) bool {
	for _, s := range sources {
		for _, e := range q.Relevant {
			if s.Source == e.Source && e.Quote != "" && strings.Contains(s.Text, e.Quote) {
				return true
			}
		}
	}
	return false
}
func BeforeHit(r *agent.Retrieval, q Question) bool {
	sources := make([]agent.Source, len(r.Candidates))
	for i, c := range r.Candidates {
		sources[i] = c.Source
	}
	return hit(sources, q)
}

type configured struct {
	r Retriever
	o agent.RetrievalOptions
}

func (c configured) Retrieve(ctx context.Context, q string) (*agent.Retrieval, error) {
	return c.r.RetrieveConfigured(ctx, q, c.o)
}
func RunMode(ctx context.Context, r Retriever, model string, q BenchmarkQuestion, o agent.RetrievalOptions) (ModeResult, error) {
	a, err := AnswerQuestion(ctx, r.LLM, configured{r, o}, model, q.Query, true)
	if err != nil {
		return ModeResult{}, err
	}
	return ModeResult{Mode: o.Mode, Answer: a, EvidenceBefore: BeforeHit(a.Retrieval, q.Question), EvidenceAfter: hit(a.Retrieval.Sources, q.Question)}, nil
}
func CompareFour(ctx context.Context, r Retriever, model string, q BenchmarkQuestion, o agent.RetrievalOptions) (Comparison, error) {
	c := Comparison{Question: q}
	for _, mode := range Modes {
		o.Mode = mode
		v, err := RunMode(ctx, r, model, q, o)
		if err != nil {
			return c, fmt.Errorf("%s: %w", mode, err)
		}
		c.Results = append(c.Results, v)
	}
	return c, nil
}
func Calibrate(ctx context.Context, r Retriever, qs []BenchmarkQuestion, hash string, o agent.RetrievalOptions) (Calibration, error) {
	info, err := r.Documents.Store.Info()
	if err != nil {
		return Calibration{}, err
	}
	c := Calibration{CreatedAt: time.Now().UTC(), Model: r.Model, CorpusHash: info.CorpusHash, DatasetHash: hash, Questions: qs}
	// Calibrate the relevance threshold on original questions. Rewriting is tested
	// separately, so it cannot hide a threshold that discards useful evidence.
	o.Mode = "filter"
	o.RelevanceThreshold = 0
	for _, q := range qs {
		v, err := r.RetrieveConfigured(ctx, q.Query, o)
		if err != nil {
			return c, err
		}
		c.Retrievals = append(c.Retrievals, v)
	}
	bestUtility, bestDistance := -1, 100
	for _, threshold := range []int{1, 2, 3} {
		score := CalibrationScore{Threshold: threshold}
		o.RelevanceThreshold = threshold
		for i, rr := range c.Retrievals {
			v := *rr
			v.Candidates = append([]agent.Candidate(nil), rr.Candidates...)
			selectSources(&v, o)
			score.Selected += len(v.Sources)
			if qs[i].Answerable && hit(v.Sources, qs[i].Question) {
				score.AnswerableHits++
			}
			if !qs[i].Answerable && len(v.Sources) == 0 {
				score.UnanswerableEmpty++
			}
		}
		c.Scores = append(c.Scores, score)
		utility := score.AnswerableHits + score.UnanswerableEmpty
		distance := threshold - 2
		if distance < 0 {
			distance = -distance
		}
		if utility > bestUtility || (utility == bestUtility && distance < bestDistance) {
			bestUtility, bestDistance = utility, distance
			c.Settings = o
		}
	}
	c.Rationale = "Порог 1/2/3: максимум суммы попаданий эталонного доказательства для ответимых вопросов и пустых выборок для неответимых. При равенстве — ближайший к 2 (предпочтение задано до прогона). Настройка только на отдельном calibration-наборе, без итоговых вопросов."
	return c, nil
}
func SaveCalibration(dir string, c Calibration) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "calibration.json"), append(b, '\n'))
}
func ApplyExperimentReviews(r *Experiment, reviews map[string]map[string]Review) error {
	if !r.Complete || len(reviews) != len(r.Comparisons) {
		return fmt.Errorf("нужен полный набор оценок")
	}
	for i := range r.Comparisons {
		c := &r.Comparisons[i]
		grades := reviews[c.Question.ID]
		if len(grades) != 4 || len(c.Results) != 4 {
			return fmt.Errorf("нужны четыре оценки %s", c.Question.ID)
		}
		for j := range c.Results {
			v := &c.Results[j]
			g, ok := grades[v.Mode]
			if !ok || g.AnswerHash != AnswerHash(v.Answer.Text) || g.Correctness < 0 || g.Correctness > 2 || g.Completeness < 0 || g.Completeness > 2 || g.Rationale == "" || g.CitationSupport == nil {
				return fmt.Errorf("неверная/устаревшая оценка %s/%s", c.Question.ID, v.Mode)
			}
			v.Answer.Review = &g
		}
	}
	r.Evaluation = "Смысловая проверка Codex по ожиданиям и источникам; без независимого второго оценщика. Корректность/полнота 0–2. Для вопросов без ответа правильное воздержание оценивается 2/2. Ссылки проверяются по содержанию, а не только по ID."
	return nil
}
func SaveExperiment(dir string, r Experiment) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = writeAtomic(filepath.Join(dir, "comparison.json"), append(b, '\n')); err != nil {
		return err
	}
	var s strings.Builder
	fmt.Fprintf(&s, "# Реранкинг, фильтрация и query rewrite\n\nМодель `%s`; корпус `%s`; набор `%s`.\n\nK до=%d; K после=%d; порог=%d/3. Каждый режим — новая сессия без памяти, профиля, MCP и истории. Одинаковая инструкция ответа, температура по умолчанию; rewrite и reranker не видят ожиданий. Вызовы независимы, поэтому возможна вариативность rewrite и ответов. Время и LLM-токены включают вспомогательные вызовы; embedding tokens отдельно.\n\n%s\n\n", r.Model, r.CorpusHash, r.DatasetHash, r.Settings.TopKBefore, r.Settings.TopKAfter, r.Settings.RelevanceThreshold, r.Evaluation)
	fmt.Fprintln(&s, "| Вопрос | Режим | Корректность / полнота | Эталон до → после | Чанков | LLM токены | мс |\n|---|---|---|---|---:|---:|---:|")
	for _, c := range r.Comparisons {
		for _, v := range c.Results {
			fmt.Fprintf(&s, "| %s | %s | %s | %t → %t | %d | %d | %d |\n", c.Question.ID, v.Mode, grade(v.Answer.Review), v.EvidenceBefore, v.EvidenceAfter, len(v.Answer.Retrieval.Sources), v.Answer.Usage.TotalTokens, v.Answer.DurationMS)
		}
	}
	for _, c := range r.Comparisons {
		fmt.Fprintf(&s, "\n## %s — %s\n\nОтвет есть в базе: %t. Ожидания: %s\n\n", c.Question.ID, c.Question.Query, c.Question.Answerable, strings.Join(c.Question.Expectations, "; "))
		for _, v := range c.Results {
			a := v.Answer
			rr := a.Retrieval
			fmt.Fprintf(&s, "### %s\n\n%s\n\nRewrite: %s\n\nПредупреждение: %s\n\n", v.Mode, a.Text, rr.RewrittenQuery, rr.Warning)
			if a.Review != nil {
				fmt.Fprintf(&s, "Оценка %s. Галлюцинация: %t; воздержание: %t. %s\n\n", grade(a.Review), a.Review.Hallucination, a.Review.Abstention, a.Review.Rationale)
			}
			for _, src := range rr.Sources {
				fmt.Fprintf(&s, "- [%s] %s / %s (`%s`)\n", src.Ref, src.Source, src.Section, src.ChunkID)
			}
			fmt.Fprintln(&s, "\nВсе кандидаты, оценки, цитаты реранкера и причины отсева сохранены в comparison.json.")
		}
	}
	return writeAtomic(filepath.Join(dir, "comparison.md"), []byte(s.String()))
}
