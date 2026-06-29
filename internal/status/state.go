package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/olli-io/kmux/internal/config"
)

// stateFile returns the path to kmux's persisted state file
// (~/.config/kmux/state.json), creating the directory as needed. It shares
// config.ConfigDir so state and config always live in the same ~/.config/kmux.
func stateFile() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// persistedState is the on-disk shape of the state kmux remembers across runs.
type persistedState struct {
	Detached []string              `json:"detached"`       // session names the user detached
	Idle     map[string]IdleRecord `json:"idle,omitempty"` // session -> persisted idle clock
}

// LoadState reads the state persisted by a previous run: the set of detached
// session names and the per-session idle clocks (see IdleRecord). A missing state
// file yields empty, non-nil maps, not an error.
func LoadState() (detached map[string]bool, idle map[string]IdleRecord, err error) {
	path, err := stateFile()
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, map[string]IdleRecord{}, nil
		}
		return nil, nil, err
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, nil, err
	}
	detached = make(map[string]bool, len(st.Detached))
	for _, s := range st.Detached {
		detached[s] = true
	}
	if st.Idle == nil {
		st.Idle = map[string]IdleRecord{}
	}
	return detached, st.Idle, nil
}

// SaveState persists the detached-session set and the per-session idle clocks so
// both survive a restart. Detached names are sorted for a stable, diff-friendly
// file; the idle map is written verbatim.
func SaveState(detached map[string]bool, idle map[string]IdleRecord) error {
	path, err := stateFile()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(detached))
	for s, on := range detached {
		if on {
			names = append(names, s)
		}
	}
	sort.Strings(names)
	data, err := json.MarshalIndent(persistedState{Detached: names, Idle: idle}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
