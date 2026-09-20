package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/profile"
)

func (s *server) handleProfiles(w http.ResponseWriter, r *http.Request) {
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
	view, err := a.Profiles()
	if err != nil {
		writeJSON(w, 500, apiResponse{Error: "Не удалось загрузить профили"})
		return
	}
	writeJSON(w, 200, apiResponse{Profiles: &view})
}

func (s *server) handleProfileMutation(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, 415, apiResponse{Error: "Ожидается Content-Type: application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var request struct {
		ActiveID string          `json:"activeId"`
		ID       string          `json:"id"`
		Profile  profile.Profile `json:"profile"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, 400, apiResponse{Error: "Некорректный профиль"})
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
	var view agent.ProfileView
	if r.URL.Path == "/api/profiles/switch" {
		view, err = a.SwitchProfile(request.ActiveID, request.ID)
	} else {
		view, err = a.SaveProfile(request.ActiveID, request.Profile, r.URL.Path == "/api/profiles/new")
	}
	if err != nil {
		status, message := 500, "Не удалось сохранить профиль. Обновите страницу и повторите."
		if errors.Is(err, profile.ErrInvalid) {
			status, message = 400, "Проверьте настройки: имя до 100 символов, описание до 1000, ограничения до 2000."
		}
		if errors.Is(err, profile.ErrConflict) {
			status, message = 409, "Профиль изменился или недоступен. Обновите страницу."
		}
		writeJSON(w, status, apiResponse{Error: message})
		return
	}
	writeJSON(w, 200, apiResponse{Profiles: &view})
}
