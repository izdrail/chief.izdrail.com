// Package tools provides file system and shell tools that the Ollama agent
// can invoke during its agentic loop. These match the tool names referenced
// in the embedded agent prompt (Read, Write, Edit, Bash, Glob, Grep, List).
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/izdrail/chief/internal/ollama"
)

// Definitions returns the list of tool definitions to pass to the Ollama API.
func Definitions() []ollama.Tool {
	return []ollama.Tool{
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "Read",
				Description: "Read the contents of a file at the given path. Optionally specify start_line and end_line (1-indexed, inclusive) to read a slice.",
				Parameters: mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "Path to the file to read. Relative to the project root.",
						},
						"start_line": map[string]interface{}{
							"type":        "integer",
							"description": "Optional. First line to return (1-indexed).",
						},
						"end_line": map[string]interface{}{
							"type":        "integer",
							"description": "Optional. Last line to return (1-indexed, inclusive).",
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
				Description: "Write content to a file, creating it or overwriting it completely.",
				Parameters: mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "Path to the file to write. Relative to the project root.",
						},
						"content": map[string]interface{}{
							"type":        "string",
							"description": "The full content to write to the file.",
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
				Description: "Edit a file by replacing an exact string with new content. The old_string must exist verbatim in the file.",
				Parameters: mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "Path to the file to edit. Relative to the project root.",
						},
						"old_string": map[string]interface{}{
							"type":        "string",
							"description": "The exact string to find and replace (must exist verbatim).",
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
				Description: "Run a shell command and return its combined stdout+stderr. Use for running tests, builds, git commits, linting, etc. Working directory is the project root.",
				Parameters: mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{
							"type":        "string",
							"description": "The shell command to execute. Runs in bash (or sh if bash unavailable).",
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
				Description: "Find files matching a glob pattern. Supports ** for recursive matching (e.g. **/*.go). Returns paths relative to the project root.",
				Parameters: mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"pattern": map[string]interface{}{
							"type":        "string",
							"description": "Glob pattern. Use ** for recursive (e.g. **/*.go, src/**/*.ts).",
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
				Description: "Search for a regex pattern in files and return matching lines with file:line format.",
				Parameters: mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"pattern": map[string]interface{}{
							"type":        "string",
							"description": "The regex pattern to search for.",
						},
						"path": map[string]interface{}{
							"type":        "string",
							"description": "The file or directory to search in. Defaults to project root.",
						},
					},
					"required": []string{"pattern"},
				}),
			},
		},
		{
			Type: "function",
			Function: ollama.ToolFunction{
				Name:        "List",
				Description: "List the contents of a directory (files and subdirectories). Use this to explore the project structure.",
				Parameters: mustJSON(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type":        "string",
							"description": "Directory path to list. Relative to the project root. Use '.' for the root itself.",
						},
					},
					"required": []string{"path"},
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
	case "List":
		return executeList(argsJSON, workDir)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func executeRead(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		FilePath  string `json:"file_path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Read args: %w", err)
	}
	path := resolvePath(args.FilePath, workDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err), nil
	}

	content := string(data)

	// Apply line range if specified
	if args.StartLine > 0 || args.EndLine > 0 {
		lines := strings.Split(content, "\n")
		start := args.StartLine - 1
		if start < 0 {
			start = 0
		}
		end := args.EndLine
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		if start >= len(lines) {
			return "Error: start_line is beyond end of file", nil
		}
		content = strings.Join(lines[start:end], "\n")
	}

	// Truncate very large files with a helpful message
	const maxOutput = 12000
	if len(content) > maxOutput {
		content = content[:maxOutput] + fmt.Sprintf("\n\n... (file truncated at %d bytes, use start_line/end_line to read more)", maxOutput)
	}

	return content, nil
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

	// Try bash first; fall back to sh (for Alpine and minimal containers)
	shell := "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		shell = "sh"
	}

	cmd := exec.Command(shell, "-c", args.Command)
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

// executeGlob supports ** recursive patterns by walking the directory tree.
func executeGlob(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Glob args: %w", err)
	}

	pattern := args.Pattern

	// If the pattern starts with **, do a recursive walk
	if strings.Contains(pattern, "**") {
		return globRecursive(pattern, workDir)
	}

	// Otherwise use standard filepath.Glob
	absPattern := pattern
	if !filepath.IsAbs(pattern) {
		absPattern = filepath.Join(workDir, pattern)
	}

	matches, err := filepath.Glob(absPattern)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	if len(matches) == 0 {
		return "No files found matching pattern.", nil
	}

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

// globRecursive walks workDir and matches files against the suffix pattern after **.
func globRecursive(pattern, workDir string) (string, error) {
	// Split on ** to get prefix dir and suffix pattern
	parts := strings.SplitN(pattern, "**", 2)
	baseDir := workDir
	if parts[0] != "" && parts[0] != "/" {
		baseDir = filepath.Join(workDir, strings.TrimSuffix(parts[0], "/"))
	}
	suffix := strings.TrimPrefix(parts[1], "/")

	var matches []string
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if d.IsDir() {
			// skip hidden dirs like .git
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if suffix == "" {
			matches = append(matches, path)
			return nil
		}
		ok, _ := filepath.Match(suffix, d.Name())
		if ok {
			rel, _ := filepath.Rel(workDir, path)
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return fmt.Sprintf("Error walking directory: %v", err), nil
	}
	if len(matches) == 0 {
		return "No files found matching pattern.", nil
	}
	// Cap results
	if len(matches) > 200 {
		matches = matches[:200]
		matches = append(matches, "... (results capped at 200)")
	}
	return strings.Join(matches, "\n"), nil
}

func executeGrep(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse Grep args: %w", err)
	}

	searchPath := workDir
	if args.Path != "" {
		searchPath = resolvePath(args.Path, workDir)
	}

	grepArgs := []string{"-r", "-n", args.Pattern, searchPath}

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

	// Make paths relative
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	var rel []string
	for _, line := range lines {
		if colonIdx := strings.Index(line, ":"); colonIdx > 0 {
			absPath := line[:colonIdx]
			rest := line[colonIdx:]
			relPath, err := filepath.Rel(workDir, absPath)
			if err == nil {
				line = relPath + rest
			}
		}
		rel = append(rel, line)
	}
	output = strings.Join(rel, "\n")

	// Truncate very long output
	const maxOutput = 4096
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n... (output truncated)"
	}

	return output, nil
}

// executeList lists the contents of a directory.
func executeList(argsJSON json.RawMessage, workDir string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return "", fmt.Errorf("parse List args: %w", err)
	}

	dirPath := workDir
	if args.Path != "" && args.Path != "." {
		dirPath = resolvePath(args.Path, workDir)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Sprintf("Error listing directory: %v", err), nil
	}

	if len(entries) == 0 {
		return "(empty directory)", nil
	}

	var lines []string
	for _, entry := range entries {
		if entry.IsDir() {
			lines = append(lines, entry.Name()+"/")
		} else {
			info, _ := entry.Info()
			size := ""
			if info != nil {
				size = fmt.Sprintf(" (%d bytes)", info.Size())
			}
			lines = append(lines, entry.Name()+size)
		}
	}

	relPath, _ := filepath.Rel(workDir, dirPath)
	header := fmt.Sprintf("Contents of %s/:", relPath)
	return header + "\n" + strings.Join(lines, "\n"), nil
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
