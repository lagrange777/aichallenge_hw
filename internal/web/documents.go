package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codex-chat-cli/internal/docindex"
)

func (s *server) documentsReady(w http.ResponseWriter) bool {
	if s.documents == nil || s.documents.Store == nil {
		writeJSON(w, 503, apiResponse{Error: "Индекс документов пока не построен. Запустите doc-index build и перезапустите сервер."})
		return false
	}
	return true
}
func (s *server) handleDocuments(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) || !s.documentsReady(w) {
		return
	}
	info, err := s.documents.Store.Info()
	if err != nil {
		writeJSON(w, 503, apiResponse{Error: "Не удалось прочитать индекс документов"})
		return
	}
	var report *docindex.Report
	value, err := s.documents.Store.Report()
	if err == nil {
		report = &value
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 500, apiResponse{Error: "Не удалось прочитать отчёт сравнения"})
		return
	}
	writeJSON(w, 200, map[string]any{"index": info, "report": report})
}
func (s *server) handleDocumentChunks(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) || !s.documentsReady(w) {
		return
	}
	strategy := r.URL.Query().Get("strategy")
	if strategy == "" {
		strategy = docindex.Fixed
	}
	offset := 0
	var err error
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
	}
	if err != nil || offset < 0 || (strategy != docindex.Fixed && strategy != docindex.Structured) {
		writeJSON(w, 400, apiResponse{Error: "Некорректная стратегия или страница"})
		return
	}
	chunks, err := s.documents.Store.Chunks(strategy, 20, offset)
	if err != nil {
		writeJSON(w, 500, apiResponse{Error: "Не удалось прочитать чанки"})
		return
	}
	writeJSON(w, 200, map[string]any{"chunks": chunks, "offset": offset})
}
func (s *server) handleDocumentSearch(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) || !s.documentsReady(w) {
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, 415, apiResponse{Error: "Ожидается application/json"})
		return
	}
	var input struct {
		Query string `json:"query"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(input.Query) == "" || len(input.Query) > 8000 {
		writeJSON(w, 400, apiResponse{Error: "Введите вопрос длиной до 8000 байт"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	result, err := s.documents.Search(ctx, input.Query, 5)
	if err != nil {
		writeJSON(w, 502, apiResponse{Error: "Поиск недоступен. Проверьте доступ к embedding-модели и соответствие индекса."})
		return
	}
	writeJSON(w, 200, result)
}
