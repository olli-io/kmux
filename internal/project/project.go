package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/olli-io/kmux/internal/config"
)

// Worktree is a linked git worktree of a project (never the main worktree).
type Worktree struct {
	Name     string // short name relative to the project
	Path     string
	Branch   string // short branch name, or "(detached)"
	Dirty    bool   // has staged or unstaged changes
	Ahead    int    // commits ahead of upstream
	Behind   int    // commits behind upstream
	Upstream bool   // an upstream branch is configured
}

// worktreeSegment strips a leading "<project><sep>" (sep one of ._-) from a
// worktree's basename so its row resolves to the same session name tmux carries;
// without the strip the row never matches and launching it spawns a duplicate.
// A basename that doesn't carry the prefix is returned unchanged.
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
	Dirty     bool   // main worktree has staged or unstaged changes
	Ahead     int    // main worktree commits ahead of upstream
	Behind    int    // main worktree commits behind upstream
	Upstream  bool   // an upstream branch is configured
	Worktrees []Worktree
}

func projectsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "git")
}

// ScanProjects lists every git repo directly under ~/git, sorted by name, with
// its linked worktrees. A missing ~/git yields an empty slice, not an error.
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
		// Linked worktrees show up as sibling folders here too, but listWorktrees
		// attaches them to their parent, so skipping them avoids double-listing.
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

	// Fold in extra project folders from config, deduped against the ~/git scan
	// and one another. Bad entries are skipped rather than failing the scan.
	cfg, err := config.LoadConfig()
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

// ScanProjectsAt rescans only the given main-worktree paths (the projects with a
// running session), sorted by name. It is the steady-state refresh that avoids
// re-sweeping all of ~/git. A path that no longer resolves to a git repo is skipped.
func ScanProjectsAt(paths []string) []Project {
	out := make([]Project, 0, len(paths))
	for _, dir := range paths {
		p, err := ScanProject(dir)
		if err != nil {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ScanProject builds the Project for the git repo containing dir (main worktree,
// a linked worktree, or any subdirectory). The main worktree is always listed
// first in --porcelain output, so it anchors the project.
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

// worktreeStatus reports dirty and upstream-sync state in one git call:
// `status --porcelain=v2 --branch` yields both the changed-file entries and the
// ahead/behind counts. Best-effort: any git error reads as clean with no upstream.
func worktreeStatus(dir string) (dirty bool, ahead, behind int, upstream bool) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain=v2", "--branch").Output()
	if err != nil {
		return false, 0, 0, false
	}
	return parseStatus(string(out))
}

// parseStatus reads `git status --porcelain=v2 --branch`: `# branch.*` headers
// carry the upstream and ahead/behind counts; any other line means dirty.
func parseStatus(out string) (dirty bool, ahead, behind int, upstream bool) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "# branch.upstream "):
			upstream = true
		case strings.HasPrefix(line, "# branch.ab "):
			// Format: "# branch.ab +<ahead> -<behind>", present only with an upstream.
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) == 2 {
				ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
		case strings.HasPrefix(line, "# "):
			// Other headers (branch.oid/head): not status-relevant.
		default:
			// Any non-header line is a changed/untracked/unmerged entry, so it's dirty.
			dirty = true
		}
	}
	return dirty, ahead, behind, upstream
}

// markStatus fills the Dirty and Ahead/Behind/Upstream status of a project and
// each of its worktrees, one git status call per checkout.
func markStatus(p *Project) {
	p.Dirty, p.Ahead, p.Behind, p.Upstream = worktreeStatus(p.Path)
	for i := range p.Worktrees {
		p.Worktrees[i].Dirty, p.Worktrees[i].Ahead, p.Worktrees[i].Behind, p.Worktrees[i].Upstream = worktreeStatus(p.Worktrees[i].Path)
	}
}

// firstWorktreePath returns the path from the first `worktree ` record, which git
// always emits for the main worktree.
func firstWorktreePath(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			return strings.TrimPrefix(line, "worktree ")
		}
	}
	return ""
}

// isMainWorktree reports whether dir's .git is a real directory. Linked worktrees
// and submodules have a .git *file*, so they're excluded as standalone projects.
func isMainWorktree(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// listWorktrees returns the main worktree's branch and its linked worktrees.
// Best-effort: any git error yields an empty branch and no worktrees.
//
// The topology cache skips the `git worktree list` spawn when `.git` is unchanged
// since the last scan. Only topology is cached — dirty/ahead/behind change without
// touching `.git` mtime, so markStatus recomputes them every scan.
func listWorktrees(dir string) (string, []Worktree) {
	mtime, ok := gitMtime(dir)
	if ok {
		if branch, wts, hit := lookupTopo(dir, mtime); hit {
			return branch, wts
		}
	}
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return "", nil
	}
	branch, wts := parseWorktrees(string(out), dir)
	if ok {
		storeTopo(dir, mtime, branch, wts)
	}
	return branch, wts
}

// gitMtime returns the mtime (unix nanos) of dir's `.git` directory and whether it
// could be read. Git bumps it on any ref/HEAD/worktree change, so it signals a
// topology change. A `.git` file or a stat error yields ok=false, skipping the cache.
func gitMtime(dir string) (int64, bool) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil || !info.IsDir() {
		return 0, false
	}
	return info.ModTime().UnixNano(), true
}

// topoEntry caches a project's worktree topology keyed by the `.git` mtime it was
// read at.
type topoEntry struct {
	mtime     int64
	branch    string
	worktrees []Worktree
}

// topoCache memoizes worktree topology per path to skip the `git worktree list`
// spawn when `.git` is unchanged. Its own mutex keeps it safe against concurrent scans.
var (
	topoMu    sync.Mutex
	topoCache = map[string]topoEntry{}
)

// lookupTopo returns the cached topology for dir if recorded at the same `.git`
// mtime. The returned slice is a fresh copy so the caller can't mutate the cache.
func lookupTopo(dir string, mtime int64) (branch string, wts []Worktree, hit bool) {
	topoMu.Lock()
	defer topoMu.Unlock()
	e, ok := topoCache[dir]
	if !ok || e.mtime != mtime {
		return "", nil, false
	}
	return e.branch, append([]Worktree(nil), e.worktrees...), true
}

// storeTopo records dir's topology under its `.git` mtime, storing a copy so a
// later mutation of the caller's slice can't corrupt the cache.
func storeTopo(dir string, mtime int64, branch string, wts []Worktree) {
	topoMu.Lock()
	defer topoMu.Unlock()
	topoCache[dir] = topoEntry{
		mtime:     mtime,
		branch:    branch,
		worktrees: append([]Worktree(nil), wts...),
	}
}

// parseWorktrees parses `git worktree list --porcelain`, returning the main
// worktree's branch (the record whose path equals mainPath) and the linked worktrees.
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
			flush()
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
