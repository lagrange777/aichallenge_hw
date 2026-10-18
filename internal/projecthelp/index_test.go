package projecthelp

import (
	"codex-chat-cli/internal/mcpclient"
	"codex-chat-cli/internal/projectmcp"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func docsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "docs"), 0700)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# Project\n## Структура проекта\nТочка входа cmd/codex-chat, HTTP в internal/web.\n"), 0600)
	os.WriteFile(filepath.Join(root, "docs", "api.md"), []byte("# API\n## POST /api/chat\nmessage — обязательное поле JSON. Сервер возвращает answer и messages.\n"), 0600)
	os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET_SHOULD_NOT_APPEAR"), 0600)
	return root
}
func TestIndexScopedPersistedAndRefreshed(t *testing.T) {
	root := docsFixture(t)
	idx := &Index{Root: root, Path: filepath.Join(t.TempDir(), "index.json")}
	s, err := idx.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	raw, e := os.ReadFile(idx.Path)
	if e != nil || strings.Contains(string(raw), "SECRET_SHOULD_NOT_APPEAR") {
		t.Fatal("bad persisted index")
	}
	if s.Documents != 2 || len(Search(s, "Как вызвать POST /api/chat и передать message?")) == 0 {
		t.Fatal(s)
	}
	old := s.Hash
	os.WriteFile(filepath.Join(root, "docs", "api.md"), []byte("# Cache\nИстория обновлена и находится в Redis."), 0600)
	next, e := idx.Refresh()
	if e != nil || next.Hash == old {
		t.Fatal("stale docs", e)
	}
	if hits := Search(next, "Redis"); len(hits) != 1 || !strings.Contains(hits[0].Text, "Redis") {
		t.Fatal(hits)
	}
	if len(Search(next, "zxqvnotpresent987")) != 0 {
		t.Fatal("unrelated match")
	}
	var persisted Snapshot
	if json.Unmarshal(raw, &persisted) != nil || persisted.Hash != old {
		t.Fatal("invalid snapshot")
	}
}
func TestIndexRejectsSymlinksAndInvalidUTF8(t *testing.T) {
	for _, kind := range []string{"link-file", "link-dir", "utf8"} {
		t.Run(kind, func(t *testing.T) {
			root := docsFixture(t)
			outside := t.TempDir()
			os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0600)
			switch kind {
			case "link-file":
				os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "docs", "bad.md"))
			case "link-dir":
				os.Symlink(outside, filepath.Join(root, "docs", "bad"))
			case "utf8":
				os.WriteFile(filepath.Join(root, "docs", "bad.md"), []byte{255, 254}, 0600)
			}
			if _, e := (&Index{Root: root}).Refresh(); e == nil {
				t.Fatal("unsafe document accepted")
			}
		})
	}
}

func TestSearchPrefersDeploymentHeadingOverIncidentalDockerMention(t *testing.T) {
	root := docsFixture(t)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# Пример интеграции\n## Mock-сервис для чата\nДля примера поднимите Docker mock, затем откройте веб-чат в Docker. Это пример mock API, не инструкция запуска приложения.\n"), 0600)
	os.WriteFile(filepath.Join(root, "docs", "run.md"), []byte("# Разработка\n## Запуск в Docker\nСоздайте .env с OPENAI_API_KEY. Выполните docker compose up -d --build. Адрес веб-интерфейса: http://127.0.0.1:8080.\n"), 0600)
	s, err := (&Index{Root: root}).Refresh()
	if err != nil {
		t.Fatal(err)
	}
	hits := Search(s, "Как запустить проект в Docker и на каком адресе открыть веб-чат?")
	if len(hits) == 0 || hits[0].Source != "docs/run.md" {
		t.Fatalf("wrong deployment section: %+v", hits)
	}
}
func TestRetrieverRealMCPAndRevocation(t *testing.T) {
	root := docsFixture(t)
	if e := exec.Command("git", "init", "-b", "help-live", root).Run(); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(projectmcp.Handler(root))
	defer srv.Close()
	store, e := mcpclient.NewStore(filepath.Join(t.TempDir(), "mcp.json"))
	if e != nil {
		t.Fatal(e)
	}
	url := srv.URL + "/mcp"
	if e = store.EnsureConnection("project", url, []string{"git_branch", "project_files"}); e != nil {
		t.Fatal(e)
	}
	r := &Retriever{Index: &Index{Root: root}, Store: store, Endpoint: url}
	v, e := r.Retrieve(context.Background(), "Текущая ветка?")
	if e != nil || v.Warning != "" || !strings.Contains(v.Sources[len(v.Sources)-1].Text, "help-live") {
		t.Fatalf("%+v %v", v, e)
	}
	cs, _ := store.List()
	if e = store.SetPermissions(cs[0].ID, cs[0].Version, false, nil); e != nil {
		t.Fatal(e)
	}
	v, e = r.Retrieve(context.Background(), "POST /api/chat message")
	if e != nil || v.Warning == "" || len(v.Sources) == 0 {
		t.Fatal("docs fallback failed", e)
	}
	for _, s := range v.Sources {
		if strings.HasPrefix(s.Source, "mcp://") {
			t.Fatal("revoked MCP evidence leaked")
		}
	}
}
