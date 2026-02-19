package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/izdrail/chief/internal/ollama"
	"github.com/izdrail/chief/internal/tools"
)

// AgentOptions configures the agent's behavior.
type AgentOptions struct {
	MaxToolRounds int
	WorkDir       string
}

// AgentEvent represents a streaming event from the agent.
type AgentEvent struct {
	TextDelta  string
	ToolName   string
	ToolInput  map[string]interface{}
	ToolResult string
	Error      error
	Done       bool
}

// RunAgent drives the agentic loop: sending messages to Ollama,
// executing tools as requested, and feeding results back.
func RunAgent(
	ctx context.Context,
	client *ollama.Client,
	messages []ollama.Message,
	opts AgentOptions,
) <-chan AgentEvent {
	ch := make(chan AgentEvent, 64)

	if opts.MaxToolRounds == 0 {
		opts.MaxToolRounds = 50
	}

	go func() {
		defer close(ch)

		toolDefs := tools.Definitions()

		for round := 0; round < opts.MaxToolRounds; round++ {
			// Check context
			if ctx.Err() != nil {
				ch <- AgentEvent{Error: ctx.Err()}
				return
			}

			req := ollama.ChatRequest{
				Model:    client.Model,
				Messages: messages,
				Tools:    toolDefs,
				Stream:   true,
				Options: &ollama.Options{
					NumCtx: 32768,
				},
			}

			// Collect the full assistant response
			var textBuilder strings.Builder
			var toolCalls []ollama.ToolCall
			var streamErr error

			stream := client.ChatStream(ctx, req)
			for event := range stream {
				if event.Error != nil {
					streamErr = event.Error
					break
				}
				if event.TextDelta != "" {
					textBuilder.WriteString(event.TextDelta)
					ch <- AgentEvent{TextDelta: event.TextDelta}
				}
				if len(event.ToolCalls) > 0 {
					toolCalls = append(toolCalls, event.ToolCalls...)
				}
			}

			if streamErr != nil {
				ch <- AgentEvent{Error: fmt.Errorf("stream error: %w", streamErr)}
				return
			}

			assistantText := textBuilder.String()

			// Add assistant message to history
			assistantMsg := ollama.Message{
				Role:      "assistant",
				Content:   assistantText,
				ToolCalls: toolCalls,
			}
			messages = append(messages, assistantMsg)

			// If no tool calls, the model is done
			if len(toolCalls) == 0 {
				ch <- AgentEvent{Done: true}
				return
			}

			// Execute each tool call and add results to messages
			for _, tc := range toolCalls {
				toolName := tc.Function.Name
				toolArgs := tc.Function.Arguments

				// Parse args for the event
				var argsMap map[string]interface{}
				json.Unmarshal(toolArgs, &argsMap)

				ch <- AgentEvent{
					ToolName:  toolName,
					ToolInput: argsMap,
				}

				result, err := tools.Execute(toolName, toolArgs, opts.WorkDir)
				if err != nil {
					result = fmt.Sprintf("Tool error: %v", err)
				}

				ch <- AgentEvent{ToolResult: result}

				// Add tool result to messages
				toolID := tc.ID
				if toolID == "" {
					toolID = toolName
				}
				messages = append(messages, ollama.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: toolID,
					Name:       toolName,
				})
			}

			// Continue the loop to let the model process tool results
		}

		ch <- AgentEvent{Error: fmt.Errorf("max tool rounds (%d) exceeded", opts.MaxToolRounds)}
	}()

	return ch
}

// RunInteractive starts an interactive session with the Ollama agent.
func RunInteractive(ctx context.Context, client *ollama.Client, initialPrompt string, opts AgentOptions) error {
	messages := []ollama.Message{
		{Role: "user", Content: initialPrompt},
	}

	reader := bufio.NewReader(os.Stdin)

	for {
		// Run the agent loop for one turn (assistant response + tool calls)
		stream := RunAgent(ctx, client, messages, opts)

		var assistantText string
		var streamErr error

		for event := range stream {
			if event.Error != nil {
				streamErr = event.Error
				break
			}
			if event.TextDelta != "" {
				fmt.Print(event.TextDelta)
				assistantText += event.TextDelta
			}
			if event.ToolName != "" {
				fmt.Printf("\n[agent uses tool: %s]\n", event.ToolName)
			}
		}

		if streamErr != nil {
			return streamErr
		}

		fmt.Print("\n\n> ")
		input, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		input = strings.TrimSpace(input)
		if input == "/exit" || input == "exit" || input == "quit" {
			return nil
		}

		messages = append(messages, ollama.Message{Role: "user", Content: input})
	}
}
