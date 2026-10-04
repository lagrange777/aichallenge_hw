package web

import (
	"codex-chat-cli/internal/rag"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func (s *server) handleRAGReport(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}
	path := os.Getenv("RAG_REPORT_PATH")
	if path == "" {
		path = "artifacts/rag/comparison.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, 404, apiResponse{Error: "Отчёт ещё не создан. Запустите rag-eval."})
		return
	}
	var report rag.Report
	if json.Unmarshal(b, &report) != nil {
		writeJSON(w, 500, apiResponse{Error: "Не удалось прочитать RAG-отчёт"})
		return
	}
	writeJSON(w, 200, report)
}
func (s *server) handleRAGCompare(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) || !s.documentsReady(w) {
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, 415, apiResponse{Error: "Ожидается application/json"})
		return
	}
	var input struct {
		Query string `json:"query"`
		Model string `json:"model"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(input.Query) == "" || len(input.Query) > 8000 {
		writeJSON(w, 400, apiResponse{Error: "Введите вопрос: 1–8000 байт"})
		return
	}
	if input.Model == "" {
		input.Model = s.model
	}
	if !s.allowed[input.Model] {
		writeJSON(w, 400, apiResponse{Error: "Выберите модель из списка"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	pair, err := rag.Compare(ctx, s.llm, rag.Retriever{Documents: s.documents}, input.Model, rag.Question{Query: strings.TrimSpace(input.Query)})
	if err != nil {
		writeJSON(w, 502, apiResponse{Error: "Не удалось завершить сравнение: " + err.Error()})
		return
	}
	writeJSON(w, 200, pair)
}
