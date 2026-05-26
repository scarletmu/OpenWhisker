package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAI Chat Completions function-calling wire types. Kept separate from the
// existing `openAIResponseRequest` because we need full visibility into role,
// tool_calls, and tool_call_id — fields the single-turn Responses helper hides.

// ChatMessage is one OpenAI Chat Completions message. Role is one of
// "system" / "user" / "assistant" / "tool". When the assistant emits a
// tool_call, the next request must include a matching {Role: "tool",
// ToolCallID: ..., Content: <stringified result>} message.
type ChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
}

// ChatToolCall describes one function invocation requested by the model.
// Arguments is a JSON-encoded string (per the OpenAI protocol).
type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatToolFunction `json:"function"`
}

type ChatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatTool is one entry of the tools catalog presented to the model.
type ChatTool struct {
	Type     string           `json:"type"`
	Function ChatToolFunction2 `json:"function"`
}

type ChatToolFunction2 struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ChatCompletionRequest is the subset of the OpenAI Chat Completions request
// body we need for tool-calling. Additional fields can be added later (e.g.
// temperature, response_format) without breaking callers.
type ChatCompletionRequest struct {
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	Tools      []ChatTool    `json:"tools,omitempty"`
	ToolChoice any           `json:"tool_choice,omitempty"`
	MaxTokens  int           `json:"max_tokens,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
}

// ChatCompletionResponse is the subset we consume.
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ChatCompletionClient is the minimal interface ToolCallingEngine depends on.
// OpenAIClient implements it via CreateChatCompletion; tests can supply a
// fake without touching the network.
type ChatCompletionClient interface {
	CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error)
}

// CreateChatCompletion sends a Chat Completions request with full tool-calling
// support. The OpenAI / DeepSeek / openai-compatible endpoints all expose this
// shape; the response is returned essentially unmodified so the engine can
// reason about finish_reason, tool_calls, and the assistant content stream.
func (c OpenAIClient) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return ChatCompletionResponse{}, errors.New("openai-compatible api key is required")
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = c.model()
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = c.maxOutput()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return ChatCompletionResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL(), "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Organization != "" {
		httpReq.Header.Set("OpenAI-Organization", c.Organization)
	}
	if c.Project != "" {
		httpReq.Header.Set("OpenAI-Project", c.Project)
	}
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return ChatCompletionResponse{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ChatCompletionResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatCompletionResponse{}, fmt.Errorf("chat completions api returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var decoded ChatCompletionResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return ChatCompletionResponse{}, fmt.Errorf("decode chat completion response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return ChatCompletionResponse{}, errors.New(decoded.Error.Message)
	}
	return decoded, nil
}
