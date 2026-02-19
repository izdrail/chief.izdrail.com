package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minicodemonkey/chief/internal/db"
	"github.com/minicodemonkey/chief/internal/git"
	"github.com/minicodemonkey/chief/internal/git/api"
	"github.com/minicodemonkey/chief/internal/loop"
	"github.com/minicodemonkey/chief/internal/prd"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	addr        string
	baseDir     string
	store       *db.Store
	apiClient   *api.Client
	loopManager *loop.Manager
	mux         *http.ServeMux
	logBuffer   []string
	logMu       sync.Mutex
}

func NewServer(addr, baseDir string, gitBaseURL, gitToken string) *Server {
	// Initialize SQLite store
	dbPath := filepath.Join(baseDir, ".chief", "chief.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)
	store, err := db.NewStore(dbPath)
	if err != nil {
		fmt.Printf("Warning: failed to initialize SQLite store: %v\n", err)
	}

	srv := &Server{
		addr:        addr,
		baseDir:     baseDir,
		store:       store,
		apiClient:   api.NewClient(gitBaseURL, gitToken),
		loopManager: loop.NewManager(10),
		mux:         http.NewServeMux(),
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
	s.mux.HandleFunc("/api/prd/delete", s.handlePRDDelete)
	s.mux.HandleFunc("/api/agent/start", s.handleAgentStart)
	s.mux.HandleFunc("/api/agent/stop", s.handleAgentStop)
	s.mux.HandleFunc("/api/agent/status", s.handleAgentStatus)
	s.mux.HandleFunc("/api/agent/log", s.handleAgentLog)
	s.mux.HandleFunc("/api/git/repos", s.handleListRepos)
	s.mux.HandleFunc("/api/git/repos/", s.handleGitRepoAction)
	
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
		if err == nil {
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	prdDir := filepath.Join(s.baseDir, ".chief", "prds", req.Name)
	if err := os.MkdirAll(prdDir, 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Handle repo cloning if provided
	if req.RepoURL != "" {
		repoDir := filepath.Join(s.baseDir, ".chief", "repos", req.Name)
		if _, err := os.Stat(repoDir); os.IsNotExist(err) {
			s.log(fmt.Sprintf("Cloning repository %s into %s...", req.RepoURL, repoDir))
			if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
				http.Error(w, fmt.Sprintf("failed to create repo parent dir: %v", err), http.StatusInternalServerError)
				return
			}
			
			// Get token from env or config if available
			token := os.Getenv("GITHUB_TOKEN")
			if err := git.CloneRepo(req.RepoURL, repoDir, token); err != nil {
				http.Error(w, fmt.Sprintf("clone failed: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}

	// Generate the PRD specification first
	s.log(fmt.Sprintf("Generating specification for %s...", req.Name))
	if err := prd.GeneratePRD(prdDir, req.Name, req.Description); err != nil {
		http.Error(w, fmt.Sprintf("generation failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Run conversion
	s.log(fmt.Sprintf("Converting new PRD %s to JSON...", req.Name))
	if err := prd.Convert(prd.ConvertOptions{PRDDir: prdDir, Force: true}); err != nil {
		http.Error(w, fmt.Sprintf("conversion failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Sync to DB
	if s.store != nil {
		p, err := prd.LoadPRD(filepath.Join(prdDir, "prd.json"))
		if err == nil {
			projectID, err := s.store.SaveProject(req.Name, p.Project, p.Description, req.RepoURL)
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

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "PRD %s created", req.Name)
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
			s.handleListPullRequests(w, r, owner, repo)
		} else if r.Method == http.MethodPost {
			s.handleCreatePR(w, r, owner, repo)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	case "issues":
		if r.Method == http.MethodGet {
			s.handleListIssues(w, r, owner, repo)
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
	req.Owner = owner
	req.Repo = repo
	if err := s.apiClient.CreatePullRequest(req); err != nil {
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
	req.Owner = owner
	req.Repo = repo
	if err := s.apiClient.CreateIssue(req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleSuggestFix(w http.ResponseWriter, r *http.Request, owner, repo string) {
	var req api.FixSuggestionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Owner = owner
	req.Repo = repo
	if err := s.apiClient.SuggestFix(req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) log(msg string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logBuffer = append(s.logBuffer, fmt.Sprintf("%s: %s", time.Now().Format(time.RFC3339), msg))
}

// scanPRDs is a helper logic from main
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
