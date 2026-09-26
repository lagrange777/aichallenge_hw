package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"codex-chat-cli/internal/mcpclient"
)

type mcpView struct {
	Connections  []mcpclient.Connection `json:"connections"`
	ConnectionID string                 `json:"connectionId,omitempty"`
	Discovery    *mcpclient.Discovery   `json:"discovery,omitempty"`
}

func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodGet) {
		return
	}
	s.writeMCP(w, nil)
}
func (s *server) writeMCP(w http.ResponseWriter, checked *mcpView) {
	if s.mcpStore == nil {
		writeJSON(w, 503, apiResponse{Error: "Хранилище MCP не подключено"})
		return
	}
	list, err := s.mcpStore.List()
	if err != nil {
		writeJSON(w, 500, apiResponse{Error: "Не удалось прочитать настройки MCP"})
		return
	}
	if checked == nil {
		checked = &mcpView{}
	}
	checked.Connections = list
	writeJSON(w, 200, apiResponse{MCP: checked})
}
func (s *server) handleMCPMutation(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	if s.mcpStore == nil {
		writeJSON(w, 503, apiResponse{Error: "Хранилище MCP не подключено"})
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, 415, apiResponse{Error: "Ожидается application/json"})
		return
	}
	var request struct {
		ID         string `json:"id"`
		Version    int    `json:"version"`
		Name       string `json:"name"`
		URL        string `json:"url"`
		Token      string `json:"token"`
		ClearToken bool   `json:"clearToken"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, 400, apiResponse{Error: "Некорректные настройки MCP"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, 400, apiResponse{Error: "Ожидается один JSON-объект"})
		return
	}
	var err error
	view := &mcpView{}
	switch r.URL.Path {
	case "/api/mcp/save":
		_, err = s.mcpStore.Save(mcpclient.Connection{ID: request.ID, Name: request.Name, URL: request.URL, Version: request.Version}, request.Token, request.ClearToken)
	case "/api/mcp/delete":
		err = s.mcpStore.Delete(request.ID, request.Version)
	case "/api/mcp/check":
		var c mcpclient.Connection
		var token string
		c, token, err = s.mcpStore.Get(request.ID, request.Version)
		if err == nil {
			var result mcpclient.Discovery
			result, err = mcpclient.Discover(r.Context(), c.URL, token)
			if err != nil {
				writeJSON(w, 502, apiResponse{Error: err.Error()})
				return
			}
			// A result for an edited/deleted connection must never look current.
			_, _, err = s.mcpStore.Get(request.ID, request.Version)
			view.ConnectionID, view.Discovery = c.ID, &result
		}
	}
	if err != nil {
		if errors.Is(err, mcpclient.ErrConflict) {
			writeJSON(w, 409, apiResponse{Error: err.Error()})
			return
		}
		// Validation messages are safe; filesystem errors may contain private paths.
		var pathError interface{ Unwrap() error }
		if errors.As(err, &pathError) {
			writeJSON(w, 500, apiResponse{Error: "Не удалось сохранить настройки MCP"})
			return
		}
		writeJSON(w, 400, apiResponse{Error: err.Error()})
		return
	}
	s.writeMCP(w, view)
}
