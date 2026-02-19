package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/izdrail/chief/internal/config"
	"github.com/izdrail/chief/internal/db"
	"github.com/izdrail/chief/internal/git"
	"github.com/izdrail/chief/internal/git/api"
	"github.com/izdrail/chief/internal/loop"
	"github.com/izdrail/chief/internal/ollama"
	"github.com/izdrail/chief/internal/prd"
)

//go:embed static
var staticFiles embed.FS

type CreationStatus struct {
	PRDName string `json:"prd_name"`
	Status  string `json:"status"` // "pending", "generating", "converting", "syncing", "complete", "error"
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

type Server struct {
	addr           string
	baseDir        string
	store          *db.Store
	apiClient      *api.Client
	ollama         *ollama.Client
	loopManager    *loop.Manager
	mux            *http.ServeMux
	logBuffer      []string
	logMu          sync.Mutex
	creationStatus map[string]*CreationStatus
	statusMu       sync.Mutex
}

func NewServer(addr, baseDir string, gitToken string) *Server {
	// Initialize SQLite store
	dbPath := filepath.Join(baseDir, ".chief", "chief.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	store, err := db.NewStore(dbPath)
	if err != nil {
		fmt.Printf("Warning: failed to initialize SQLite store: %v\n", err)
	}

	srv := &Server{
		addr:           addr,
		baseDir:        baseDir,
		store:          store,
		apiClient:      api.NewClient(gitToken),
		ollama:         ollama.NewClient(),
		loopManager:    loop.NewManager(10),
		mux:            http.NewServeMux(),
		creationStatus: make(map[string]*CreationStatus),
	}
	if store != nil {
		srv.loopManager.SetStore(store)
	}
	return srv
}

func (s *Server) Start() error {
	s.mux.HandleFunc("/api/prd/list", s.handlePRDList)
	s.mux.HandleFunc("/api/prd/get", s.handlePRDGet)
	s.mux.HandleFunc("/api/prd/create", s.handlePRDCreate)
	s.mux.HandleFunc("/api/prd/create/status", s.handlePRDCreateStatus)
	s.mux.HandleFunc("/api/prd/delete", s.handlePRDDelete)
	s.mux.HandleFunc("/api/agent/start", s.handleAgentStart)
	s.mux.HandleFunc("/api/agent/stop", s.handleAgentStop)
	s.mux.HandleFunc("/api/agent/status", s.handleAgentStatus)
	s.mux.HandleFunc("/api/agent/log", s.handleAgentLog)
	s.mux.HandleFunc("/api/agent/iterations", s.handleAgentIterations)
	s.mux.HandleFunc("/api/git/repos", s.handleListRepos)
	s.mux.HandleFunc("/api/git/repos/", s.handleGitRepoAction)
	s.mux.HandleFunc("/api/git/diff", s.handleGitDiff)
	s.mux.HandleFunc("/api/git/push", s.handleGitPush)
	s.mux.HandleFunc("/api/git/pr", s.handleGitPR)
	s.mux.HandleFunc("/api/git/merge", s.handleGitMerge)
	s.mux.HandleFunc("/api/git/clean", s.handleGitClean)
	s.mux.HandleFunc("/api/story/delete", s.handleStoryDelete)
	s.mux.HandleFunc("/api/config", s.handleConfig)
	
	// Serve static frontend
	subFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to create static file system: %w", err)
	}
	s.mux.Handle("/", http.FileServer(http.FS(subFS)))

	// Start event listener
	go func() {
		for event := range s.loopManager.Events() {
			msg := ""
			if event.Event.Text != "" {
				msg = event.Event.Text
			} else if event.Event.Tool != "" {
				msg = fmt.Sprintf("Tool: %s", event.Event.Tool)
			} else if event.Event.Err != nil {
				msg = fmt.Sprintf("Error: %v", event.Event.Err)
			}
			
			if msg != "" {
				s.log(fmt.Sprintf("[%s] %s", event.PRDName, msg))
				if s.store != nil {
					s.store.AddLog(event.PRDName, msg)
				}
			}
		}
	}()

	fmt.Printf("Starting Chief server on %s\n", s.addr)
	return http.ListenAndServe(s.addr, s.mux)
}

func (s *Server) handlePRDList(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		prds, err := s.store.ListProjects()
		if err == nil && len(prds) > 0 {
			json.NewEncoder(w).Encode(prds)
			return
		}
	}

	// Fallback to directory scan
	names, err := scanPRDs(filepath.Join(s.baseDir, ".chief", "prds"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	prds := make([]db.ProjectInfo, len(names))
	for i, name := range names {
		prds[i] = db.ProjectInfo{
			Name:  name,
			Title: name,
		}
	}
	json.NewEncoder(w).Encode(prds)
}

func (s *Server) handlePRDGet(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	// 1. Always start by loading from the PRD JSON file (the source of truth for spec)
	path := filepath.Join(s.baseDir, ".chief", "prds", name, "prd.json")
	p, err := prd.LoadPRD(path)
	if err != nil {
		// If file doesn't exist, we might try loading from DB only, or just return 404
		http.Error(w, fmt.Sprintf("failed to load PRD from file: %v", err), http.StatusNotFound)
		return
	}

	// 2. Overlay state from database if it exists (the source of truth for progress)
	if s.store != nil {
		id, _, _, _, err := s.store.GetProject(name)
		if err == nil {
			stories, err := s.store.GetStories(id)
			if err == nil && len(stories) > 0 {
				// Create a map for quick lookup
				storyMap := make(map[string]db.StoryDB)
				for _, st := range stories {
					storyMap[st.ID] = st
				}

				// Update file stories with DB state
				for i := range p.UserStories {
					if st, ok := storyMap[p.UserStories[i].ID]; ok {
						p.UserStories[i].Passes = st.Passes
						p.UserStories[i].InProgress = st.InProgress
					}
				}
			}
		}
	}

	json.NewEncoder(w).Encode(p)
}

func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	
	var req struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		req.Path = filepath.Join(s.baseDir, ".chief", "prds", req.Name, "prd.json")
	}

	// Sync to DB if available
	var repoURL string
	if s.store != nil {
		p, err := prd.LoadPRD(req.Path)
		if err == nil {
			// Try to get existing repoURL
			_, _, _, rURL, _ := s.store.GetProject(req.Name)
			repoURL = rURL
			
			projectID, err := s.store.SaveProject(req.Name, p.Project, p.Description, repoURL)
			if err == nil {
				for _, story := range p.UserStories {
					s.store.SaveStory(projectID, db.StoryDB{
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
		}
	}

	// Automatic branch/worktree management
	branchName := fmt.Sprintf("chief/%s", req.Name)
	worktreePath := git.WorktreePathForPRD(s.baseDir, req.Name)
	
	repoBaseDir := s.baseDir
	if repoURL != "" {
		repoBaseDir = filepath.Join(s.baseDir, ".chief", "repos", req.Name)
	}

	if git.IsGitRepo(repoBaseDir) {
		s.log(fmt.Sprintf("Ensuring worktree for %s on branch %s", req.Name, branchName))
		if err := git.CreateWorktree(repoBaseDir, worktreePath, branchName); err != nil {
			s.log(fmt.Sprintf("Warning: failed to create worktree: %v. Running in base dir.", err))
			// Fallback to base dir if worktree creation fails
			worktreePath = ""
			branchName = ""
		}
	} else {
		worktreePath = ""
		branchName = ""
	}

	// Register or update with worktree info
	if instance := s.loopManager.GetInstance(req.Name); instance == nil {
		if worktreePath != "" {
			s.loopManager.RegisterWithWorktree(req.Name, req.Path, repoURL, worktreePath, branchName)
		} else {
			s.loopManager.Register(req.Name, req.Path)
		}
	} else if worktreePath != "" {
		s.loopManager.UpdateWorktreeInfo(req.Name, repoURL, worktreePath, branchName)
	}
	
	// Start via manager
	if err := s.loopManager.Start(req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// We can't easily stream logs back in this request structure without a websocket or long poll
	// The manager runs the loop in a goroutine and sends events to its central channel.
	// We need to tap into that central channel in the server struct.

	fmt.Fprintf(w, "Agent started for %s", req.Name)
}

func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	// Implementation to stop agent
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	
	if err := s.loopManager.Stop(name); err == nil {
		fmt.Fprintf(w, "Agent stopped for %s", name)
	} else {
		http.Error(w, "agent not found", http.StatusNotFound)
	}
}

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	state, iter, err := s.loopManager.GetState(name)
	
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	
	resp := map[string]interface{}{
		"state":     state,
		"iteration": iter,
		"error":     errStr,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handlePRDCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		RepoURL     string `json:"repo_url"`
		Restart     bool   `json:"restart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	s.statusMu.Lock()
	if !req.Restart {
		if status, exists := s.creationStatus[req.Name]; exists && status.Status != "error" && status.Status != "complete" {
			s.statusMu.Unlock()
			http.Error(w, "Creation already in progress for this PRD", http.StatusConflict)
			return
		}
	}
	s.creationStatus[req.Name] = &CreationStatus{
		PRDName: req.Name,
		Status:  "pending",
		Message: "Starting creation process...",
	}
	s.statusMu.Unlock()

	// Run in background
	go s.runPRDCreationTask(req.Name, req.Description, req.RepoURL)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "prd_name": req.Name})
}

func (s *Server) runPRDCreationTask(name, description, repoURL string) {
	updateStatus := func(status, message, errStr string) {
		s.statusMu.Lock()
		defer s.statusMu.Unlock()
		if s.creationStatus[name] == nil {
			s.creationStatus[name] = &CreationStatus{PRDName: name}
		}
		s.creationStatus[name].Status = status
		s.creationStatus[name].Message = message
		s.creationStatus[name].Error = errStr
	}

	prdDir := filepath.Join(s.baseDir, ".chief", "prds", name)
	if err := os.MkdirAll(prdDir, 0755); err != nil {
		updateStatus("error", "Failed to create PRD directory", err.Error())
		return
	}

	// Handle repo cloning if provided
	if repoURL != "" {
		updateStatus("cloning", "Cloning repository...", "")
		repoDir := filepath.Join(s.baseDir, ".chief", "repos", name)
		if _, err := os.Stat(repoDir); os.IsNotExist(err) {
			s.log(fmt.Sprintf("Cloning repository %s into %s...", repoURL, repoDir))
			if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
				updateStatus("error", "Failed to create repo parent dir", err.Error())
				return
			}
			
			token := os.Getenv("GITHUB_TOKEN")
			if err := git.CloneRepo(repoURL, repoDir, token); err != nil {
				updateStatus("error", "Clone failed", err.Error())
				return
			}
		}
	}

	// Generate the PRD specification first
	updateStatus("generating", "Generating specification from description...", "")
	s.log(fmt.Sprintf("Generating specification for %s...", name))
	if err := prd.GeneratePRD(prdDir, name, description); err != nil {
		updateStatus("error", "Generation failed", err.Error())
		return
	}

	// Run conversion
	updateStatus("converting", "Converting new PRD to JSON...", "")
	s.log(fmt.Sprintf("Converting new PRD %s to JSON...", name))
	if err := prd.Convert(prd.ConvertOptions{PRDDir: prdDir, Force: true}); err != nil {
		updateStatus("error", "Conversion failed", err.Error())
		return
	}

	// Sync to DB
	updateStatus("syncing", "Syncing to database...", "")
	if s.store != nil {
		p, err := prd.LoadPRD(filepath.Join(prdDir, "prd.json"))
		if err == nil {
			projectID, err := s.store.SaveProject(name, p.Project, p.Description, repoURL)
			if err == nil {
				for _, story := range p.UserStories {
					s.store.SaveStory(projectID, db.StoryDB{
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
		}
	}

	updateStatus("complete", "PRD created successfully", "")
}

func (s *Server) handlePRDCreateStatus(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	s.statusMu.Lock()
	status, exists := s.creationStatus[name]
	s.statusMu.Unlock()

	if !exists {
		http.Error(w, "No creation task found for this PRD", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handlePRDDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	// 1. Stop and remove from loop manager
	s.loopManager.Remove(req.Name)

	// 2. Remove from database
	if s.store != nil {
		if err := s.store.DeleteProject(req.Name); err != nil {
			s.log(fmt.Sprintf("Warning: failed to delete project from DB: %v", err))
		}
	}

	// 3. Remove PRD directory
	prdDir := filepath.Join(s.baseDir, ".chief", "prds", req.Name)
	if err := os.RemoveAll(prdDir); err != nil {
		s.log(fmt.Sprintf("Warning: failed to remove PRD dir: %v", err))
	}

	// 4. Remove cloned repo directory if it exists
	repoDir := filepath.Join(s.baseDir, ".chief", "repos", req.Name)
	if _, err := os.Stat(repoDir); err == nil {
		if err := os.RemoveAll(repoDir); err != nil {
			s.log(fmt.Sprintf("Warning: failed to remove repo dir: %v", err))
		}
	}

	// 5. Remove worktree directory if it exists
	worktreeDir := filepath.Join(s.baseDir, ".chief", "worktrees", req.Name)
	if _, err := os.Stat(worktreeDir); err == nil {
		git.RemoveWorktree(s.baseDir, worktreeDir)
	}

	s.log(fmt.Sprintf("PRD %s deleted", req.Name))
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "PRD %s deleted", req.Name)
}

func (s *Server) handleAgentIterations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(map[string]int{"max_iterations": s.loopManager.MaxIterations()})
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Name  string `json:"name"`
			Delta int    `json:"delta"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		current := s.loopManager.MaxIterations()
		newMax := current + req.Delta
		if newMax < 1 {
			newMax = 1
		}

		s.loopManager.SetMaxIterations(newMax)
		if req.Name != "" {
			s.loopManager.SetMaxIterationsForInstance(req.Name, newMax)
		}

		json.NewEncoder(w).Encode(map[string]int{"max_iterations": newMax})
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleAgentLog(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if s.store != nil && name != "" {
		logs, err := s.store.GetLogs(name, 100)
		if err == nil {
			json.NewEncoder(w).Encode(logs)
			return
		}
	}

	s.logMu.Lock()
	defer s.logMu.Unlock()
	// Return last 100 log lines
	start := 0
	if len(s.logBuffer) > 100 {
		start = len(s.logBuffer) - 100
	}
	json.NewEncoder(w).Encode(s.logBuffer[start:])
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.apiClient.ListRepos()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(repos)
}

func (s *Server) handleGitRepoAction(w http.ResponseWriter, r *http.Request) {
	// Simple path parsing for /api/git/repos/{owner}/{repo}/{action}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	owner := parts[3]
	repo := parts[4]
	action := parts[5]

	switch action {
	case "pulls":
		if r.Method == http.MethodGet {
			if len(parts) > 6 {
				num, _ := strconv.Atoi(parts[6])
				s.handleGetPullRequest(w, r, owner, repo, num)
			} else {
				s.handleListPullRequests(w, r, owner, repo)
			}
		} else if r.Method == http.MethodPost {
			s.handleCreatePR(w, r, owner, repo)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	case "issues":
		if r.Method == http.MethodGet {
			if len(parts) > 6 {
				num, _ := strconv.Atoi(parts[6])
				s.handleGetIssue(w, r, owner, repo, num)
			} else {
				s.handleListIssues(w, r, owner, repo)
			}
		} else if r.Method == http.MethodPost {
			s.handleCreateIssue(w, r, owner, repo)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	case "suggest-fix":
		if r.Method == http.MethodPost {
			s.handleSuggestFix(w, r, owner, repo)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

func (s *Server) handleListIssues(w http.ResponseWriter, r *http.Request, owner, repo string) {
	state := r.URL.Query().Get("state")
	issues, err := s.apiClient.ListIssues(owner, repo, state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(issues)
}

func (s *Server) handleListPullRequests(w http.ResponseWriter, r *http.Request, owner, repo string) {
	state := r.URL.Query().Get("state")
	pulls, err := s.apiClient.ListPullRequests(owner, repo, state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pulls)
}

func (s *Server) handleCreatePR(w http.ResponseWriter, r *http.Request, owner, repo string) {
	var req api.PullRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.apiClient.CreatePullRequest(owner, repo, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleCreateIssue(w http.ResponseWriter, r *http.Request, owner, repo string) {
	var req api.IssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.apiClient.CreateIssue(owner, repo, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleSuggestFix(w http.ResponseWriter, r *http.Request, owner, repo string) {
	var req struct {
		IssueNumber int    `json:"issue_number"`
		Context     string `json:"context"` // Optional context or code snippet
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Fetch issue details
	issue, err := s.apiClient.GetIssue(owner, repo, req.IssueNumber)
	if err != nil {
		s.log(fmt.Sprintf("Warning: failed to fetch issue #%d: %v", req.IssueNumber, err))
	}

	prompt := fmt.Sprintf("I need a fix for issue #%d in %s/%s.\n", req.IssueNumber, owner, repo)
	if issue != nil {
		prompt += fmt.Sprintf("Issue Title: %s\nIssue Body: %s\n", issue.Title, issue.Body)
	}
	if req.Context != "" {
		prompt += fmt.Sprintf("\nContext:\n%s\n", req.Context)
	}
	prompt += "\nPlease suggest a fix or implementation plan."

	// Use local Ollama
	resp, err := s.ollama.Chat(r.Context(), ollama.ChatRequest{
		Model: "codellama:7b", // Default or configurable
		Messages: []ollama.Message{
			{Role: "user", Content: prompt},
		},
		Stream: false,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("ollama error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"suggestion": resp.Content,
	})
}

func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request, owner, repo string, number int) {
	issue, err := s.apiClient.GetIssue(owner, repo, number)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(issue)
}

func (s *Server) handleGetPullRequest(w http.ResponseWriter, r *http.Request, owner, repo string, number int) {
	pull, err := s.apiClient.GetPullRequest(owner, repo, number)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pull)
}

func (s *Server) log(msg string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logBuffer = append(s.logBuffer, fmt.Sprintf("%s: %s", time.Now().Format(time.RFC3339), msg))
}

// scanPRDs is a helper logic from main
func (s *Server) handleStoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		PRDName string `json:"prd_name"`
		StoryID string `json:"story_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.PRDName == "" || req.StoryID == "" {
		http.Error(w, "prd_name and story_id required", http.StatusBadRequest)
		return
	}

	// 1. Remove from database
	if s.store != nil {
		projectID, err := s.store.GetProjectID(req.PRDName)
		if err == nil {
			if err := s.store.DeleteStory(projectID, req.StoryID); err != nil {
				s.log(fmt.Sprintf("Warning: failed to delete story %s from DB: %v", req.StoryID, err))
			}
		}
	}

	// 2. Remove from prd.json file
	path := filepath.Join(s.baseDir, ".chief", "prds", req.PRDName, "prd.json")
	p, err := prd.LoadPRD(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to load PRD: %v", err), http.StatusNotFound)
		return
	}

	newStories := make([]prd.UserStory, 0)
	found := false
	for _, story := range p.UserStories {
		if story.ID != req.StoryID {
			newStories = append(newStories, story)
		} else {
			found = true
		}
	}

	if !found {
		http.Error(w, "story not found in PRD file", http.StatusNotFound)
		return
	}

	p.UserStories = newStories
	normalizedContent, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to marshal PRD: %v", err), http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(path, append(normalizedContent, '\n'), 0644); err != nil {
		http.Error(w, fmt.Sprintf("failed to write PRD file: %v", err), http.StatusInternalServerError)
		return
	}

	s.log(fmt.Sprintf("Story %s removed from PRD %s", req.StoryID, req.PRDName))
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Story %s removed", req.StoryID)
}

func scanPRDs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			prdPath := filepath.Join(root, entry.Name(), "prd.json")
			if _, err := os.Stat(prdPath); err == nil {
				names = append(names, entry.Name())
			}
		}
	}
	return names, nil
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg, _ := config.Load(s.baseDir)
		json.NewEncoder(w).Encode(cfg)
		return
	}
	if r.Method == http.MethodPost {
		var cfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.Save(s.baseDir, &cfg)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	worktreeDir := git.WorktreePathForPRD(s.baseDir, name)
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		worktreeDir = s.baseDir
	}
	diff, err := git.GetDiff(worktreeDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"diff": diff})
}

func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	worktreeDir := git.WorktreePathForPRD(s.baseDir, req.Name)
	// Get current branch to push
	branch, err := git.GetCurrentBranch(worktreeDir)
	if err != nil {
		http.Error(w, "Could not determine current branch: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := git.PushBranch(worktreeDir, branch); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGitPR(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, title, desc, repoURL, err := s.store.GetProject(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	owner, repo, err := git.ParseGithubURL(repoURL)
	if err != nil {
		http.Error(w, "Invalid repo URL", http.StatusBadRequest)
		return
	}

	worktreeDir := git.WorktreePathForPRD(s.baseDir, req.Name)
	branch, err := git.GetCurrentBranch(worktreeDir)
	if err != nil {
		http.Error(w, "Failed to get current branch", http.StatusInternalServerError)
		return
	}

	prReq := api.PullRequestRequest{
		Title: fmt.Sprintf("feat(%s): %s", req.Name, title),
		Body:  desc,
		Head:  branch,
		Base:  "main",
	}

	pr, err := s.apiClient.CreatePullRequest(owner, repo, prReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(pr)
}

func (s *Server) handleGitMerge(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	worktreeDir := git.WorktreePathForPRD(s.baseDir, req.Name)
	branch, err := git.GetCurrentBranch(worktreeDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	conflicts, err := git.MergeBranch(s.baseDir, branch)
	if err != nil {
		if len(conflicts) > 0 {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":     err.Error(),
				"conflicts": conflicts,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGitClean(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string `json:"name"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	worktreeDir := git.WorktreePathForPRD(s.baseDir, req.Name)
	if err := git.RemoveWorktree(s.baseDir, worktreeDir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
