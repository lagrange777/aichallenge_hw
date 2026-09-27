package scheduler

import (
	"codex-chat-cli/internal/mcpclient"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/jsonschema-go/jsonschema"
)

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}
type Catalog struct {
	Source SourceInfo `json:"source"`
	Tools  []Tool     `json:"tools"`
}
type Executor interface {
	Catalog(context.Context, string) (Catalog, error)
	Execute(context.Context, Job) (json.RawMessage, error)
}
type MCPExecutor struct{ Store *mcpclient.Store }

func (e *MCPExecutor) Catalog(ctx context.Context, id string) (Catalog, error) {
	list, err := e.Store.List()
	if err != nil {
		return Catalog{}, err
	}
	for _, c := range list {
		if c.ID != id {
			continue
		}
		_, token, err := e.Store.Get(id, c.Version)
		if err != nil {
			return Catalog{}, err
		}
		result, err := mcpclient.Discover(ctx, c.URL, token)
		if err != nil {
			return Catalog{}, err
		}
		out := Catalog{Source: SourceInfo{c.ID, c.Name, c.Version}, Tools: []Tool{}}
		for _, t := range result.Tools {
			if t.Annotations == nil || !t.Annotations.ReadOnlyHint {
				continue
			}
			b, err := json.Marshal(t.InputSchema)
			if err != nil {
				return Catalog{}, err
			}
			out.Tools = append(out.Tools, Tool{t.Name, t.Description, b})
		}
		return out, nil
	}
	return Catalog{}, errors.New("Выберите сохранённое MCP-подключение")
}
func validateArguments(c Catalog, spec Spec) error {
	for _, t := range c.Tools {
		if t.Name != spec.ToolName {
			continue
		}
		var schema jsonschema.Schema
		if json.Unmarshal(t.InputSchema, &schema) != nil {
			return errors.New("Некорректная схема инструмента")
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return errors.New("Схема инструмента не поддерживается")
		}
		if err = resolved.Validate(spec.Arguments); err != nil {
			return errors.New("Параметры не соответствуют inputSchema инструмента: проверьте обязательные поля и типы")
		}
		return nil
	}
	return errors.New("Инструмент недоступен или не помечен как readOnly")
}
func (e *MCPExecutor) Execute(ctx context.Context, j Job) (json.RawMessage, error) {
	r, err := mcpclient.ReadTool(ctx, e.Store, j.ConnectionID, j.ConnectionVersion, j.ToolName, j.Arguments)
	if errors.Is(err, mcpclient.ErrConflict) {
		return nil, errors.New("Подключение MCP изменилось. Отредактируйте и сохраните задание для подключения новой версии")
	}
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	// Preserve complete responses; never silently turn truncated JSON into source data.
	if len(b) > 32<<10 {
		return nil, errors.New("Ответ инструмента превышает 32 КиБ. Ограничьте выборку в параметрах задания")
	}
	return b, nil
}
