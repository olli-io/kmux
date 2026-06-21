package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Worktree is a linked git worktree of a project (never the main worktree).
type Worktree struct {
	Name   string // directory basename
	Path   string
	Branch string // short branch name, or "(detached)"
}

// Project is a git repository directly under the projects root, together with
// its linked worktrees.
type Project struct {
	Name      string
	Path      string
	Branch    string // current branch of the main worktree, or "(detached)"
	Worktrees []Worktree
}

// projectsRoot is the directory scanned for git projects (~/git).
func projectsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "git")
}

// ScanProjects lists every git repo directly under ~/git (sorted by name)
// together with its linked worktrees. Non-repos and unreadable entries are
// skipped. A missing ~/git yields an empty slice, not an error.
func ScanProjects() ([]Project, error) {
	root := projectsRoot()
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var projects []Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name())
		// Only main worktrees are standalone projects. Linked worktrees show up
		// as sibling folders here too, but they're attached to their parent repo
		// via listWorktrees, so skipping them avoids double-listing.
		if !isMainWorktree(path) {
			continue
		}
		branch, worktrees := listWorktrees(path)
		projects = append(projects, Project{
			Name:      e.Name(),
			Path:      path,
			Branch:    branch,
			Worktrees: worktrees,
		})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

// isMainWorktree reports whether dir is the main worktree of a git repo, i.e.
// its .git entry is a real directory. Linked worktrees (and submodules) have a
// .git *file* pointing into the parent repo's .git/worktrees/; those are
// deliberately excluded so they aren't listed as standalone projects.
func isMainWorktree(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// listWorktrees returns the current branch of the main worktree at dir together
// with its linked worktrees. Best-effort: any git error yields an empty branch
// and no worktrees rather than failing the whole scan.
func listWorktrees(dir string) (string, []Worktree) {
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return "", nil
	}
	return parseWorktrees(string(out), dir)
}

// parseWorktrees parses `git worktree list --porcelain` output, returning the
// main worktree's branch (the record whose path equals mainPath) and the linked
// worktrees.
func parseWorktrees(out, mainPath string) (string, []Worktree) {
	var mainBranch string
	var wts []Worktree
	var cur Worktree

	flush := func() {
		switch {
		case cur.Path == "":
		case cur.Path == mainPath:
			mainBranch = cur.Branch
		default:
			cur.Name = filepath.Base(cur.Path)
			wts = append(wts, cur)
		}
		cur = Worktree{}
	}

	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush() // a new record begins; commit the previous one
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.Branch = "(detached)"
		}
	}
	flush()

	sort.Slice(wts, func(i, j int) bool { return wts[i].Name < wts[j].Name })
	return mainBranch, wts
}
