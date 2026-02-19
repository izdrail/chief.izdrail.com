package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetRepositoryHandler(t *testing.T) {
	// Test public repository
	req := httptest.NewRequest("GET", "/api/repositories/org1/repo1", nil)
	w := httptest.NewRecorder()
	getRepositoryHandler(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}
	
	var repo map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&repo); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	
	if repo["name"] != "repo1" {
		t.Errorf("Expected name 'repo1', got '%v'", repo["name"])
	}
	
	// Test private repository without auth
	req = httptest.NewRequest("GET", "/api/repositories/org2/repo2", nil)
	w = httptest.NewRecorder()
	getRepositoryHandler(w, req)
	
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status %d for private repo without auth, got %d", http.StatusUnauthorized, w.Code)
	}
	
	// Test private repository with auth
	req = httptest.NewRequest("GET", "/api/repositories/org2/repo2", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	w = httptest.NewRecorder()
	getRepositoryHandler(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d for private repo with auth, got %d", http.StatusOK, w.Code)
	}
	
	// Test non-existent repository
	req = httptest.NewRequest("GET", "/api/repositories/nonexistent/repo", nil)
	w = httptest.NewRecorder()
	getRepositoryHandler(w, req)
	
	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status %d for non-existent repo, got %d", http.StatusNotFound, w.Code)
	}
}

func TestRepositoryStructure(t *testing.T) {
	// Test that repository structure matches expected format
	req := httptest.NewRequest("GET", "/api/repositories/org1/repo1", nil)
	w := httptest.NewRecorder()
	getRepositoryHandler(w, req)
	
	var repo map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&repo); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	
	// Check required fields
	requiredFields := []string{"name", "description", "visibility", "createdAt", "updatedAt", "size"}
	for _, field := range requiredFields {
		if _, ok := repo[field]; !ok {
			t.Errorf("Missing required field: %s", field)
		}
	}
	
	// Check optional fields
	optionalFields := []string{"readme", "license", "contributors"}
	for _, field := range optionalFields {
		// These fields should be present but not required to fail if missing
		_ = field
	}
}
