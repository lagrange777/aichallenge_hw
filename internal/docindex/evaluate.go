package docindex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

type Evidence struct {
	Source string `json:"source"`
	Quote  string `json:"quote"`
}
type Question struct {
	ID       string     `json:"id"`
	Query    string     `json:"query"`
	Relevant []Evidence `json:"relevant"`
}
type QueryScore struct {
	ID             string  `json:"id"`
	Query          string  `json:"query"`
	Hit            bool    `json:"hit"`
	Recall         float64 `json:"recall"`
	ReciprocalRank float64 `json:"reciprocal_rank"`
	Matches        []Match `json:"matches"`
}
type Evaluation struct {
	Strategy string       `json:"strategy"`
	HitRate  float64      `json:"hit_rate_at_k"`
	Recall   float64      `json:"recall_at_k"`
	MRR      float64      `json:"mrr_at_k"`
	SearchMS int64        `json:"search_ms"`
	Queries  []QueryScore `json:"queries"`
}
type Report struct {
	Info                 Info         `json:"index"`
	CreatedAt            time.Time    `json:"created_at"`
	K                    int          `json:"k"`
	QuestionsHash        string       `json:"questions_hash"`
	QueryEmbeddingTokens int          `json:"query_embedding_tokens_billed_this_run"`
	Evaluations          []Evaluation `json:"evaluations"`
}

func ReadQuestions(path string) ([]Question, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var q []Question
	if err = json.Unmarshal(b, &q); err != nil {
		return nil, err
	}
	if len(q) == 0 || len(q) > 100 {
		return nil, fmt.Errorf("нужно 1–100 контрольных вопросов")
	}
	return q, nil
}
func (s *Store) Evaluate(ctx context.Context, docs []Document, questions []Question, e Embedder, k int) (Report, error) {
	info, err := s.Info()
	if err != nil {
		return Report{}, err
	}
	if e.Model() != info.Model || e.Dimensions() != info.Dimensions {
		return Report{}, fmt.Errorf("другая embedding-модель")
	}
	if k < 1 || k > 20 || len(questions) == 0 {
		return Report{}, fmt.Errorf("некорректные параметры сравнения")
	}
	bySource := map[string]Document{}
	for _, d := range docs {
		bySource[d.Source] = d
	}
	if len(bySource) != len(info.Documents) {
		return Report{}, fmt.Errorf("корпус отличается от проиндексированного")
	}
	for _, d := range info.Documents {
		if bySource[d.Source].Hash != d.Hash {
			return Report{}, fmt.Errorf("корпус изменён: %s; перестройте индекс", d.Source)
		}
	}
	type anchor struct {
		source     string
		start, end int
	}
	anchors := make([][]anchor, len(questions))
	texts := make([]string, len(questions))
	ids := map[string]bool{}
	for i, q := range questions {
		if q.ID == "" || ids[q.ID] || strings.TrimSpace(q.Query) == "" || len(q.Query) > 8000 || len(q.Relevant) == 0 {
			return Report{}, fmt.Errorf("некорректный вопрос %q", q.ID)
		}
		ids[q.ID] = true
		texts[i] = q.Query
		for _, a := range q.Relevant {
			d, ok := bySource[a.Source]
			if !ok || strings.TrimSpace(a.Quote) == "" || strings.Count(d.Text, a.Quote) != 1 {
				return Report{}, fmt.Errorf("%s: цитата должна встречаться ровно один раз в %s", q.ID, a.Source)
			}
			start := utf8.RuneCountInString(d.Text[:strings.Index(d.Text, a.Quote)])
			anchors[i] = append(anchors[i], anchor{a.Source, start, start + utf8.RuneCountInString(a.Quote)})
		}
	}
	// The same query vector is reused for both strategies. Ground truth uses
	// source spans, never strategy-dependent chunk IDs or similarity scores.
	vectors, tokens, _, err := s.vectors(ctx, e, texts, nil)
	if err != nil {
		return Report{}, err
	}
	raw, _ := json.Marshal(questions)
	report := Report{Info: info, CreatedAt: time.Now().UTC(), K: k, QuestionsHash: digest(string(raw)), QueryEmbeddingTokens: tokens}
	for _, strategy := range []string{Fixed, Structured} {
		eval := Evaluation{Strategy: strategy}
		started := time.Now()
		for i, q := range questions {
			matches, err := s.Search(ctx, strategy, vectors[i], k)
			if err != nil {
				return Report{}, err
			}
			score := QueryScore{ID: q.ID, Query: q.Query, Matches: matches}
			covered := map[int]bool{}
			for rank, m := range matches {
				for n, a := range anchors[i] {
					if m.Chunk.Source == a.source && m.Chunk.Start <= a.start && m.Chunk.End >= a.end {
						covered[n] = true
						if !score.Hit {
							score.Hit = true
							score.ReciprocalRank = 1 / float64(rank+1)
						}
					}
				}
			}
			score.Recall = float64(len(covered)) / float64(len(anchors[i]))
			if score.Hit {
				eval.HitRate++
			}
			eval.MRR += score.ReciprocalRank
			eval.Recall += score.Recall
			eval.Queries = append(eval.Queries, score)
		}
		eval.SearchMS = time.Since(started).Milliseconds()
		n := float64(len(questions))
		eval.HitRate /= n
		eval.Recall /= n
		eval.MRR /= n
		report.Evaluations = append(report.Evaluations, eval)
	}
	raw, _ = json.Marshal(report)
	_, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO document_index_meta VALUES('report',?)", string(raw))
	return report, err
}
func (s *Store) Report() (Report, error) {
	var raw string
	err := s.db.QueryRow("SELECT value FROM document_index_meta WHERE key='report'").Scan(&raw)
	if err != nil {
		return Report{}, err
	}
	var report Report
	err = json.Unmarshal([]byte(raw), &report)
	return report, err
}
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Сравнение стратегий индексации\n\nПостроено: %s. Модель: `%s`, размерность: %d.\n\nКорпус: %d документов, %d слов, %d Unicode-символов. Условный объём при 400 словах на страницу: %.1f страниц (для кода это только эквивалент объёма).\n\nSHA-256 корпуса: `%s`.\n\nРазмер чанка: до %d Unicode-символов; перекрытие: %d. Это символы, не токены. Fixed пересекает границы разделов; structured сохраняет разделы Markdown и объявления Go, большие блоки делит по абзацам или лимиту. В обеих стратегиях границы файлов сохраняются.\n\n", r.Info.CreatedAt.Format(time.RFC3339), r.Info.Model, r.Info.Dimensions, len(r.Info.Documents), r.Info.Words, r.Info.Characters, r.Info.PagesAt400Words, r.Info.CorpusHash, r.Info.Config.Size, r.Info.Config.Overlap)
	b.WriteString("## Объём индекса\n\n| Стратегия | Чанки | Мин. / медиана / макс., симв. | Средний размер | Всего симв. с overlap | Пересекают разделы | Векторы, байт | Построение, мс | Токены API в этом запуске | Чанки из кэша |\n|---|---:|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, s := range r.Info.Strategies {
		fmt.Fprintf(&b, "| %s | %d | %d / %d / %d | %.1f | %d | %d | %d | %d | %d | %d |\n", s.Strategy, s.Chunks, s.Min, s.Median, s.Max, s.Mean, s.IndexedCharacters, s.CrossSectionChunks, s.VectorBytes, s.DurationMS, s.EmbeddingTokens, s.CacheHits)
	}
	fmt.Fprintf(&b, "\n## Качество поиска\n\nОдинаковые %d вопросов, одна модель, одинаковый k=%d и один вектор каждого вопроса для обеих стратегий. Эталон — заранее выбранная уникальная цитата в исходном файле. Попадание засчитывается, если чанк полностью содержит эту цитату. Несколько чанков с одной цитатой не увеличивают recall.\n\nHit@k — доля вопросов с хотя бы одним попаданием; Recall@k — средняя доля найденных эталонных фрагментов; MRR@k — средний обратный ранг первого попадания, 0 при отсутствии.\n\n| Стратегия | Hit@%d | Recall@%d | MRR@%d | Поиск всех вопросов, мс |\n|---|---:|---:|---:|---:|\n", len(r.Evaluations[0].Queries), r.K, r.K, r.K, r.K)
	for _, e := range r.Evaluations {
		fmt.Fprintf(&b, "| %s | %.1f%% | %.1f%% | %.3f | %d |\n", e.Strategy, e.HitRate*100, e.Recall*100, e.MRR, e.SearchMS)
	}
	b.WriteString("\n## Результаты по вопросам\n\n| Вопрос | Fixed: первый релевантный ранг | Structured: первый релевантный ранг |\n|---|---:|---:|\n")
	for i, q := range r.Evaluations[0].Queries {
		ranks := []string{"—", "—"}
		for j, e := range r.Evaluations {
			if e.Queries[i].ReciprocalRank > 0 {
				ranks[j] = fmt.Sprintf("%.0f", 1/e.Queries[i].ReciprocalRank)
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", strings.ReplaceAll(q.Query, "|", "\\|"), ranks[0], ranks[1])
	}
	a, z := r.Evaluations[0], r.Evaluations[1]
	winner := "Одинаковый Hit@k: сравните MRR и отдельные ошибки в таблице."
	if a.HitRate > z.HitRate {
		winner = "На этом наборе вопросов fixed чаще находит эталонный фрагмент в top-k."
	} else if z.HitRate > a.HitRate {
		winner = "На этом наборе вопросов structured чаще находит эталонный фрагмент в top-k."
	}
	fmt.Fprintf(&b, "\n## Выводы и ограничения\n\n%s Структурное разбиение по построению сохраняет границы разделов, но образует чанки разной длины, включая короткие. Fixed обеспечивает более равномерный размер и может терять целостность раздела. Победитель на небольшом ручном наборе не гарантирует преимущество на других корпусах.\n\nЭто оценка поиска, не качества генерируемых ответов. Несовпадение с выбранной цитатой может быть альтернативным релевантным ответом; полные top-k сохранены в JSON для ручного анализа. Корпус — исторический снимок проекта, включая устаревшие описания; индекс не проверяет истинность документов.\n\nВремя построения включает сеть и зависит от кэша; fixed строится первым, structured может повторно использовать совпадающие тексты. Поэтому время и оплаченные токены этого запуска не являются независимым сравнением скорости или полной стоимости стратегий. Время поиска не включает получение эмбеддингов вопросов. Токены эмбеддингов вопросов в этом запуске: %d.\n\nSHA-256 вопросов: `%s`. Полные метаданные, параметры и результаты находятся в comparison.json и SQLite.\n", winner, r.QueryEmbeddingTokens, r.QuestionsHash)
	return b.String()
}
