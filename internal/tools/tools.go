// Package tools provides file system and shell tools that the Ollama agent
// can invoke during its agentic loop. These match the tool names referenced
// in the embedded agent prompt (Read, Write, Edit, Bash, Glob, Grep).
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/minicodemonkey/chief/internal/ollama"
)

// Definitions returns the list of tool definitions to pass to the Ollama API.
func Definitions() []ollama.Tool {
	return []ollama.Tool{
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "Read",
				Description: "Read the contents of a file at the given path.",
				Parameters:  mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "The path to the file to read.",
						},
					},
					"required": []string{"file_path"},
				}),
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "Write",
				Description: "Write content to a file, creating it or overwriting it.",
				Parameters:  mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "The path to the file to write.",
						},
						"content": map[string]interface{}{
							"type":        "string",
							"description": "The content to write to the file.",
						},
					},
					"required": []string{"file_path", "content"},
				}),
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "Edit",
				Description: "Edit a file by replacing an exact string with new content.",
				Parameters:  mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "The path to the file to edit.",
						},
						"old_string": map[string]interface{}{
							"type":        "string",
							"description": "The exact string to find and replace.",
						},
						"new_string": map[string]interface{}{
							"type":        "string",
							"description": "The replacement string.",
						},
					},
					"required": []string{"file_path", "old_string", "new_string"},
				}),
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "Bash",
				Description: "Run a shell command and return its output. Use for running tests, builds, git commits, etc.",
				Parameters:  mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{
							"type":        "string",
							"description": "The shell command to execute.",
						},
					},
					"required": []string{"command"},
				}),
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "Glob",
				Description: "Find files matching a glob pattern.",
				Parameters:  mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"pattern": map[string]interface{}{
							"type":        "string",
							"description": "The glob pattern to match files against.",
						},
					},
					"required": []string{"pattern"},
				}),
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "Grep",
				Description: "Search for a pattern in files and return matching lines.",
				Parameters:  mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"pattern": map[string]interface{}{
							"type":        "string",
							"description": "The regex pattern to search for.",
						},
						"path": map[string]interface{}{
							"type":        "string",
							"description": "The file or directory to search in.",
						},
					},
					"required": []string{"pattern"},
				}),
			},
		},
	}
}

// Execute runs the named tool with the given JSON arguments in the specified working directory.
// Returns the tool output as a string.
func Execute(name string, argsJSON json.RawMessage, workDir string) (string, error) {
	switch name {
	case "Read":
		return executeRead(argsJSON, workDir)
	case "Write":
		return executeWrite(argsJSON, workDir)
	case "Edit":
		return executeEdit(argsJSON, workDir)
	case "Bash":
		return executeBash(argsJSON, workDir)
	case "Glob":
		return executeGlob(argsJSON, workDir)
	case "Grep":
		return executeGrep(argsJSON, workDir)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func executeRead(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Read args: %w", err)
	}
	path := resolvePath(args.FilePath, workDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err), nil
	}
	return string(data), nil
}

func executeWrite(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Write args: %w", err)
	}
	path := resolvePath(args.FilePath, workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Sprintf("Error creating directories: %v", err), nil
	}
	if err := os.WriteFile(path, []byte(args.Content), 0644); err != nil {
		return fmt.Sprintf("Error writing file: %v", err), nil
	}
	return fmt.Sprintf("File written successfully: %s", args.FilePath), nil
}

func executeEdit(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		FilePath  string `json:"file_path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Edit args: %w", err)
	}
	path := resolvePath(args.FilePath, workDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Error reading file for edit: %v", err), nil
	}
	content := string(data)
	if !strings.Contains(content, args.OldString) {
		return fmt.Sprintf("Error: old_string not found in %s", args.FilePath), nil
	}
	newContent := strings.Replace(content, args.OldString, args.NewString, 1)
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return fmt.Sprintf("Error writing edited file: %v", err), nil
	}
	return fmt.Sprintf("File edited successfully: %s", args.FilePath), nil
}

func executeBash(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Bash args: %w", err)
	}

	cmd := exec.Command("bash", "-c", args.Command)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n[stderr]\n" + stderr.String()
	}
	if err != nil {
		output += fmt.Sprintf("\n[exit error: %v]", err)
	}

	// Truncate very long output
	const maxOutput = 8192
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n... (output truncated)"
	}

	return output, nil
}

func executeGlob(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Glob args: %w", err)
	}

	pattern := args.Pattern
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(workDir, pattern)
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	if len(matches) == 0 {
		return "No files found matching pattern.", nil
	}

	// Make paths relative to workDir for readability
	var result []string
	for _, m := range matches {
		rel, err := filepath.Rel(workDir, m)
		if err != nil {
			rel = m
		}
		result = append(result, rel)
	}

	return strings.Join(result, "\n"), nil
}

func executeGrep(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Grep args: %w", err)
	}

	grepArgs := []string{"-r", "-n", "--include=*", args.Pattern}
	if args.Path != "" {
		searchPath := resolvePath(args.Path, workDir)
		grepArgs = append(grepArgs, searchPath)
	} else {
		grepArgs = append(grepArgs, workDir)
	}

	cmd := exec.Command("grep", grepArgs...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmd.Run() // grep returns exit code 1 when no matches, that's OK

	output := stdout.String()
	if output == "" {
		return "No matches found.", nil
	}

	// Truncate very long output
	const maxOutput = 4096
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n... (output truncated)"
	}

	return output, nil
}

// resolvePath resolves a path relative to workDir if it's not absolute.
func resolvePath(path, workDir string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workDir, path)
}

// mustJSON marshals v to JSON, panicking on error.
func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return b
}
