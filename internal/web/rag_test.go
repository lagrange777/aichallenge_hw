package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRAGAPIsGuardMethodsOriginAndUnavailableIndex(t *testing.T) {
	h := NewHandler(&fakeLLM{}, "test-model", nil)
	for _, tc := range []struct {
		method, path, origin, header string
		want                         int
	}{
		{"GET", "/api/rag/compare", "", "1", 405},
		{"POST", "/api/rag/compare", "https://evil.example", "1", 403},
		{"POST", "/api/rag/compare", "", "", 403},
		{"POST", "/api/rag/compare", "", "1", 503},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"query":"q"}`))
		r.Header.Set("X-Codex-Chat", tc.header)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	w := performChatBody(h, nil, `{"message":"question","rag":true}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("missing RAG accepted: %d", w.Code)
	}
}
