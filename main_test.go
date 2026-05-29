package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// -----------------------------------------------------------------------------
// sanitizeForComment — keeps [a-zA-Z0-9-] only
// -----------------------------------------------------------------------------

func TestSanitizeForComment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"perso-mbp", "perso-mbp"},
		{"work-laptop", "work-laptop"},
		{"MacBook Pro 16", "MacBookPro16"},
		{"some_underscore", "someunderscore"},
		{"with.dots", "withdots"},
		{"colons:and slashes/", "colonsandslashes"},
		{"", ""},
		{"---", "---"},
		{"héllo-wörld", "hllo-wrld"}, // accented chars dropped; ASCII letters kept
	}
	for _, c := range cases {
		if got := sanitizeForComment(c.in); got != c.want {
			t.Errorf("sanitizeForComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// -----------------------------------------------------------------------------
// loadConfig — regression test for the nil-map bug
//
// yaml.Unmarshal overrides the pre-initialized empty Endpoints map with nil
// when the YAML has `endpoints:` with no children (or only commented children).
// loadConfig must re-init the map post-Unmarshal so register/enroll don't
// panic on the first endpoint. Real bug, hit during 2026-05-29 cutover.
// -----------------------------------------------------------------------------

func TestLoadConfig_NilMapRegression(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.yaml")
	t.Setenv("BASTIONHUB_CONFIG", path)

	if err := os.WriteFile(path, []byte(`bastion_alias: bastion
admin_alias: bastion-root
endpoints:
  # only commented examples — this triggered the panic before the fix
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Endpoints == nil {
		t.Errorf("cfg.Endpoints is nil — should be initialized empty map")
	}
	// Assignment must not panic.
	cfg.Endpoints["test"] = Endpoint{Port: 12001, User: "root"}
	if len(cfg.Endpoints) != 1 {
		t.Errorf("post-init assignment failed: %d entries", len(cfg.Endpoints))
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoints.yaml")
	t.Setenv("BASTIONHUB_CONFIG", path)

	// Minimal config — no bastion_alias / admin_alias keys; defaults expected.
	if err := os.WriteFile(path, []byte(`endpoints:
  perso-mbp:
    port: 12001
    user: psd
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.BastionAlias != "bastion" {
		t.Errorf("BastionAlias default = %q, want %q", cfg.BastionAlias, "bastion")
	}
	if cfg.AdminAlias != "bastion-root" {
		t.Errorf("AdminAlias default = %q, want %q", cfg.AdminAlias, "bastion-root")
	}
	if ep, ok := cfg.Endpoints["perso-mbp"]; !ok {
		t.Errorf("perso-mbp endpoint missing")
	} else if ep.Port != 12001 || ep.User != "psd" {
		t.Errorf("perso-mbp = %+v, want Port=12001 User=psd", ep)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.yaml")
	t.Setenv("BASTIONHUB_CONFIG", path)

	_, err := loadConfig()
	if err == nil {
		t.Errorf("expected error when config file missing")
	}
}

// -----------------------------------------------------------------------------
// allocatePort — picks the next free port in 12001-12099
// -----------------------------------------------------------------------------

func TestAllocatePort_Empty(t *testing.T) {
	cfg := &Config{Endpoints: map[string]Endpoint{}}
	p, err := allocatePort(cfg)
	if err != nil || p != 12001 {
		t.Errorf("first port = %d, %v; want 12001, nil", p, err)
	}
}

func TestAllocatePort_SkipsTaken(t *testing.T) {
	cfg := &Config{Endpoints: map[string]Endpoint{
		"a": {Port: 12001},
		"b": {Port: 12002},
	}}
	p, err := allocatePort(cfg)
	if err != nil || p != 12003 {
		t.Errorf("got %d, %v; want 12003, nil", p, err)
	}
}

func TestAllocatePort_FindsHole(t *testing.T) {
	cfg := &Config{Endpoints: map[string]Endpoint{
		"a": {Port: 12001},
		// 12002 is the hole
		"c": {Port: 12003},
	}}
	p, err := allocatePort(cfg)
	if err != nil || p != 12002 {
		t.Errorf("got %d, %v; want 12002 (hole), nil", p, err)
	}
}

func TestAllocatePort_Exhausted(t *testing.T) {
	cfg := &Config{Endpoints: map[string]Endpoint{}}
	for i := 12001; i <= 12099; i++ {
		cfg.Endpoints[fmt.Sprintf("ep-%d", i)] = Endpoint{Port: i}
	}
	_, err := allocatePort(cfg)
	if err == nil {
		t.Errorf("expected error when port range fully allocated")
	}
}
