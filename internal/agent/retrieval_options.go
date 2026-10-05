package agent

import "fmt"

// RetrievalOptions are per-turn settings; they never mutate the shared retriever.
type RetrievalOptions struct {
	Grounded           bool   `json:"grounded,omitempty"`
	Mode               string `json:"mode"`
	TopKBefore         int    `json:"topKBefore"`
	TopKAfter          int    `json:"topKAfter"`
	RelevanceThreshold int    `json:"relevanceThreshold"`
}

func DefaultRetrievalOptions() RetrievalOptions {
	return RetrievalOptions{Mode: "baseline", TopKBefore: 20, TopKAfter: 5, RelevanceThreshold: 2}
}
func (o RetrievalOptions) Validate() error {
	if o.Grounded && ((o.Mode != "filter" && o.Mode != "full") || o.RelevanceThreshold < 2) {
		return fmt.Errorf("строгий RAG требует фильтр и порог не ниже 2/3")
	}
	if o.Mode != "baseline" && o.Mode != "filter" && o.Mode != "rewrite" && o.Mode != "full" {
		return fmt.Errorf("режим RAG: baseline, filter, rewrite или full")
	}
	if o.TopKBefore < 1 || o.TopKBefore > 20 || o.TopKAfter < 1 || o.TopKAfter > 10 || o.TopKAfter > o.TopKBefore {
		return fmt.Errorf("top-K до: 1–20; после: 1–10, не больше K до")
	}
	if o.RelevanceThreshold < 0 || o.RelevanceThreshold > 3 {
		return fmt.Errorf("порог релевантности: целое число 0–3")
	}
	return nil
}

type RetrievalStep struct {
	Attempts   int    `json:"attempts,omitempty"`
	Warning    string `json:"warning,omitempty"`
	Name       string `json:"name"`
	Model      string `json:"model"`
	Usage      Usage  `json:"usage"`
	DurationMS int64  `json:"durationMs"`
}
type Candidate struct {
	Source
	Rank         int     `json:"rank"`
	OriginalRank int     `json:"originalRank,omitempty"`
	RewriteRank  int     `json:"rewriteRank,omitempty"`
	FusionScore  float64 `json:"fusionScore,omitempty"`
	Evaluated    bool    `json:"evaluated"`
	Relevance    int     `json:"relevance"`
	Reason       string  `json:"reason,omitempty"`
	Evidence     string  `json:"evidence,omitempty"`
	Decision     string  `json:"decision"`
}

const NoRAGEvidence = "В найденных документах недостаточно информации для ответа на этот вопрос. Попробуйте уточнить вопрос или добавить подходящие источники."

func (r *Retrieval) AuxiliaryUsage() Usage {
	var usage Usage
	if r != nil {
		for _, s := range r.Steps {
			usage = sumUsage(usage, s.Usage)
		}
	}
	return usage
}
