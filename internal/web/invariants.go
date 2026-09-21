package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/profile"
)

func (s *server) handleInvariants(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, 415, apiResponse{Error: "Ожидается Content-Type: application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var cmd memory.InvariantCommand
	if err := decoder.Decode(&cmd); err != nil {
		writeJSON(w, 400, apiResponse{Error: "Некорректный запрос инвариантов"})
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
	state, err := a.UpdateInvariants(cmd)
	if err != nil {
		status, message := 500, "Не удалось сохранить инварианты задачи"
		if errors.Is(err, agent.ErrInvariant) {
			status, message = 409, err.Error()
		}
		if errors.Is(err, memory.ErrInvalid) {
			status, message = 400, err.Error()
		}
		if errors.Is(err, memory.ErrConflict) || errors.Is(err, profile.ErrConflict) {
			status, message = 409, "Состояние задачи изменилось или действие недоступно. Обновите страницу."
		}
		writeJSON(w, status, apiResponse{Error: message})
		return
	}
	writeJSON(w, 200, apiResponse{Memory: &state, Messages: a.Messages()})
}
