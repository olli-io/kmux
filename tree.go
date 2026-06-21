package main

import (
	"sort"
	"strings"
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
	badge       string // pre-styled agent badge (sessions only)
	mark        string // pre-styled attach mark (sessions only)

	// Actionable metadata for project-section leaves; empty on folders and
	// session rows. dir is the directory to operate in (lazygit, new sessions);
	// session is the claude session name to focus or create.
	dir     string
	session string
}

// expectedSession returns the claude session name for a project/worktree pair.
// wt is "" for the main worktree. It mirrors the naming convention parsed by
// matchProject and ListAgentSessions (a trailing _cl).
func expectedSession(project, wt string) string {
	if wt == "" {
		return tmuxSafe(project + "_cl")
	}
	return tmuxSafe(project + "_" + wt + "_cl")
}

// tmuxSafe rewrites a desired session name into the form tmux actually stores.
// tmux forbids '.' and ':' in session names and silently replaces them with
// '_'. Worktree directories often contain a '.' (e.g. repo.branch), so without
// this the computed name never matches the live tmux session and the row fails
// to register as active.
func tmuxSafe(name string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(name)
}

// matchProject finds the project whose name is the longest prefix of rem such
// that rem == name or rem starts with name+"_". It returns the project name and
// the trailing worktree segment ("" when rem == name). ok is false when no
// project matches.
func matchProject(rem string, names []string) (proj, wt string, ok bool) {
	best := ""
	for _, n := range names {
		if rem == n || strings.HasPrefix(rem, n+"_") {
			if len(n) > len(best) {
				best = n
			}
		}
	}
	if best == "" {
		return "", "", false
	}
	if rem == best {
		return best, "", true
	}
	return best, strings.TrimPrefix(rem, best+"_"), true
}

// projectNames extracts the project names (for prefix matching).
func projectNames(ps []Project) []string {
	n := make([]string, len(ps))
	for i, p := range ps {
		n[i] = p.Name
	}
	return n
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
		rem := strings.TrimSuffix(strings.TrimSuffix(s, "_cl"), "_oc")
		proj, wt, ok := matchProject(rem, names)
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

// buildSessionRows flattens sessions into project > worktree > session rows,
// honoring collapse state. attached reports whether a session has a live pane.
func buildSessionRows(sessions, names []string, collapsed map[string]bool, attached func(string) bool, deco rowDeco) []row {
	groups, order := groupSessions(sessions, names)

	var rows []row
	for _, p := range order {
		pkey := "sess:" + p
		rows = append(rows, row{section: sectionSessions, key: pkey, collapsible: true, label: p})
		if collapsed[pkey] {
			continue
		}
		g := groups[p]

		// Main-worktree sessions hang directly off the project.
		sort.Strings(g.main)
		for _, s := range g.main {
			rows = append(rows, deco.session(s, 1, attached(s)))
		}

		// Worktree sessions get an intermediate, collapsible worktree node.
		wtNames := make([]string, 0, len(g.wts))
		for w := range g.wts {
			wtNames = append(wtNames, w)
		}
		sort.Strings(wtNames)
		for _, w := range wtNames {
			wkey := pkey + "/" + w
			rows = append(rows, row{section: sectionSessions, depth: 1, key: wkey, collapsible: true, label: w})
			if collapsed[wkey] {
				continue
			}
			ss := g.wts[w]
			sort.Strings(ss)
			for _, s := range ss {
				rows = append(rows, deco.session(s, 2, attached(s)))
			}
		}
	}
	return rows
}

// buildProjectRows flattens projects into rows. A project with no linked
// worktrees is a single actionable leaf. A multi-worktree project becomes a
// collapsible folder whose expanded children list the main worktree first,
// then each linked worktree; every child is an actionable leaf.
func buildProjectRows(projects []Project, collapsed map[string]bool, hasSession func(string) bool, deco rowDeco) []row {
	var rows []row
	for _, p := range projects {
		mainSession := expectedSession(p.Name, "")
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
			if hasSession(expectedSession(p.Name, w.Name)) {
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
			wtSession := expectedSession(p.Name, w.Name)
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
