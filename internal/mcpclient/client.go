// Package mcpclient connects to remote MCP servers to discover tools.
package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Discovery struct {
	ServerName      string      `json:"serverName"`
	ServerVersion   string      `json:"serverVersion"`
	ProtocolVersion string      `json:"protocolVersion"`
	Tools           []*mcp.Tool `json:"tools"`
}

func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(endpoint) > 2048 {
		return errors.New("Укажите URL MCP без логина, query-параметров и фрагмента")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("Используйте HTTPS; HTTP разрешён только для localhost")
	}
	return nil
}

type authTransport struct {
	token string
	base  http.RoundTripper
}

type limitedBody struct {
	io.Reader
	io.Closer
}

func (t authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, errors.New("Не удалось связаться с MCP-сервером")
	}
	if response.StatusCode >= 300 {
		response.Body.Close()
		return nil, fmt.Errorf("MCP-сервер вернул HTTP %d", response.StatusCode)
	}
	response.Body = limitedBody{io.LimitReader(response.Body, 4<<20), response.Body}
	return response, nil
}

// Discover performs MCP initialization and tools/list, including pagination.
// Remote error bodies are deliberately not exposed: they may echo credentials.
func Discover(ctx context.Context, endpoint, token string) (Discovery, error) {
	if err := ValidateEndpoint(endpoint); err != nil {
		return Discovery{}, err
	}
	if strings.ContainsAny(token, "\r\n") {
		return Discovery{}, errors.New("Некорректный ключ")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{
		Timeout:       30 * time.Second,
		Transport:     authTransport{token, transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "codex-chat-web", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return Discovery{}, connectionError(ctx, err, "Не удалось установить MCP-соединение")
	}
	defer session.Close()
	info := session.InitializeResult()
	if info == nil || info.ServerInfo == nil || info.Capabilities == nil {
		return Discovery{}, errors.New("Сервер вернул некорректный ответ инициализации MCP")
	}
	result := Discovery{ServerName: info.ServerInfo.Name, ServerVersion: info.ServerInfo.Version, ProtocolVersion: info.ProtocolVersion, Tools: []*mcp.Tool{}}
	if info.Capabilities.Tools == nil {
		return result, nil
	}
	params := &mcp.ListToolsParams{}
	cursors, names := map[string]bool{}, map[string]bool{}
	for page := 0; page < 100; page++ {
		list, err := session.ListTools(ctx, params)
		if err != nil {
			return Discovery{}, connectionError(ctx, err, "Соединение установлено, но список инструментов недоступен")
		}
		for _, tool := range list.Tools {
			if tool == nil || strings.TrimSpace(tool.Name) == "" || names[tool.Name] || tool.InputSchema == nil {
				return Discovery{}, errors.New("Сервер вернул некорректный список инструментов")
			}
			names[tool.Name] = true
			result.Tools = append(result.Tools, tool)
		}
		if len(result.Tools) > 2000 {
			return Discovery{}, errors.New("Сервер вернул слишком много инструментов (лимит 2000)")
		}
		if list.NextCursor == "" {
			return result, nil
		}
		if cursors[list.NextCursor] {
			return Discovery{}, errors.New("Сервер повторяет страницы инструментов")
		}
		cursors[list.NextCursor] = true
		params.Cursor = list.NextCursor
	}
	return Discovery{}, errors.New("Превышен лимит страниц инструментов")
}

func connectionError(ctx context.Context, err error, fallback string) error {
	if ctx.Err() != nil {
		return errors.New("Проверка отменена или сервер не ответил за 30 секунд")
	}
	// Only fixed status hints; never forward arbitrary server messages or URLs.
	for _, code := range []string{"401", "403", "404", "429"} {
		if strings.Contains(err.Error(), "HTTP "+code) {
			return fmt.Errorf("MCP: HTTP %s. Проверьте адрес, ключ и доступ к сервису", code)
		}
	}
	return errors.New(fallback + ". Проверьте URL, доступ к серверу и поддержку Streamable HTTP")
}
