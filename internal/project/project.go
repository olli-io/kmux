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
// "[kmux][CC]<projectPath>@<segment>" (see ExpectedSession / MatchProject). Stripping the
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

// ScanProjectsAt rescans only the given main-worktree paths (e.g. the projects
// that currently have a running session) and returns fresh Project values,
// sorted by name. It is the steady-state refresh: kmux does one full ScanProjects
// sweep at launch, then rescans only active projects on each tick, so a recurring
// scan spends git calls on the repos being worked in rather than all of ~/git. A
// path that no longer resolves to a git repository is skipped (best-effort, like
// the rest of a scan) rather than failing the batch.
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

// worktreeStatus reports the working-tree and upstream-sync state of the checkout
// at dir in a single git invocation: `git status --porcelain=v2 --branch` returns
// both the changed-file entries (any of which means dirty) and, in its `# branch.*`
// header, the upstream and ahead/behind counts — folding what used to be a separate
// `status --porcelain` plus `rev-list --count` into one call. upstream is false
// when no upstream is configured (a fresh branch, a detached HEAD), in which case
// git emits no `# branch.ab` line and ahead/behind stay zero. Best-effort: any git
// error reads as clean with no upstream so a status hiccup never fails a scan.
func worktreeStatus(dir string) (dirty bool, ahead, behind int, upstream bool) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain=v2", "--branch").Output()
	if err != nil {
		return false, 0, 0, false
	}
	return parseStatus(string(out))
}

// parseStatus reads `git status --porcelain=v2 --branch` output. The `# branch.*`
// header lines carry the upstream and ahead/behind counts; any other (non-`#`)
// line is a changed/untracked/unmerged entry, so its mere presence means dirty.
func parseStatus(out string) (dirty bool, ahead, behind int, upstream bool) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "# branch.upstream "):
			upstream = true
		case strings.HasPrefix(line, "# branch.ab "):
			// Format: "# branch.ab +<ahead> -<behind>". Present only with an upstream.
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) == 2 {
				ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
		case strings.HasPrefix(line, "# "):
			// Other header lines (branch.oid/head): not status-relevant here.
		default:
			// Any non-header line is a changed/untracked/unmerged entry (1/2/u/?),
			// so the worktree has uncommitted changes.
			dirty = true
		}
	}
	return dirty, ahead, behind, upstream
}

// markStatus fills the working-tree (Dirty) and upstream-sync (Ahead/Behind/
// Upstream) status of a project and each of its worktrees from the checkout at
// each path. It is the only place a scan spends git status calls per worktree,
// kept to one invocation each (see worktreeStatus).
func markStatus(p *Project) {
	p.Dirty, p.Ahead, p.Behind, p.Upstream = worktreeStatus(p.Path)
	for i := range p.Worktrees {
		p.Worktrees[i].Dirty, p.Worktrees[i].Ahead, p.Worktrees[i].Behind, p.Worktrees[i].Upstream = worktreeStatus(p.Worktrees[i].Path)
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
//
// Worktree topology (which worktrees exist, their branches) changes only on
// `git worktree add/remove`, branch checkout, commit, or fetch — all of which
// bump the main worktree's `.git` directory mtime. Working-tree edits (which flip
// the dirty bit) do NOT, so dirty/ahead/behind are deliberately NOT cached here —
// they are recomputed every scan by markStatus. This cache only skips the
// `git worktree list` spawn when `.git` is unchanged since the last scan, which is
// the common steady-state case (topology is stable between edits), cutting one git
// process per project per scan.
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

// gitMtime returns the modification time (unix nanos) of the main worktree's
// `.git` directory at dir, and whether it could be read. Git updates this
// directory on any ref/HEAD/worktree change (commit, checkout, fetch, worktree
// add/remove), so it is a reliable change signal for worktree topology — unlike a
// working-tree edit, which changes no `.git` mtime and so is left to the always-run
// status pass. A `.git` file (linked worktree/submodule) or a stat error yields
// ok=false, so those simply skip the cache and always re-list.
func gitMtime(dir string) (int64, bool) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil || !info.IsDir() {
		return 0, false
	}
	return info.ModTime().UnixNano(), true
}

// topoEntry caches a project's worktree topology (branch + linked worktrees) keyed
// by the `.git` mtime it was read at.
type topoEntry struct {
	mtime     int64
	branch    string
	worktrees []Worktree
}

// topoCache memoizes worktree topology per main-worktree path so a scan can skip
// the `git worktree list` spawn when `.git` is unchanged. It is guarded by its own
// mutex: the dashboard's scanning guard already serializes scans, but the lock
// keeps the cache safe if that ever changes or another caller scans concurrently.
var (
	topoMu    sync.Mutex
	topoCache = map[string]topoEntry{}
)

// lookupTopo returns the cached topology for dir if it was recorded at the same
// `.git` mtime, and whether it hit. The returned worktree slice is a fresh copy so
// a caller (which sets per-worktree status fields on it) can never mutate the
// cached entry.
func lookupTopo(dir string, mtime int64) (branch string, wts []Worktree, hit bool) {
	topoMu.Lock()
	defer topoMu.Unlock()
	e, ok := topoCache[dir]
	if !ok || e.mtime != mtime {
		return "", nil, false
	}
	return e.branch, append([]Worktree(nil), e.worktrees...), true
}

// storeTopo records dir's topology under the `.git` mtime it was read at. It stores
// a copy so a later mutation of the caller's slice can't corrupt the cache.
func storeTopo(dir string, mtime int64, branch string, wts []Worktree) {
	topoMu.Lock()
	defer topoMu.Unlock()
	topoCache[dir] = topoEntry{
		mtime:     mtime,
		branch:    branch,
		worktrees: append([]Worktree(nil), wts...),
	}
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
