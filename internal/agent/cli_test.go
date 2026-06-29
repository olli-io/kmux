package agent

import "testing"

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    ParsedArgs
		wantErr bool
	}{
		{"empty", nil, ParsedArgs{}, false},
		{"path only", []string{"/g/x"}, ParsedArgs{Path: "/g/x"}, false},
		{"agent space", []string{"--agent", "claude"}, ParsedArgs{Agent: "claude"}, false},
		{"agent equals", []string{"--agent=opencode"}, ParsedArgs{Agent: "opencode"}, false},
		{"agent then path", []string{"--agent", "claude", "/g/x"}, ParsedArgs{Path: "/g/x", Agent: "claude"}, false},
		{"path then agent", []string{"/g/x", "--agent", "claude"}, ParsedArgs{Path: "/g/x", Agent: "claude"}, false},
		{"session space", []string{"--session", "claude"}, ParsedArgs{Agent: "claude", PrintSession: true}, false},
		{"session equals", []string{"--session=opencode"}, ParsedArgs{Agent: "opencode", PrintSession: true}, false},
		{"session with path", []string{"--session", "opencode", "/g/x"}, ParsedArgs{Path: "/g/x", Agent: "opencode", PrintSession: true}, false},
		{"agent missing value", []string{"--agent"}, ParsedArgs{}, true},
		{"session missing value", []string{"--session"}, ParsedArgs{}, true},
		{"bad kind", []string{"--agent", "vim"}, ParsedArgs{}, true},
		{"bad session kind", []string{"--session=vim"}, ParsedArgs{}, true},
		{"unknown flag", []string{"--nope"}, ParsedArgs{}, true},
		{"two paths", []string{"/a", "/b"}, ParsedArgs{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseArgs(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseArgs(%v) err = %v, wantErr %v", c.args, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if got != c.want {
				t.Errorf("ParseArgs(%v) = %+v, want %+v", c.args, got, c.want)
			}
		})
	}
}
