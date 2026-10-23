package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ChatMessage is one text turn of a conversation.
type ChatMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// ChatToolCall is a read-only tool the AI asked the backend to run.
type ChatToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ChatToolResult is an executed call with its result.
type ChatToolResult struct {
	ChatToolCall
	Result any `json:"result"`
}

// ChatRequest is one step of the chat loop: the conversation plus every
// tool call already executed for the current user message.
type ChatRequest struct {
	Messages    []ChatMessage    `json:"messages"`
	ToolResults []ChatToolResult `json:"toolResults,omitempty"`
	ForceAnswer bool             `json:"forceAnswer,omitempty"`
}

// ChatResponse is either more tool calls (Type "tool_calls") or the
// final answer (Type "answer"; Mode "llm" or "rules").
type ChatResponse struct {
	Type      string         `json:"type"`
	ToolCalls []ChatToolCall `json:"toolCalls,omitempty"`
	Content   string         `json:"content,omitempty"`
	Mode      string         `json:"mode,omitempty"`
}

// Chat runs one step against POST /ai/chat. No retries: the caller's
// loop is interactive and a failed step is reported to the user.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ai: marshaling chat request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/ai/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: building chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: chat request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("ai: reading chat response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ai: chat returned status %d: %s", resp.StatusCode, truncateBody(respBody))
	}
	var out ChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("ai: parsing chat response: %w", err)
	}
	return &out, nil
}

func truncateBody(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}
