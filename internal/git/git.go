// Package git provides Git utility functions for Chief.
package git

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GetCurrentBranch returns the current git branch name for a directory.
func GetCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// IsProtectedBranch returns true if the branch name is main or master.
func IsProtectedBranch(branch string) bool {
	return branch == "main" || branch == "master"
}

// CreateBranch creates a new branch and switches to it.
func CreateBranch(dir, branchName string) error {
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = dir
	return cmd.Run()
}

// BranchExists returns true if a branch with the given name exists.
func BranchExists(dir, branchName string) (bool, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", branchName)
	cmd.Dir = dir
	err := cmd.Run()
	if err != nil {
		// Branch doesn't exist
		return false, nil
	}
	return true, nil
}

// IsGitRepo returns true if the directory is inside a git repository.
func IsGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// CommitCount returns the number of commits on branch that are not on the default branch.
// Returns 0 if the count cannot be determined.
func CommitCount(repoDir, branch string) int {
	defaultBranch, err := GetDefaultBranch(repoDir)
	if err != nil {
		return 0
	}
	cmd := exec.Command("git", "rev-list", "--count", defaultBranch+".."+branch)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return count
}

// GetDiff returns the git diff output for the working directory.
// It shows the diff between the current branch and its merge base with the default branch.
// If on main/master or if merge-base fails, it shows the last few commits' diff.
func GetDiff(dir string) (string, error) {
	branch, err := GetCurrentBranch(dir)
	if err != nil {
		return "", err
	}

	// If on a feature branch, diff against merge-base with main/master
	if !IsProtectedBranch(branch) {
		baseBranch, err := GetDefaultBranch(dir)
		if err == nil && baseBranch != "" {
			mergeBase, err := getMergeBase(dir, baseBranch, "HEAD")
			if err == nil && mergeBase != "" {
				return getDiffOutput(dir, mergeBase, "HEAD")
			}
		}
	}

	// Fallback: show diff of recent commits (last 10)
	return getDiffOutput(dir, "HEAD~10", "HEAD")
}

// GetDiffStats returns a short diffstat summary.
func GetDiffStats(dir string) (string, error) {
	branch, err := GetCurrentBranch(dir)
	if err != nil {
		return "", err
	}

	if !IsProtectedBranch(branch) {
		baseBranch, err := GetDefaultBranch(dir)
		if err == nil && baseBranch != "" {
			mergeBase, err := getMergeBase(dir, baseBranch, "HEAD")
			if err == nil && mergeBase != "" {
				cmd := exec.Command("git", "diff", "--stat", mergeBase, "HEAD")
				cmd.Dir = dir
				output, err := cmd.Output()
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(string(output)), nil
			}
		}
	}

	cmd := exec.Command("git", "diff", "--stat", "HEAD~10", "HEAD")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// getMergeBase returns the merge base commit between two refs.
func getMergeBase(dir, ref1, ref2 string) (string, error) {
	cmd := exec.Command("git", "merge-base", ref1, ref2)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// getDiffOutput returns the full diff between two refs.
func getDiffOutput(dir, from, to string) (string, error) {
	cmd := exec.Command("git", "diff", from, to)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
// CloneRepo clones a repository to the target directory.
func CloneRepo(url, targetDir, token string) error {
	cloneURL := url
	if token != "" && strings.HasPrefix(url, "http") {
		// Embed token for authentication if it's an HTTP URL
		// https://host.com/repo -> https://token@host.com/repo
		cloneURL = strings.Replace(url, "://", "://"+token+"@", 1)
	}

	cmd := exec.Command("git", "clone", cloneURL, targetDir)
	return cmd.Run()
}

// InitRepo initializes a new git repository in the directory.
func InitRepo(dir string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	return cmd.Run()
}
// GetRemoteURL returns the URL of the given remote for the repository.
func GetRemoteURL(dir, remote string) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", remote)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// ParseGithubURL extracts the owner and repo name from a GitHub URL.
// Supports both HTTPS (https://github.com/owner/repo.git) and
// SSH (git@github.com:owner/repo.git) formats.
func ParseGithubURL(rawURL string) (owner, repo string, err error) {
	rawURL = strings.TrimSuffix(rawURL, ".git")

	// SSH: git@github.com:owner/repo
	if strings.HasPrefix(rawURL, "git@") {
		// git@github.com:owner/repo
		parts := strings.SplitN(rawURL, ":", 2)
		if len(parts) == 2 {
			ownerRepo := strings.SplitN(parts[1], "/", 2)
			if len(ownerRepo) == 2 {
				return ownerRepo[0], ownerRepo[1], nil
			}
		}
		return "", "", fmt.Errorf("invalid SSH GitHub URL: %s", rawURL)
	}

	// HTTPS: https://github.com/owner/repo
	trimmed := strings.TrimPrefix(rawURL, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) >= 3 {
		return parts[1], parts[2], nil
	}
	return "", "", fmt.Errorf("invalid GitHub URL: %s", rawURL)
}
