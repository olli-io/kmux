package status

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/olli-io/kmux/internal/config"
	"github.com/olli-io/kmux/internal/project"
)

// projectsFile returns the path to kmux's cached project list
// (~/.config/kmux/projects.json), creating the directory as needed. It lives
// alongside state.json (see stateFile) but is a separate file on purpose: the
// idle/detached state churns on nearly every tick, whereas the project list
// changes rarely, so keeping them apart avoids rewriting the (larger) project
// cache for every idle-clock update and lets the idler load just the list it
// needs without parsing session state.
func projectsFile() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "projects.json"), nil
}

// cachedProjects is the on-disk shape of the project cache the dashboard writes
// after each full scan, so a freshly-spawned idler can paint its picker without
// re-running project.ScanProjects (dozens of serial git calls). The list is the
// full, unscoped set the dashboard last scanned, including config extras.
type cachedProjects struct {
	Projects []project.Project `json:"projects"`
}

// SaveProjects persists the full scanned project list so the next idler launch
// can populate its picker from disk instead of shelling out to git for every
// repo under ~/git. Only an unscoped dashboard should call this — a scoped
// dashboard holds a single project and would clobber the full list. Best-effort:
// callers treat a write error as non-fatal (the idler just falls back to a live
// scan).
func SaveProjects(projects []project.Project) error {
	path, err := projectsFile()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cachedProjects{Projects: projects}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadProjects reads the project list the dashboard last cached. A missing cache
// (no dashboard has run yet) yields a nil slice and ok=false so the caller can
// fall back to a live project.ScanProjects. A read/parse error is returned so an
// unexpected failure is visible rather than silently masked as "no cache".
func LoadProjects() (projects []project.Project, ok bool, err error) {
	path, err := projectsFile()
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var c cachedProjects
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false, err
	}
	return c.Projects, len(c.Projects) > 0, nil
}
