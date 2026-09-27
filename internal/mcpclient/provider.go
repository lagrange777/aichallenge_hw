package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"codex-chat-cli/internal/agent"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Provider struct{ store *Store }

func NewProvider(s *Store) *Provider { return &Provider{store: s} }
func (p *Provider) Tools(ctx context.Context) ([]agent.ToolDefinition, []agent.ToolRun) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connections, err := p.store.List()
	if err != nil {
		return nil, []agent.ToolRun{{Name: "tools/list", Output: "Не удалось прочитать настройки MCP", Error: true}}
	}
	var defs []agent.ToolDefinition
	var runs []agent.ToolRun
	for _, c := range connections {
		if !c.Enabled {
			continue
		}
		_, token, e := p.store.Get(c.ID, c.Version)
		var d Discovery
		if e == nil {
			d, e = Discover(ctx, c.URL, token)
		}
		if e != nil {
			runs = append(runs, agent.ToolRun{ServerName: c.Name, Name: "tools/list", Error: true, Output: "Не удалось получить инструменты MCP. Проверьте подключение."})
			continue
		}
		matched := map[string]bool{}
		for _, t := range d.Tools {
			if !slices.Contains(c.AllowedTools, t.Name) {
				continue
			}
			if t.Annotations == nil || !t.Annotations.ReadOnlyHint {
				continue
			}
			schema, e := json.Marshal(t.InputSchema)
			if e != nil || len(schema) > 32<<10 {
				continue
			}
			var s jsonschema.Schema
			if json.Unmarshal(schema, &s) != nil {
				continue
			}
			if _, e = s.Resolve(nil); e != nil {
				continue
			}
			matched[t.Name] = true
			hash := sha256.Sum256([]byte(c.ID + ":" + t.Name))
			defs = append(defs, agent.ToolDefinition{Name: fmt.Sprintf("mcp_%x", hash[:16]), ServerID: c.ID, ServerName: c.Name, ToolName: t.Name, Version: c.Version, Description: c.Name + " / " + t.Name + ": " + t.Description, Parameters: schema})
			if len(defs) >= 64 {
				return defs, runs
			}
		}
		for _, name := range c.AllowedTools {
			if !matched[name] {
				runs = append(runs, agent.ToolRun{ServerName: c.Name, Name: name, Error: true, Output: "Выбранный инструмент недоступен: отсутствует, не имеет readOnlyHint или содержит неподдерживаемую схему"})
			}
		}
	}
	return defs, runs
}
func (p *Provider) Call(ctx context.Context, def agent.ToolDefinition, arguments string) (string, error) {
	c, token, err := p.store.Get(def.ServerID, def.Version)
	if err != nil || !c.Enabled || !slices.Contains(c.AllowedTools, def.ToolName) {
		return "", errors.New("Разрешение на инструмент отозвано или настройки изменились")
	}
	var args map[string]any
	if len(arguments) > 16<<10 || json.Unmarshal([]byte(arguments), &args) != nil || args == nil {
		return "", errors.New("Аргументы должны быть JSON-объектом")
	}
	var schema jsonschema.Schema
	if json.Unmarshal(def.Parameters, &schema) != nil {
		return "", errors.New("Некорректная схема инструмента")
	}
	resolved, err := schema.Resolve(nil)
	if err != nil || resolved.Validate(args) != nil {
		return "", errors.New("Аргументы не соответствуют схеме инструмента")
	}
	session, closeSession, err := connect(ctx, c.URL, token)
	if err != nil {
		return "", err
	}
	defer closeSession()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: def.ToolName, Arguments: args})
	if err != nil {
		return "", connectionError(ctx, err, "Не удалось вызвать MCP-инструмент")
	}
	raw, err := json.Marshal(result)
	if err != nil || result == nil {
		return "", errors.New("MCP вернул некорректный результат")
	}
	output := string(raw)
	if token != "" {
		output = strings.ReplaceAll(output, token, "[REDACTED]")
	}
	if len(output) > 32<<10 {
		output = output[:32<<10]
		for !utf8.ValidString(output) {
			output = output[:len(output)-1]
		}
		output += "\n[Результат обрезан до 32 КиБ]"
	}
	if result.IsError {
		return "", fmt.Errorf("Ошибка MCP-инструмента: %s", output)
	}
	return output, nil
}
