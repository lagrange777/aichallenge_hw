package docindex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultModel = "text-embedding-3-small"
const DefaultDimensions = 1536

type EmbeddingBatch struct {
	Vectors [][]float32
	Tokens  int
}
type Embedder interface {
	Embed(context.Context, []string) (EmbeddingBatch, error)
	Model() string
	Dimensions() int
}
type OpenAIEmbedder struct {
	key, endpoint, model string
	dimensions           int
	client               *http.Client
}

func NewOpenAIEmbedder(key, baseURL, model string, dimensions int) (*OpenAIEmbedder, error) {
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("для эмбеддингов требуется OPENAI_API_KEY")
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return nil, fmt.Errorf("некорректный OPENAI_BASE_URL (HTTPS или HTTP loopback)")
	}
	if model == "" {
		model = DefaultModel
	}
	if dimensions < 1 || dimensions > 3072 {
		return nil, fmt.Errorf("размерность: 1–3072")
	}
	return &OpenAIEmbedder{key: key, endpoint: strings.TrimRight(baseURL, "/") + "/embeddings", model: model, dimensions: dimensions, client: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (e *OpenAIEmbedder) Model() string   { return e.model }
func (e *OpenAIEmbedder) Dimensions() int { return e.dimensions }
func normalize(v []float32, dimensions int) error {
	if len(v) != dimensions {
		return fmt.Errorf("ожидалось %d координат, получено %d", dimensions, len(v))
	}
	var norm float64
	for _, f := range v {
		x := float64(f)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Errorf("неконечная координата")
		}
		norm += x * x
	}
	if norm == 0 {
		return fmt.Errorf("нулевой вектор")
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return nil
}
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) (EmbeddingBatch, error) {
	if len(texts) == 0 || len(texts) > 32 {
		return EmbeddingBatch{}, fmt.Errorf("пакет: 1–32 текста")
	}
	for _, s := range texts {
		if strings.TrimSpace(s) == "" || len(s) > 8000 {
			return EmbeddingBatch{}, fmt.Errorf("текст должен быть непустым и не больше 8000 байт UTF-8")
		}
	}
	body, _ := json.Marshal(map[string]any{"model": e.model, "dimensions": e.dimensions, "input": texts, "encoding_format": "float"})
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return EmbeddingBatch{}, ctx.Err()
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
		if err != nil {
			return EmbeddingBatch{}, fmt.Errorf("не удалось создать запрос эмбеддингов")
		}
		req.Header.Set("Authorization", "Bearer "+e.key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return EmbeddingBatch{}, ctx.Err()
			}
			return EmbeddingBatch{}, fmt.Errorf("сеть эмбеддингов недоступна")
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		resp.Body.Close()
		if (resp.StatusCode == 429 || resp.StatusCode >= 500) && attempt < 2 {
			continue
		}
		if resp.StatusCode != 200 {
			return EmbeddingBatch{}, fmt.Errorf("OpenAI embeddings: HTTP %d; проверьте ключ, доступ к модели и квоту", resp.StatusCode)
		}
		if readErr != nil || len(b) > 8<<20 {
			return EmbeddingBatch{}, fmt.Errorf("некорректный размер ответа эмбеддингов")
		}
		var data struct {
			Model string `json:"model"`
			Data  []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
			Usage struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(b, &data) != nil || len(data.Data) != len(texts) || data.Model != e.model || data.Usage.TotalTokens < 0 {
			return EmbeddingBatch{}, fmt.Errorf("некорректный ответ эмбеддингов или другая модель")
		}
		vectors := make([][]float32, len(texts))
		for _, item := range data.Data {
			if item.Index < 0 || item.Index >= len(texts) || vectors[item.Index] != nil {
				return EmbeddingBatch{}, fmt.Errorf("некорректный порядок эмбеддингов")
			}
			if err := normalize(item.Embedding, e.dimensions); err != nil {
				return EmbeddingBatch{}, err
			}
			vectors[item.Index] = item.Embedding
		}
		return EmbeddingBatch{Vectors: vectors, Tokens: data.Usage.TotalTokens}, nil
	}
	return EmbeddingBatch{}, fmt.Errorf("не удалось получить эмбеддинги")
}
