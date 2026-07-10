package tui

import (
	"path/filepath"
	"sort"

	"github.com/olli-io/kmux/internal/agent"
	"github.com/olli-io/kmux/internal/project"
	"github.com/olli-io/kmux/internal/status"
)

// section identifies which panel a row belongs to.
type section int

const (
	sectionSessions section = iota
	sectionProjects
)

// ungrouped holds sessions whose project prefix matches no ~/git project.
const ungrouped = "(ungrouped)"

// liveState is a row's aggregate session state, driving its name color. Ordered
// by precedence so a numeric max rolls several sessions up to the strongest state.
type liveState int

const (
	liveNone liveState = iota
	liveDetached
	liveAttached
)

func maxLive(a, b liveState) liveState {
	if b > a {
		return b
	}
	return a
}

// row is one visible line in the dashboard tree. Both panels' rows share one flat
// slice so a single cursor traverses them; section says which panel.
type row struct {
	section     section
	depth       int    // indent level
	key         string // collapse-state key; empty for leaves
	collapsible bool
	label       string // pre-styled label text
	badge       string // pre-styled agent badge with attach state, e.g. ACC/DOC (sessions only)
	mark        string // pre-styled attention glyph: what the agent is doing (sessions only)

	// dir is the directory to operate in, set on project-section leaves. session is
	// the agent session to focus, create, or kill; empty on folder headers.
	dir     string
	session string
}

// sessionGroup collects one project's sessions: those on the main worktree, and
// those keyed by worktree segment.
type sessionGroup struct {
	main []string
	wts  map[string][]string
}

// groupSessions buckets sessions by matched project and worktree segment. names
// holds the projects' main-worktree paths.
func groupSessions(sessions, names []string) (map[string]*sessionGroup, []string) {
	groups := map[string]*sessionGroup{}
	var order []string
	get := func(p string) *sessionGroup {
		g, ok := groups[p]
		if !ok {
			g = &sessionGroup{wts: map[string][]string{}}
			groups[p] = g
			order = append(order, p)
		}
		return g
	}
	for _, s := range sessions {
		proj, wt, ok := agent.MatchProject(s, names)
		if !ok {
			proj, wt = ungrouped, "" // list flat under the ungrouped node
		}
		g := get(proj)
		if wt == "" {
			g.main = append(g.main, s)
		} else {
			g.wts[wt] = append(g.wts[wt], s)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		// Real projects sort alphabetically; the ungrouped bucket sinks to the end.
		if (order[i] == ungrouped) != (order[j] == ungrouped) {
			return order[j] == ungrouped
		}
		return order[i] < order[j]
	})
	return groups, order
}

// sessionsOf returns a group's sessions in display order: main-worktree first,
// then worktree sessions by segment then name.
func sessionsOf(g *sessionGroup) []string {
	out := append([]string(nil), g.main...)
	sort.Strings(out)
	wtNames := make([]string, 0, len(g.wts))
	for w := range g.wts {
		wtNames = append(wtNames, w)
	}
	sort.Strings(wtNames)
	for _, w := range wtNames {
		ss := append([]string(nil), g.wts[w]...)
		sort.Strings(ss)
		out = append(out, ss...)
	}
	return out
}

// buildSessionRows flattens sessions into project > session rows, mirroring the
// Projects pane: a single-session project is a bare leaf, several become a
// collapsible folder. Folders sort first, leaves next, ungrouped last.
func buildSessionRows(sessions, names []string, collapsed map[string]bool, attention map[string]status.AttentionState, attached, detached func(string) bool, deco rowDeco) []row {
	groups, order := groupSessions(sessions, names)

	var rows []row
	emitFolder := func(p string, ss []string) {
		pkey := "sess:" + p
		rows = append(rows, row{
			section:     sectionSessions,
			key:         pkey,
			depth:       1,
			collapsible: true,
			label:       deco.sessionFolder(filepath.Base(p), !collapsed[pkey]),
		})
		if collapsed[pkey] {
			return
		}
		for _, s := range ss {
			rows = append(rows, deco.session(s, 2, attention[s], attached(s), detached(s)))
		}
	}
	emitLeaf := func(s string) {
		rows = append(rows, deco.session(s, 1, attention[s], attached(s), detached(s)))
	}
	emit := func(p string, ss []string) {
		if len(ss) > 1 {
			emitFolder(p, ss)
		} else {
			emitLeaf(ss[0])
		}
	}

	// Split into folders and leaves, preserving alphabetical order; the ungrouped
	// bucket is emitted last regardless of size.
	type grp struct {
		name string
		ss   []string
	}
	var folders, leaves []grp
	var ung *grp
	for _, p := range order {
		ss := sessionsOf(groups[p])
		switch {
		case p == ungrouped:
			g := grp{p, ss}
			ung = &g
		case len(ss) > 1:
			folders = append(folders, grp{p, ss})
		default:
			leaves = append(leaves, grp{p, ss})
		}
	}
	for _, f := range folders {
		emitFolder(f.name, f.ss)
	}
	for _, l := range leaves {
		emitLeaf(l.ss[0])
	}
	if ung != nil {
		emit(ung.name, ung.ss)
	}
	return rows
}

// buildProjectRows flattens projects into rows: a project with no linked worktrees
// is a single leaf; a multi-worktree project is a collapsible folder listing the
// main worktree first, then each linked worktree.
func buildProjectRows(projects []project.Project, collapsed map[string]bool, live func(string) liveState, deco rowDeco) []row {
	// Folders sort to the top, single-worktree leaves after; order preserved.
	ordered := make([]project.Project, 0, len(projects))
	for _, p := range projects {
		if len(p.Worktrees) > 0 {
			ordered = append(ordered, p)
		}
	}
	for _, p := range projects {
		if len(p.Worktrees) == 0 {
			ordered = append(ordered, p)
		}
	}

	var rows []row
	for _, p := range ordered {
		mainSession := agent.ExpectedSession(p.Path, "")
		if len(p.Worktrees) == 0 {
			rows = append(rows, row{
				section: sectionProjects,
				label:   deco.projectLeaf(p, live(mainSession)),
				dir:     p.Path,
				session: mainSession,
			})
			continue
		}

		// The collapsed header rolls up its worktrees: green if any has a live pane,
		// red if any has only a detached one.
		folderLive := live(mainSession)
		for _, w := range p.Worktrees {
			folderLive = maxLive(folderLive, live(agent.ExpectedSession(p.Path, w.Name)))
		}

		pkey := "proj:" + p.Name
		rows = append(rows, row{
			section:     sectionProjects,
			key:         pkey,
			collapsible: true,
			label:       deco.projectFolder(p, !collapsed[pkey], folderLive),
		})
		if collapsed[pkey] {
			continue
		}
		// Main worktree first, then the linked worktrees.
		rows = append(rows, row{
			section: sectionProjects,
			depth:   1,
			label:   deco.mainWorktree(p, live(mainSession)),
			dir:     p.Path,
			session: mainSession,
		})
		for _, w := range p.Worktrees {
			wtSession := agent.ExpectedSession(p.Path, w.Name)
			rows = append(rows, row{
				section: sectionProjects,
				depth:   1,
				label:   deco.worktree(w, live(wtSession)),
				dir:     w.Path,
				session: wtSession,
			})
		}
	}
	return rows
}
