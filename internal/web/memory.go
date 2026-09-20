package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/memory"
)

func (s *server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, apiResponse{Error: "Метод не поддерживается"})
		return
	}
	a, err := s.agentFor(w, r)
	if err != nil {
		writeJSON(w, 500, apiResponse{Error: "Не удалось загрузить сессию"})
		return
	}
	state, err := a.Memories()
	if err != nil {
		writeJSON(w, 500, apiResponse{Error: "Не удалось загрузить память"})
		return
	}
	writeJSON(w, 200, apiResponse{Memory: &state})
}

type memoryRequest struct {
	ID     string       `json:"id"`
	TaskID string       `json:"taskId"`
	Action string       `json:"action"`
	Layer  memory.Layer `json:"layer"`
	Key    string       `json:"key"`
	Value  string       `json:"value"`
	Name   string       `json:"name"`
}

func (s *server) handleMemoryMutation(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, 415, apiResponse{Error: "Ожидается Content-Type: application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request memoryRequest
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, 400, apiResponse{Error: "Некорректный запрос памяти"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, 400, apiResponse{Error: "Ожидается один JSON-объект"})
		return
	}
	a, err := s.agentFor(w, r)
	if err != nil {
		writeJSON(w, 500, apiResponse{Error: "Не удалось загрузить сессию"})
		return
	}
	var state agent.MemoryView
	switch r.URL.Path {
	case "/api/memory/review":
		state, err = a.ReviewMemory(request.ID, request.TaskID, request.Action, request.Layer, request.Key, request.Value)
	case "/api/memory/edit":
		state, err = a.EditMemory(request.TaskID, request.ID, request.Layer, request.Key, request.Value, false)
	case "/api/memory/delete":
		state, err = a.EditMemory(request.TaskID, request.ID, request.Layer, "", "", true)
	case "/api/tasks/new":
		state, err = a.NewTask(request.TaskID, request.Name)
	}
	if err != nil {
		status, message := 500, "Не удалось сохранить изменения памяти. Обновите страницу."
		if errors.Is(err, memory.ErrInvalid) {
			status, message = 400, "Проверьте слой, ключ и текст записи. Ключ — до 100 символов, текст — до 2000 символов; не более 100 записей в слое."
		}
		if errors.Is(err, memory.ErrConflict) {
			status, message = 409, "Задача или предложение уже изменились. Обновите память и повторите действие."
		}
		writeJSON(w, status, apiResponse{Error: message})
		return
	}
	writeJSON(w, 200, apiResponse{Memory: &state, Messages: a.Messages(), Context: snapshotPointer(a.Snapshot())})
}
