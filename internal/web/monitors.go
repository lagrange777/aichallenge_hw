package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"codex-chat-cli/internal/scheduler"
)

func (s *server) handleMonitors(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}
	if s.monitors == nil {
		writeJSON(w, 503, apiResponse{Error: "Планировщик не подключён"})
		return
	}
	if connection := r.URL.Query().Get("connectionId"); connection != "" {
		if s.monitors.Executor == nil {
			writeJSON(w, 503, apiResponse{Error: "MCP-исполнитель не подключён"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		catalog, err := s.monitors.Executor.Catalog(ctx, connection)
		if err != nil {
			monitorError(w, err)
			return
		}
		writeJSON(w, 200, catalog)
		return
	}
	if id := r.URL.Query().Get("id"); id != "" {
		history, err := s.monitors.History(id)
		if err != nil {
			monitorError(w, err)
			return
		}
		writeJSON(w, 200, history)
		return
	}
	jobs, err := s.monitors.Store.List()
	if err != nil {
		monitorError(w, err)
		return
	}
	allow, err := s.monitors.Store.AllowChat()
	if err != nil {
		monitorError(w, err)
		return
	}
	sources := []scheduler.SourceInfo{}
	if s.mcpStore != nil {
		list, e := s.mcpStore.List()
		if e != nil {
			monitorError(w, e)
			return
		}
		for _, c := range list {
			sources = append(sources, scheduler.SourceInfo{ID: c.ID, Name: c.Name, Version: c.Version})
		}
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs, "sources": sources, "allowChatChanges": allow})
}
func (s *server) handleMonitorMutation(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	if s.monitors == nil {
		writeJSON(w, 503, apiResponse{Error: "Планировщик не подключён"})
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, 415, apiResponse{Error: "Ожидается application/json"})
		return
	}
	var input struct {
		scheduler.Spec
		ID      string `json:"id"`
		Version int    `json:"version"`
		Paused  bool   `json:"paused"`
		Enabled bool   `json:"enabled"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeJSON(w, 400, apiResponse{Error: "Некорректные параметры задания"})
		return
	}
	if d.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, apiResponse{Error: "Ожидается один JSON-объект"})
		return
	}
	var err error
	var result any
	switch r.URL.Path {
	case "/api/monitors/create":
		result, err = s.monitors.Create(r.Context(), input.Spec)
	case "/api/monitors/update":
		result, err = s.monitors.Update(r.Context(), input.ID, input.Version, input.Spec)
	case "/api/monitors/preview":
		result, err = s.monitors.Preview(r.Context(), input.Spec)
	case "/api/monitors/run":
		result, err = s.monitors.Store.RunOnce(input.ID, input.Version)
	case "/api/monitors/delete":
		err = s.monitors.Store.Delete(input.ID, input.Version)
		result = map[string]bool{"deleted": err == nil}
	case "/api/monitors/pause":
		result, err = s.monitors.Store.Pause(input.ID, input.Version, input.Paused, time.Now().Unix())
	case "/api/monitors/settings":
		err = s.monitors.Store.SetAllowChat(input.Enabled)
		result = map[string]bool{"allowChatChanges": input.Enabled}
	}
	if err != nil {
		monitorError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func monitorError(w http.ResponseWriter, err error) {
	status := 400
	message := err.Error()
	if errors.Is(err, scheduler.ErrConflict) {
		status = 409
	}
	if errors.Is(err, sql.ErrNoRows) {
		status = 404
		message = "Задание не найдено"
	}
	var codeError interface{ Code() int }
	var wrapped interface{ Unwrap() error }
	if errors.As(err, &codeError) || errors.As(err, &wrapped) {
		status = 500
		message = "Не удалось обработать задание"
	}
	writeJSON(w, status, apiResponse{Error: message})
}
