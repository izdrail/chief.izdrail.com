// Package ollama provides an HTTP client for the Ollama API with streaming
// chat completions and tool-use (function calling) support.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	// DefaultBaseURL is the default Ollama API base URL.
	DefaultBaseURL = "https://ai.izdrail.com"
	// DefaultModel is the default model to use.
	DefaultModel = "codellama:7b"
	// DefaultTimeout is the default HTTP client timeout.
	DefaultTimeout = 10 * time.Minute
)

// Client is an Ollama API client.
type Client struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

// NewClient creates a new Ollama client with defaults.
// The base URL and model can be overridden via OLLAMA_HOST and OLLAMA_MODEL env vars.
func NewClient() *Client {
	baseURL := DefaultBaseURL
	if v := os.Getenv("OLLAMA_HOST"); v != "" {
		baseURL = v
	}
	model := DefaultModel
	if v := os.Getenv("OLLAMA_MODEL"); v != "" {
		model = v
	}
	return &Client{
		BaseURL: baseURL,
		Model:   model,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// Message represents a chat message.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Name      string     `json:"name,omitempty"`
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and arguments.
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Tool defines a tool available to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a tool's name, description, and parameters.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatRequest is the request body for /api/chat.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
	Options  *Options  `json:"options,omitempty"`
}

// Options holds model generation options.
type Options struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumCtx      int     `json:"num_ctx,omitempty"`
}

// ChatResponse is a single streaming chunk from /api/chat.
type ChatResponse struct {
	Model     string  `json:"model"`
	Message   Message `json:"message"`
	Done      bool    `json:"done"`
	DoneReason string `json:"done_reason,omitempty"`
}

// StreamEvent is emitted during streaming.
type StreamEvent struct {
	// TextDelta is a partial text chunk from the assistant.
	TextDelta string
	// ToolCalls is set when the model requests tool invocations.
	ToolCalls []ToolCall
	// Done is true when the stream is complete.
	Done bool
	// Error is set if streaming encountered an error.
	Error error
}

// ChatStream sends a chat request and streams the response, emitting events on the returned channel.
// The channel is closed when streaming completes or an error occurs.
func (c *Client) ChatStream(ctx context.Context, req ChatRequest) <-chan StreamEvent {
	ch := make(chan StreamEvent, 32)
	req.Stream = true

	go func() {
		defer close(ch)

		body, err := json.Marshal(req)
		if err != nil {
			ch <- StreamEvent{Error: fmt.Errorf("marshal request: %w", err)}
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.BaseURL+"/api/chat", bytes.NewReader(body))
		if err != nil {
			ch <- StreamEvent{Error: fmt.Errorf("create request: %w", err)}
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.HTTPClient.Do(httpReq)
		if err != nil {
			ch <- StreamEvent{Error: fmt.Errorf("http request: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ch <- StreamEvent{Error: fmt.Errorf("ollama API error %d: %s", resp.StatusCode, string(body))}
			return
		}

		// Accumulate tool calls across chunks (Ollama may split them)
		var accumulatedToolCalls []ToolCall

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var chunk ChatResponse
			if err := json.Unmarshal(line, &chunk); err != nil {
				ch <- StreamEvent{Error: fmt.Errorf("parse chunk: %w", err)}
				return
			}

			// Collect tool calls
			if len(chunk.Message.ToolCalls) > 0 {
				accumulatedToolCalls = append(accumulatedToolCalls, chunk.Message.ToolCalls...)
			}

			// Emit text delta
			if chunk.Message.Content != "" {
				ch <- StreamEvent{TextDelta: chunk.Message.Content}
			}

			if chunk.Done {
				if len(accumulatedToolCalls) > 0 {
					ch <- StreamEvent{ToolCalls: accumulatedToolCalls}
				}
				ch <- StreamEvent{Done: true}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamEvent{Error: fmt.Errorf("read stream: %w", err)}
		}
	}()

	return ch
}

// Chat sends a non-streaming chat request and returns the complete response message.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*Message, error) {
	req.Stream = false

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama API error %d: %s", resp.StatusCode, string(b))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &chatResp.Message, nil
}
