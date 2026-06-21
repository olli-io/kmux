package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// stateFile returns the path to kmux's persisted state file
// (~/.config/kmux/state.json), creating the directory as needed.
func stateFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kmux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// persistedState is the on-disk shape of the state kmux remembers across runs.
type persistedState struct {
	Detached []string `json:"detached"` // session names the user detached
}

// LoadDetached reads the set of detached session names persisted by a previous
// run. A missing state file yields an empty set, not an error.
func LoadDetached() (map[string]bool, error) {
	path, err := stateFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(st.Detached))
	for _, s := range st.Detached {
		set[s] = true
	}
	return set, nil
}

// SaveDetached persists the set of detached session names so they stay detached
// across restarts. The names are sorted for a stable, diff-friendly file.
func SaveDetached(detached map[string]bool) error {
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
	data, err := json.MarshalIndent(persistedState{Detached: names}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
