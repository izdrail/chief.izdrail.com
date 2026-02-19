// Package loop provides the core agent loop that orchestrates Ollama to
// implement user stories. It includes the main Loop struct for single
// PRD execution, Manager for parallel PRD execution, and Parser for
// processing Ollama's streaming output.
package loop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/izdrail/chief/embed"
	"github.com/izdrail/chief/internal/agent"
	"github.com/izdrail/chief/internal/db"
	"github.com/izdrail/chief/internal/git"
	"github.com/izdrail/chief/internal/ollama"
	"github.com/izdrail/chief/internal/prd"
)

// RetryConfig configures automatic retry behavior on Ollama errors.
type RetryConfig struct {
	MaxRetries  int             // Maximum number of retry attempts (default: 3)
	RetryDelays []time.Duration // Delays between retries (default: 0s, 5s, 15s)
	Enabled     bool            // Whether retry is enabled (default: true)
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:  3,
		RetryDelays: []time.Duration{0, 5 * time.Second, 15 * time.Second},
		Enabled:     true,
	}
}

// Loop manages the core agent loop that invokes Ollama repeatedly until all stories are complete.
type Loop struct {
	prdPath     string
	workDir     string
	prompt      string
	maxIter     int
	iteration   int
	events      chan Event
	logFile     *os.File
	mu          sync.Mutex
	stopped     bool
	paused      bool
	retryConfig RetryConfig
	ollamaClient *ollama.Client
	store       *db.Store
	repoURL     string
	cancelFunc  context.CancelFunc // cancel the current agent run
}

// NewLoop creates a new Loop instance.
func NewLoop(prdPath, prompt string, maxIter int) *Loop {
	return &Loop{
		prdPath:      prdPath,
		prompt:       prompt,
		maxIter:      maxIter,
		events:       make(chan Event, 100),
		retryConfig:  DefaultRetryConfig(),
		ollamaClient: ollama.NewClient(),
	}
}

// NewLoopWithWorkDir creates a new Loop instance with a configurable working directory.
// When workDir is empty, defaults to the project root for backward compatibility.
func NewLoopWithWorkDir(prdPath, workDir string, prompt string, maxIter int) *Loop {
	return &Loop{
		prdPath:      prdPath,
		workDir:      workDir,
		prompt:       prompt,
		maxIter:      maxIter,
		events:       make(chan Event, 100),
		retryConfig:  DefaultRetryConfig(),
		ollamaClient: ollama.NewClient(),
	}
}

// NewLoopWithEmbeddedPrompt creates a new Loop instance using the embedded agent prompt.
// The PRD path placeholder in the prompt is automatically substituted.
func NewLoopWithEmbeddedPrompt(prdPath string, maxIter int) *Loop {
	prompt := embed.GetPrompt(prdPath)
	return NewLoop(prdPath, prompt, maxIter)
}

// SetStore sets the SQLite store for the loop.
func (l *Loop) SetStore(s *db.Store) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = s
}

// SetRepoURL sets the repository URL for the loop.
func (l *Loop) SetRepoURL(url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.repoURL = url
}

// Events returns the channel for receiving events from the loop.
func (l *Loop) Events() <-chan Event {
	return l.events
}

// Iteration returns the current iteration number.
func (l *Loop) Iteration() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.iteration
}

// Run executes the agent loop until completion or max iterations.
func (l *Loop) Run(ctx context.Context) error {
	// Open log file in PRD directory
	prdDir := filepath.Dir(l.prdPath)
	logPath := filepath.Join(prdDir, "ollama.log")
	var err error
	l.logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer l.logFile.Close()
	defer close(l.events)

	for {
		l.mu.Lock()
		if l.stopped {
			l.mu.Unlock()
			return nil
		}
		if l.paused {
			l.mu.Unlock()
			return nil
		}
		l.iteration++
		currentIter := l.iteration
		l.mu.Unlock()

		// Check if max iterations reached
		if currentIter > l.maxIter {
			l.events <- Event{
				Type:      EventMaxIterationsReached,
				Iteration: currentIter - 1,
			}
			return nil
		}

		// Send iteration start event
		l.events <- Event{
			Type:      EventIterationStart,
			Iteration: currentIter,
		}

		// Snapshot story states before iteration to detect new completions
		prePassMap := map[string]bool{}
		if prePRD, err := l.loadPRD(); err == nil {
			for _, s := range prePRD.UserStories {
				prePassMap[s.ID] = s.Passes
			}
		}

		// Run a single iteration with retry logic
		if err := l.runIterationWithRetry(ctx); err != nil {
			l.events <- Event{
				Type: EventError,
				Err:  err,
			}
			return err
		}

		// Sync PRD from filesystem back to DB if available
		l.mu.Lock()
		store := l.store
		l.mu.Unlock()
		if store != nil {
			p, err := prd.LoadPRD(l.prdPath)
			if err == nil {
				prdName := filepath.Base(filepath.Dir(l.prdPath))
				l.mu.Lock()
				repoURL := l.repoURL
				l.mu.Unlock()
				projectID, err := store.SaveProject(prdName, p.Project, p.Description, repoURL)
				if err == nil {
					for _, story := range p.UserStories {
						store.SaveStory(projectID, db.StoryDB{
							ID:                 story.ID,
							Title:              story.Title,
							Description:        story.Description,
							AcceptanceCriteria: story.AcceptanceCriteria,
							Priority:           story.Priority,
							Passes:             story.Passes,
							InProgress:         story.InProgress,
						})
					}
				}

				// Auto-push: detect newly completed stories and commit+push
				l.autoPushIfStoryCompleted(p, prePassMap)
			}
		}


		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check prd.json/database for completion
		p, err := l.loadPRD()
		if err != nil {
			l.events <- Event{
				Type: EventError,
				Err:  fmt.Errorf("failed to load PRD: %w", err),
			}
			return err
		}

		if p.AllComplete() {
			l.events <- Event{
				Type:      EventComplete,
				Iteration: currentIter,
			}
			return nil
		}

		// Check pause flag after iteration (loop stops after current iteration completes)
		l.mu.Lock()
		if l.paused {
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
	}
}

// runIterationWithRetry wraps runIteration with retry logic for error recovery.
func (l *Loop) runIterationWithRetry(ctx context.Context) error {
	l.mu.Lock()
	config := l.retryConfig
	l.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		// Check if retry is enabled (except for first attempt)
		if attempt > 0 {
			if !config.Enabled {
				return lastErr
			}

			// Get delay for this retry
			delayIdx := attempt - 1
			if delayIdx >= len(config.RetryDelays) {
				delayIdx = len(config.RetryDelays) - 1
			}
			delay := config.RetryDelays[delayIdx]

			// Emit retry event
			l.mu.Lock()
			iter := l.iteration
			l.mu.Unlock()
			l.events <- Event{
				Type:       EventRetrying,
				Iteration:  iter,
				RetryCount: attempt,
				RetryMax:   config.MaxRetries,
				Text:       fmt.Sprintf("Ollama error, retrying (%d/%d)...", attempt, config.MaxRetries),
			}

			// Wait before retry
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}

		// Check if stopped during delay
		l.mu.Lock()
		if l.stopped {
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()

		// Run the iteration
		err := l.runIteration(ctx)
		if err == nil {
			return nil // Success
		}

		// Check if this is a context cancellation (don't retry)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Check if stopped intentionally
		l.mu.Lock()
		stopped := l.stopped
		l.mu.Unlock()
		if stopped {
			return nil
		}

		lastErr = err
	}

	return fmt.Errorf("max retries (%d) exceeded: %w", config.MaxRetries, lastErr)
}

// runIteration runs a single Ollama agent iteration.
func (l *Loop) runIteration(ctx context.Context) error {
	workDir := l.effectiveWorkDir()

	// Build a cancellable context for this iteration
	iterCtx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	l.cancelFunc = cancel
	l.mu.Unlock()
	defer func() {
		cancel()
		l.mu.Lock()
		l.cancelFunc = nil
		l.mu.Unlock()
	}()

	// Build the initial messages for this iteration
	messages := []ollama.Message{
		{
			Role:    "user",
			Content: l.prompt,
		},
	}

	agentOpts := agent.AgentOptions{
		WorkDir:       workDir,
		MaxToolRounds: 50,
	}

	stream := agent.RunAgent(iterCtx, l.ollamaClient, messages, agentOpts)

	for event := range stream {
		if event.Error != nil {
			// Check if we were stopped intentionally
			l.mu.Lock()
			stopped := l.stopped
			l.mu.Unlock()
			if stopped || iterCtx.Err() != nil {
				return nil
			}
			return event.Error
		}

		if event.TextDelta != "" {
			l.logLine(event.TextDelta)

			// Check for <chief-complete/> in the text
			if strings.Contains(event.TextDelta, "<chief-complete/>") {
				l.mu.Lock()
				iter := l.iteration
				l.mu.Unlock()
				l.events <- Event{
					Type:      EventComplete,
					Iteration: iter,
					Text:      event.TextDelta,
				}
				return nil
			}

			// Check for story status tags
			if storyID := extractStoryID(event.TextDelta, "<ralph-status>", "</ralph-status>"); storyID != "" {
				l.mu.Lock()
				iter := l.iteration
				l.mu.Unlock()
				l.events <- Event{
					Type:      EventStoryStarted,
					Iteration: iter,
					Text:      event.TextDelta,
					StoryID:   storyID,
				}
			} else {
				l.mu.Lock()
				iter := l.iteration
				l.mu.Unlock()
				l.events <- Event{
					Type:      EventAssistantText,
					Iteration: iter,
					Text:      event.TextDelta,
				}
			}
		}

		if event.ToolName != "" {
			l.logLine(fmt.Sprintf("[tool] %s %v", event.ToolName, event.ToolInput))
			l.mu.Lock()
			iter := l.iteration
			l.mu.Unlock()
			l.events <- Event{
				Type:      EventToolStart,
				Iteration: iter,
				Tool:      event.ToolName,
				ToolInput: event.ToolInput,
			}
		}

		if event.ToolResult != "" {
			l.logLine(fmt.Sprintf("[tool_result] %s", event.ToolResult))
			l.mu.Lock()
			iter := l.iteration
			l.mu.Unlock()
			l.events <- Event{
				Type:      EventToolResult,
				Iteration: iter,
				Text:      event.ToolResult,
			}
		}
	}

	return nil
}

// autoPushIfStoryCompleted detects newly completed stories and auto-commits+pushes.
func (l *Loop) autoPushIfStoryCompleted(p *prd.PRD, prePassMap map[string]bool) {
	workDir := l.effectiveWorkDir()

	// Check if we're in a git repo
	if !git.IsGitRepo(workDir) {
		return
	}

	// Find newly completed stories
	var completedStories []string
	for _, story := range p.UserStories {
		if story.Passes && !prePassMap[story.ID] {
			completedStories = append(completedStories, fmt.Sprintf("%s: %s", story.ID, story.Title))
		}
	}

	if len(completedStories) == 0 {
		return
	}

	// Get current branch
	branch, err := git.GetCurrentBranch(workDir)
	if err != nil || branch == "" {
		return
	}

	// Build commit message
	commitMsg := fmt.Sprintf("feat: complete %s", strings.Join(completedStories, ", "))
	if len(commitMsg) > 120 {
		commitMsg = fmt.Sprintf("feat: complete %d stories", len(completedStories))
	}

	l.events <- Event{
		Type: EventAssistantText,
		Text: fmt.Sprintf("Auto-pushing completed stories to %s...", branch),
	}

	if err := git.CommitAndPush(workDir, branch, commitMsg); err != nil {
		l.events <- Event{
			Type: EventError,
			Err:  fmt.Errorf("auto-push failed: %w", err),
		}
	} else {
		l.events <- Event{
			Type: EventAssistantText,
			Text: fmt.Sprintf("Pushed: %s", commitMsg),
		}
	}
}

// logLine writes a line to the log file.
func (l *Loop) logLine(line string) {
	if l.logFile != nil {
		l.logFile.WriteString(line + "\n")
	}
}

// Stop terminates the current Ollama agent run and stops the loop.
func (l *Loop) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.stopped = true

	if l.cancelFunc != nil {
		l.cancelFunc()
	}
}

// Pause sets the pause flag. The loop will stop after the current iteration completes.
func (l *Loop) Pause() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paused = true
}

// Resume clears the pause flag.
func (l *Loop) Resume() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paused = false
}

// IsPaused returns whether the loop is paused.
func (l *Loop) IsPaused() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.paused
}

// IsStopped returns whether the loop is stopped.
func (l *Loop) IsStopped() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopped
}

// effectiveWorkDir returns the working directory to use for tool execution.
// If workDir is set, it is used directly. Otherwise, defaults to the PRD directory.
func (l *Loop) effectiveWorkDir() string {
	if l.workDir != "" {
		return l.workDir
	}
	return filepath.Dir(l.prdPath)
}

// IsRunning returns whether an Ollama agent is currently running.
func (l *Loop) IsRunning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cancelFunc != nil
}

// SetMaxIterations updates the maximum iterations limit.
func (l *Loop) SetMaxIterations(maxIter int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxIter = maxIter
}

// SetMaxIterations returns the current max iterations limit.
func (l *Loop) loadPRD() (*prd.PRD, error) {
	l.mu.Lock()
	store := l.store
	l.mu.Unlock()

	if store != nil {
		prdName := filepath.Base(filepath.Dir(l.prdPath))
		id, err := store.GetProjectID(prdName)
		if err == nil {
			stories, err := store.GetStories(id)
			if err == nil {
				p := &prd.PRD{
					Project:     prdName,
					UserStories: make([]prd.UserStory, len(stories)),
				}
				for i, st := range stories {
					p.UserStories[i] = prd.UserStory{
						ID:                 st.ID,
						Title:              st.Title,
						Description:        st.Description,
						AcceptanceCriteria: st.AcceptanceCriteria,
						Priority:           st.Priority,
						Passes:             st.Passes,
						InProgress:         st.InProgress,
					}
				}
				return p, nil
			}
		}
	}

	return prd.LoadPRD(l.prdPath)
}

// SetRetryConfig updates the retry configuration.
func (l *Loop) SetRetryConfig(config RetryConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.retryConfig = config
}

// DisableRetry disables automatic retry on error.
func (l *Loop) DisableRetry() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.retryConfig.Enabled = false
}

// extractStoryID extracts a story ID from text between start and end tags.
func extractStoryID(text, startTag, endTag string) string {
	startIdx := strings.Index(text, startTag)
	if startIdx == -1 {
		return ""
	}
	startIdx += len(startTag)

	endIdx := strings.Index(text[startIdx:], endTag)
	if endIdx == -1 {
		return ""
	}

	return strings.TrimSpace(text[startIdx : startIdx+endIdx])
}
