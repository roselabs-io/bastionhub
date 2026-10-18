package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runPrincipalToACL runs deploy/bastion/principal-to-acl.sh against mapFile
// and returns its stdout and whether it exited 0.
func runPrincipalToACL(t *testing.T, mapFile string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	cmd := exec.Command("bash", "deploy/bastion/principal-to-acl.sh", "gw-passthrough", "test-key-id")
	cmd.Env = append(os.Environ(), "PRINCIPAL_TO_ACL_MAP="+mapFile)
	out, err := cmd.Output()
	if _, isExit := err.(*exec.ExitError); err != nil && !isExit {
		t.Fatalf("running principal-to-acl: %v", err)
	}
	return string(out), err == nil
}

// The shipped mapping file reproduces the principal lines of the fixed list
// the script emitted before it read a mapping file.
func TestPrincipalToACL_ShippedMap(t *testing.T) {
	got, ok := runPrincipalToACL(t, "deploy/bastion/principal-acl")
	want := `permitopen="127.0.0.1:22",no-pty,no-X11-forwarding,no-agent-forwarding gw-edge-ssh-test-loopback
permitopen="127.0.0.1:12001",no-pty,no-X11-forwarding,no-agent-forwarding gw-edge-ssh-test-perso-mbp
`
	if !ok || got != want {
		t.Errorf("ok=%v output:\n%s\nwant:\n%s", ok, got, want)
	}
}

func TestPrincipalToACL_MapLines(t *testing.T) {
	cases := []struct {
		name, mapping, want string
	}{
		{"several destinations", "p 127.0.0.1:1 localhost:1\n",
			`permitopen="127.0.0.1:1",permitopen="localhost:1",no-pty,no-X11-forwarding,no-agent-forwarding p` + "\n"},
		{"comments and blank lines", "# c\n\n   # indented\np 127.0.0.1:1\n",
			`permitopen="127.0.0.1:1",no-pty,no-X11-forwarding,no-agent-forwarding p` + "\n"},
		{"no trailing newline", "p 127.0.0.1:1",
			`permitopen="127.0.0.1:1",no-pty,no-X11-forwarding,no-agent-forwarding p` + "\n"},
		{"no destination", "p\n", ""},
		{"quote in destination", "p 127.0.0.1:1\" x\n", ""},
		{"option in principal", "no-pty,p 127.0.0.1:1\n", ""},
		{"glob destination", "p *\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mapFile := filepath.Join(t.TempDir(), "principal-acl")
			if err := os.WriteFile(mapFile, []byte(c.mapping), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := runPrincipalToACL(t, mapFile)
			if !ok || got != c.want {
				t.Errorf("ok=%v output %q, want %q", ok, got, c.want)
			}
		})
	}
}

func TestPrincipalToACL_MissingMapFails(t *testing.T) {
	got, ok := runPrincipalToACL(t, filepath.Join(t.TempDir(), "absent"))
	if ok || got != "" {
		t.Errorf("ok=%v output %q, want a non-zero exit and no output", ok, got)
	}
}
