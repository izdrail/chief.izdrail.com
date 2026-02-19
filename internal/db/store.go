package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func NewStore(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			description TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS user_stories (
			id TEXT PRIMARY KEY,
			project_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			description TEXT,
			acceptance_criteria TEXT,
			priority INTEGER DEFAULT 0,
			passes BOOLEAN DEFAULT 0,
			in_progress BOOLEAN DEFAULT 0,
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS agent_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_name TEXT NOT NULL,
			message TEXT NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
	}

	for _, q := range queries {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}

	// Add title column if it doesn't exist (ignoring error if it already exists)
	s.db.Exec("ALTER TABLE projects ADD COLUMN title TEXT;")
	s.db.Exec("ALTER TABLE projects ADD COLUMN repo_url TEXT;")

	return nil
}

// UserStory helper type for DB
type StoryDB struct {
	ID                 string
	ProjectID          int64
	Title              string
	Description        string
	AcceptanceCriteria []string
	Priority           int
	Passes             bool
	InProgress         bool
}

func (s *Store) SaveProject(name, title, description, repoURL string) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO projects (name, title, description, repo_url, updated_at) 
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET 
			title = excluded.title,
			description = excluded.description,
			repo_url = excluded.repo_url,
			updated_at = CURRENT_TIMESTAMP
	`, name, title, description, repoURL)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetProject(name string) (int64, string, string, string, error) {
	var id int64
	var title, desc, repoURL sql.NullString
	err := s.db.QueryRow("SELECT id, title, description, repo_url FROM projects WHERE name = ?", name).Scan(&id, &title, &desc, &repoURL)
	return id, title.String, desc.String, repoURL.String, err
}

func (s *Store) GetProjectID(name string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT id FROM projects WHERE name = ?", name).Scan(&id)
	return id, err
}

func (s *Store) SaveStory(projectID int64, story StoryDB) error {
	ac, _ := json.Marshal(story.AcceptanceCriteria)
	_, err := s.db.Exec(`
		INSERT INTO user_stories (id, project_id, title, description, acceptance_criteria, priority, passes, in_progress)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			acceptance_criteria = excluded.acceptance_criteria,
			priority = excluded.priority,
			passes = excluded.passes,
			in_progress = excluded.in_progress
	`, story.ID, projectID, story.Title, story.Description, string(ac), story.Priority, story.Passes, story.InProgress)
	return err
}

func (s *Store) DeleteStory(projectID int64, storyID string) error {
	_, err := s.db.Exec("DELETE FROM user_stories WHERE project_id = ? AND id = ?", projectID, storyID)
	return err
}

func (s *Store) GetStories(projectID int64) ([]StoryDB, error) {
	rows, err := s.db.Query("SELECT id, title, description, acceptance_criteria, priority, passes, in_progress FROM user_stories WHERE project_id = ? ORDER BY priority ASC", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stories []StoryDB
	for rows.Next() {
		var story StoryDB
		var acStr string
		if err := rows.Scan(&story.ID, &story.Title, &story.Description, &acStr, &story.Priority, &story.Passes, &story.InProgress); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(acStr), &story.AcceptanceCriteria)
		stories = append(stories, story)
	}
	return stories, nil
}

func (s *Store) AddLog(projectName, message string) error {
	_, err := s.db.Exec("INSERT INTO agent_logs (project_name, message) VALUES (?, ?)", projectName, message)
	return err
}

func (s *Store) GetLogs(projectName string, limit int) ([]string, error) {
	rows, err := s.db.Query("SELECT message, timestamp FROM agent_logs WHERE project_name = ? ORDER BY timestamp DESC LIMIT ?", projectName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []string
	for rows.Next() {
		var msg string
		var ts time.Time
		if err := rows.Scan(&msg, &ts); err != nil {
			return nil, err
		}
		logs = append([]string{fmt.Sprintf("%s: %s", ts.Format(time.RFC3339), msg)}, logs...)
	}
	return logs, nil
}

type ProjectInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (s *Store) ListProjects() ([]ProjectInfo, error) {
	rows, err := s.db.Query("SELECT name, title, description FROM projects ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []ProjectInfo
	for rows.Next() {
		var p ProjectInfo
		var title, desc sql.NullString
		if err := rows.Scan(&p.Name, &title, &desc); err != nil {
			return nil, err
		}
		p.Title = title.String
		p.Description = desc.String
		if p.Title == "" {
			p.Title = p.Name
		}
		projects = append(projects, p)
	}
	return projects, nil
}

// DeleteProject removes a project and all related data (stories, logs) from the database.
func (s *Store) DeleteProject(name string) error {
	id, _, _, _, err := s.GetProject(name)
	if err != nil {
		// Project doesn't exist in DB, that's fine
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	// Delete stories
	if _, err := tx.Exec("DELETE FROM user_stories WHERE project_id = ?", id); err != nil {
		tx.Rollback()
		return err
	}

	// Delete logs
	if _, err := tx.Exec("DELETE FROM agent_logs WHERE project_name = ?", name); err != nil {
		tx.Rollback()
		return err
	}

	// Delete project
	if _, err := tx.Exec("DELETE FROM projects WHERE id = ?", id); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
