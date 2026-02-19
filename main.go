package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// Repository represents repository details
type Repository struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Visibility string    `json:"visibility"` // "public" or "private"
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Size        int       `json:"size"` // in KB
	Readme      string    `json:"readme,omitempty"`
	License     string    `json:"license,omitempty"`
	Contributors []string `json:"contributors,omitempty"`
}

// repositories is a mock database of repositories
var repositories = map[string]Repository{
	"org1/repo1": {
		Name:        "repo1",
		Description: "First repository",
		Visibility:  "public",
		CreatedAt:   time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2023, 2, 20, 14, 30, 0, 0, time.UTC),
		Size:        1500,
		Readme:      "# Repository 1\n\nThis is the first repository.",
		License:     "MIT",
		Contributors: []string{"user1", "user2"},
	},
	"org2/repo2": {
		Name:        "repo2",
		Description: "Second repository",
		Visibility:  "private",
		CreatedAt:   time.Date(2023, 3, 10, 9, 15, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2023, 4, 5, 10, 45, 0, 0, time.UTC),
		Size:        850,
		Readme:      "# Repository 2\n\nThis is the second repository.",
		License:     "Apache-2.0",
		Contributors: []string{"user3"},
	},
}

// getRepositoryHandler handles GET /api/repositories/{org}/{repo}
func getRepositoryHandler(w http.ResponseWriter, r *http.Request) {
	// Extract organization and repository from URL
	parts := splitPath(r.URL.Path)
	if len(parts) != 3 {
		http.Error(w, "Invalid path format", http.StatusBadRequest)
		return
	}
	
	org := parts[1]
	repo := parts[2]
	key := org + "/" + repo
	
	// Check if repository exists
	repository, exists := repositories[key]
	if !exists {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}
	
	// Check authentication for private repositories
	if repository.Visibility == "private" {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer valid-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}
	
	// Return repository details
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(repository)
}

// splitPath splits a URL path into parts
func splitPath(path string) []string {
	parts := []string{}
	for _, part := range strings.Split(path, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func main() {
	// Set up routes
	mux := http.NewServeMux()
	mux.HandleFunc("/api/repositories/{org}/{repo}", getRepositoryHandler)
	
	// Start server
	log.Println("Server starting on :8080...")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
