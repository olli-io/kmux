package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Worktree is a linked git worktree of a project (never the main worktree).
type Worktree struct {
	Name     string // short name relative to the project (see worktreeSegment)
	Path     string
	Branch   string // short branch name, or "(detached)"
	Dirty    bool   // has staged or unstaged changes (see isDirty)
	Ahead    int    // commits ahead of upstream (see gitSync)
	Behind   int    // commits behind upstream (see gitSync)
	Upstream bool   // an upstream branch is configured (see gitSync)
}

// worktreeSegment derives a worktree's short name relative to its project: the
// directory basename with a leading "<project><sep>" stripped, where sep is one
// of '.', '_', or '-'. Worktrees are conventionally placed in sibling dirs named
// "<project>.<branch>" (and similar), yet the tmux session convention is
// "<project>/<segment>~cl" (see expectedSession / matchProject). Stripping the
// redundant project prefix keeps the two in sync, so a worktree's row resolves to
// the same session name its live tmux session carries — without it the project
// row never matches its session (no active coloring) and launching it spawns a
// duplicate instead of focusing the existing pane. A basename that doesn't carry
// the prefix is returned unchanged.
func worktreeSegment(base, project string) string {
	rest, ok := strings.CutPrefix(base, project)
	if !ok || rest == "" || !strings.ContainsRune("._-", rune(rest[0])) {
		return base
	}
	if seg := rest[1:]; seg != "" {
		return seg
	}
	return base
}

// Project is a git repository directly under the projects root, together with
// its linked worktrees.
type Project struct {
	Name      string
	Path      string
	Branch    string // current branch of the main worktree, or "(detached)"
	Dirty     bool   // main worktree has staged or unstaged changes (see isDirty)
	Ahead     int    // main worktree commits ahead of upstream (see gitSync)
	Behind    int    // main worktree commits behind upstream (see gitSync)
	Upstream  bool   // an upstream branch is configured (see gitSync)
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
	seen := map[string]bool{}
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
		p := Project{
			Name:      e.Name(),
			Path:      path,
			Branch:    branch,
			Worktrees: worktrees,
		}
		markStatus(&p)
		projects = append(projects, p)
		seen[path] = true
	}

	// Fold in any extra project folders from the config file. Each is resolved to
	// its main worktree, deduped against the ~/git scan and one another, so a
	// configured folder that also lives under ~/git isn't listed twice. Bad
	// entries (missing dirs, non-repos) are skipped rather than failing the scan.
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	for _, dir := range cfg.Projects {
		p, err := ScanProject(dir)
		if err != nil || seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		projects = append(projects, *p)
	}

	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

// ScanProject builds the Project for the git repository containing dir, together
// with its linked worktrees. dir may be the main worktree, a linked worktree, or
// any subdirectory of either: git resolves the whole worktree set regardless,
// and the main worktree is always listed first in --porcelain output, so it
// anchors the project. An error is returned when dir is not inside a git repo.
func ScanProject(dir string) (*Project, error) {
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository", dir)
	}
	root := firstWorktreePath(string(out))
	if root == "" {
		return nil, fmt.Errorf("%s is not a git repository", dir)
	}
	branch, worktrees := parseWorktrees(string(out), root)
	p := &Project{
		Name:      filepath.Base(root),
		Path:      root,
		Branch:    branch,
		Worktrees: worktrees,
	}
	markStatus(p)
	return p, nil
}

// isDirty reports whether the git worktree at dir has any uncommitted changes —
// staged, unstaged, or untracked. It runs the cheapest status query that still
// covers every change kind (`git status --porcelain`) and treats any output as
// dirty. Best-effort: a git error (not a repo, etc.) reads as clean so a status
// hiccup never fails a scan.
func isDirty(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
}

// gitSync reports how far the branch checked out at dir is ahead of and behind
// its upstream (origin): ahead counts local commits the upstream lacks, behind
// counts upstream commits the local branch lacks. upstream is false when no
// upstream is configured (a fresh branch, a detached HEAD), in which case ahead
// and behind are zero. Best-effort: any git error reads as no upstream so a
// status hiccup never fails a scan.
func gitSync(dir string) (ahead, behind int, upstream bool) {
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "--left-right", "@{upstream}...HEAD").Output()
	if err != nil {
		return 0, 0, false
	}
	// --left-right with @{upstream}...HEAD prints "<behind>\t<ahead>": the left
	// side counts commits unique to the upstream, the right side those unique to
	// HEAD.
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0, false
	}
	behind, _ = strconv.Atoi(fields[0])
	ahead, _ = strconv.Atoi(fields[1])
	return ahead, behind, true
}

// markStatus fills the working-tree (Dirty) and upstream-sync (Ahead/Behind/
// Upstream) status of a project and each of its worktrees from the checkout at
// each path. It is the only place a scan spends git status calls per worktree,
// kept to two cheap invocations each.
func markStatus(p *Project) {
	p.Dirty = isDirty(p.Path)
	p.Ahead, p.Behind, p.Upstream = gitSync(p.Path)
	for i := range p.Worktrees {
		p.Worktrees[i].Dirty = isDirty(p.Worktrees[i].Path)
		p.Worktrees[i].Ahead, p.Worktrees[i].Behind, p.Worktrees[i].Upstream = gitSync(p.Worktrees[i].Path)
	}
}

// firstWorktreePath returns the path from the first `worktree ` record of
// `git worktree list --porcelain`, which git always emits for the main worktree.
func firstWorktreePath(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			return strings.TrimPrefix(line, "worktree ")
		}
	}
	return ""
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
	project := filepath.Base(mainPath)
	var mainBranch string
	var wts []Worktree
	var cur Worktree

	flush := func() {
		switch {
		case cur.Path == "":
		case cur.Path == mainPath:
			mainBranch = cur.Branch
		default:
			cur.Name = worktreeSegment(filepath.Base(cur.Path), project)
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
