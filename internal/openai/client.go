package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"codex-chat-cli/internal/chat"
)

const maxResponseBytes = 10 << 20

// Client calls the OpenAI Responses API.
type Client struct {
	apiKey       string
	model        string
	responsesURL string
	instructions string
	httpClient   *http.Client
}

type responseRequest struct {
	Model              string `json:"model"`
	Instructions       string `json:"instructions,omitempty"`
	Input              string `json:"input"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	Store              bool   `json:"store"`
}

type responseBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Error *apiError `json:"error"`
}

type errorEnvelope struct {
	Error *apiError `json:"error"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// NewClient validates dependencies and creates an API client.
func NewClient(apiKey, model, baseURL, instructions string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("API key is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("model is required")
	}
	if httpClient == nil {
		return nil, errors.New("HTTP client is required")
	}

	parsedURL, err := url.Parse(strings.TrimRight(baseURL, "/") + "/responses")
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid OpenAI base URL %q", baseURL)
	}

	return &Client{
		apiKey:       apiKey,
		model:        model,
		responsesURL: parsedURL.String(),
		instructions: instructions,
		httpClient:   httpClient,
	}, nil
}

// Respond sends one user turn. It returns the new response ID and output text.
func (c *Client) Respond(ctx context.Context, input, previousResponseID string, options chat.ResponseOptions) (string, string, error) {
	payload, err := json.Marshal(responseRequest{
		Model:              c.model,
		Instructions:       responseInstructions(c.instructions, options),
		Input:              input,
		PreviousResponseID: previousResponseID,
		Store:              true,
	})
	if err != nil {
		return "", "", fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL, bytes.NewReader(payload))
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-chat-cli/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("call OpenAI API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", "", fmt.Errorf("read OpenAI response: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", decodeAPIError(resp.StatusCode, body)
	}

	var decoded responseBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", "", fmt.Errorf("decode OpenAI response: %w", err)
	}
	if decoded.Error != nil {
		return "", "", fmt.Errorf("OpenAI API error: %s", decoded.Error.Message)
	}
	if decoded.ID == "" {
		return "", "", errors.New("OpenAI response does not contain an ID")
	}

	text := outputText(decoded)
	if text == "" {
		return "", "", fmt.Errorf("OpenAI response %s does not contain output text (status: %s)", decoded.ID, decoded.Status)
	}

	return decoded.ID, text, nil
}

func responseInstructions(base string, options chat.ResponseOptions) string {
	requirements := make([]string, 0, 3)
	if value := strings.TrimSpace(options.Format); value != "" {
		requirements = append(requirements, "Response format: "+value)
	}
	if value := strings.TrimSpace(options.LengthLimit); value != "" {
		requirements = append(requirements, "Response length limit: "+value)
	}
	if value := strings.TrimSpace(options.CompletionCondition); value != "" {
		requirements = append(requirements, "Completion condition: "+value)
	}
	if len(requirements) == 0 {
		return base
	}

	const heading = "The user specified the following requirements for this response. Every listed requirement is mandatory and must not be ignored:"
	block := heading + "\n- " + strings.Join(requirements, "\n- ")
	if strings.TrimSpace(base) == "" {
		return block
	}
	return strings.TrimSpace(base) + "\n\n" + block
}

func outputText(response responseBody) string {
	var parts []string
	for _, item := range response.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				parts = append(parts, content.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func decodeAPIError(statusCode int, body []byte) error {
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error != nil && envelope.Error.Message != "" {
		if envelope.Error.Code != "" {
			return fmt.Errorf("OpenAI API returned HTTP %d (%s): %s", statusCode, envelope.Error.Code, envelope.Error.Message)
		}
		return fmt.Errorf("OpenAI API returned HTTP %d: %s", statusCode, envelope.Error.Message)
	}

	message := strings.TrimSpace(string(body))
	if len(message) > 500 {
		message = message[:500] + "..."
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return fmt.Errorf("OpenAI API returned HTTP %d: %s", statusCode, message)
}
