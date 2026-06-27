package main

import (
	"sort"
	"strings"

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

// row is one visible line in the dashboard tree. Rows for both panels live in a
// single flat slice so a single cursor can traverse them; section tells the
// renderer which panel each belongs to.
type row struct {
	section     section
	depth       int    // indent level
	key         string // collapse-state key; empty for leaves
	collapsible bool
	label       string // pre-styled label text
	badge       string // pre-styled agent badge with attach state, e.g. ACC/DOC (sessions only)
	mark        string // pre-styled attention glyph: what the agent is doing (sessions only)

	// Actionable metadata. dir is the directory to operate in (lazygit, new
	// sessions), set on project-section leaves. session is the agent session name
	// to focus, create, or kill: the claude session for project-section leaves, the
	// row's own session for session-section leaves; empty on folder headers.
	dir     string
	session string
}

// sessionGroup collects the sessions of one project: those on the main worktree,
// and those keyed by worktree segment.
type sessionGroup struct {
	main []string
	wts  map[string][]string
}

// groupSessions buckets sessions by matched project and worktree segment.
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
		rem := strings.TrimSuffix(strings.TrimSuffix(s, "~cl"), "~oc")
		proj, wt, ok := agent.MatchProject(rem, names)
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

// sessionsOf returns a project group's sessions in display order: main-worktree
// sessions first, then worktree sessions ordered by worktree segment then session
// name (no intermediate worktree node).
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
// Projects pane's folder rules: a project with a single session is a bare leaf
// (no folder header), while a project with several sessions becomes a collapsible
// folder whose children hang directly off it. Folders sort to the top, single-
// session leaves after, and the ungrouped bucket sinks to the very end.
// attention carries each session's latest attention state (drives the leading
// status glyph). attached reports whether a session has a live pane; detached
// reports whether the user detached it (tmux alive, pane closed).
func buildSessionRows(sessions, names []string, collapsed map[string]bool, attention map[string]status.AttentionState, attached, detached func(string) bool, deco rowDeco) []row {
	groups, order := groupSessions(sessions, names)

	var rows []row
	emitFolder := func(p string, ss []string) {
		pkey := "sess:" + p
		rows = append(rows, row{
			section:     sectionSessions,
			key:         pkey,
			collapsible: true,
			label:       deco.sessionFolder(p, !collapsed[pkey]),
		})
		if collapsed[pkey] {
			return
		}
		for _, s := range ss {
			rows = append(rows, deco.session(s, 1, attention[s], attached(s), detached(s)))
		}
	}
	emitLeaf := func(s string) {
		rows = append(rows, deco.session(s, 0, attention[s], attached(s), detached(s)))
	}
	emit := func(p string, ss []string) {
		if len(ss) > 1 {
			emitFolder(p, ss)
		} else {
			emitLeaf(ss[0])
		}
	}

	// Split into multi-session folders and single-session leaves, preserving the
	// alphabetical order within each group; the ungrouped bucket is held back and
	// emitted last regardless of its size.
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

// buildProjectRows flattens projects into rows. A project with no linked
// worktrees is a single actionable leaf. A multi-worktree project becomes a
// collapsible folder whose expanded children list the main worktree first,
// then each linked worktree; every child is an actionable leaf.
func buildProjectRows(projects []project.Project, collapsed map[string]bool, hasSession func(string) bool, deco rowDeco) []row {
	// Folders (multi-worktree projects) sort to the top, single-worktree leaves
	// after; order within each group is preserved.
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
		mainSession := agent.ExpectedSession(p.Name, "")
		if len(p.Worktrees) == 0 {
			rows = append(rows, row{
				section: sectionProjects,
				label:   deco.projectLeaf(p, hasSession(mainSession)),
				dir:     p.Path,
				session: mainSession,
			})
			continue
		}

		// The collapsed folder header turns green when any of its worktrees
		// (main or linked) has a live session.
		folderActive := hasSession(mainSession)
		for _, w := range p.Worktrees {
			if hasSession(agent.ExpectedSession(p.Name, w.Name)) {
				folderActive = true
				break
			}
		}

		pkey := "proj:" + p.Name
		rows = append(rows, row{
			section:     sectionProjects,
			key:         pkey,
			collapsible: true,
			label:       deco.projectFolder(p, !collapsed[pkey], folderActive),
		})
		if collapsed[pkey] {
			continue
		}
		// Main worktree first, then the linked worktrees.
		rows = append(rows, row{
			section: sectionProjects,
			depth:   1,
			label:   deco.mainWorktree(p, hasSession(mainSession)),
			dir:     p.Path,
			session: mainSession,
		})
		for _, w := range p.Worktrees {
			wtSession := agent.ExpectedSession(p.Name, w.Name)
			rows = append(rows, row{
				section: sectionProjects,
				depth:   1,
				label:   deco.worktree(w, hasSession(wtSession)),
				dir:     w.Path,
				session: wtSession,
			})
		}
	}
	return rows
}
