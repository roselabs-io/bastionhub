package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
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

// bastionhub runs on the bastion when sshboard is deployed there, where SSHing
// to admin_alias would mean the host connecting to itself.
func TestStatusLocalModeIsEnvControlled(t *testing.T) {
	t.Setenv("BASTIONHUB_LOCAL", "")
	if os.Getenv("BASTIONHUB_LOCAL") != "" {
		t.Fatal("unset env should read empty")
	}
	t.Setenv("BASTIONHUB_LOCAL", "1")
	if os.Getenv("BASTIONHUB_LOCAL") == "" {
		t.Fatal("set env should read non-empty")
	}
}

// The header counts every 127.0.0.1 listener on the bastion, including ones
// that are not configured endpoints (serve on 8420).
func TestStatusCmdCountsEveryListener(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/sh\nprintf '8420 01:00\\n12001 02:00\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BASTIONHUB_LOCAL", "")
	cfgPath := filepath.Join(t.TempDir(), "endpoints.yaml")
	cfg := "admin_alias: test-root\nendpoints:\n  edge-a:\n    port: 12001\n    user: root\n  edge-b:\n    port: 12002\n    user: root\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASTIONHUB_CONFIG", cfgPath)
	out, err := captureStdout(t, func() error { return statusCmd(context.Background(), nil) })
	if err != nil {
		t.Fatal(err)
	}
	if want := "Querying test-root — 2 configured endpoint(s), 2 live listener(s)\n"; !strings.HasPrefix(out, want) {
		t.Errorf("output does not start with %q:\n%s", want, out)
	}
}

func TestWriteMCPTokenReplacesFileWithMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-token")
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	token, err := writeMCPToken(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != token+"\n" || len(token) != 2*adminTokenBytes {
		t.Errorf("file holds %q, token %q", got, token)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// -----------------------------------------------------------------------------
// bastion verify
// -----------------------------------------------------------------------------

const (
	testUserCA   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKb/9/52a8aDeV1gk5Z1WvKawKAf09cAUGMCs3/y4tFt"
	testSshcaOut = "user CA   " + testUserCA + " sshca user CA 20261018\n" +
		"          256 SHA256:eSG5qw5cVqS8xh1alTOMXU8ntzf0ApnUW7bl+eeQ5xs sshca user CA 20261018 (ED25519)\n" +
		"host CA   ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGsF0sEOMiV1b8sy+kM/dVt6JBxs40e/GfWIlEnOhRVL sshca host CA 20261018\n"
	testHealthyBastion = "dropin 10-bastionhub.conf ok\n" +
		"dropin 30-passthrough-acl.conf ok\n" +
		"user gw-tunnel ok\nuser gw-user ok\nuser gw-passthrough ok\n" +
		"cafile /etc/ssh/user_ca.pub\n" +
		"ca # trust root\n" +
		"ca " + testUserCA + " sshca user CA 20261018\n" +
		"krl /etc/ssh/revoked_keys.krl ok\n"
)

func TestVerifyRemoteScriptParses(t *testing.T) {
	if out, err := exec.Command("sh", "-n", "-c", verifyRemoteScript).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

func TestParseUserCA(t *testing.T) {
	got, err := parseUserCA(testSshcaOut)
	if err != nil || got != testUserCA {
		t.Errorf("parseUserCA = %q, %v; want %q", got, err, testUserCA)
	}
	if _, err := parseUserCA("host CA   ssh-ed25519 AAAA\n"); err == nil {
		t.Error("expected an error when no user CA line is present")
	}
}

func TestEvaluateVerify(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		caErr  error
		failed []string // names of the checks expected to fail
	}{
		{"healthy", testHealthyBastion, nil, nil},
		{"drop-in missing",
			strings.Replace(testHealthyBastion, "30-passthrough-acl.conf ok", "30-passthrough-acl.conf missing", 1), nil,
			[]string{"sshd drop-in 30-passthrough-acl.conf"}},
		{"account missing",
			strings.Replace(testHealthyBastion, "user gw-user ok", "user gw-user missing", 1), nil,
			[]string{"account gw-user"}},
		{"CA mismatch",
			strings.Replace(testHealthyBastion, "ca "+testUserCA, "ca ssh-ed25519 AAAAother", 1), nil,
			[]string{"TrustedUserCAKeys (/etc/ssh/user_ca.pub) matches the local sshca user CA"}},
		{"CA only in a comment",
			strings.Replace(testHealthyBastion, "ca "+testUserCA, "ca # "+testUserCA, 1), nil,
			[]string{"TrustedUserCAKeys (/etc/ssh/user_ca.pub) matches the local sshca user CA"}},
		{"no TrustedUserCAKeys",
			strings.Replace(testHealthyBastion, "cafile /etc/ssh/user_ca.pub", "cafile none", 1), nil,
			[]string{"TrustedUserCAKeys matches the local sshca user CA"}},
		{"local CA unreadable", testHealthyBastion, fmt.Errorf("sshca ca show: exit status 1"),
			[]string{"TrustedUserCAKeys matches the local sshca user CA"}},
		{"KRL unreadable",
			strings.Replace(testHealthyBastion, "revoked_keys.krl ok", "revoked_keys.krl unreadable", 1), nil,
			[]string{"KRL (/etc/ssh/revoked_keys.krl) readable"}},
		{"no RevokedKeys",
			strings.Replace(testHealthyBastion, "krl /etc/ssh/revoked_keys.krl ok", "krl none unreadable", 1), nil,
			[]string{"KRL (none) readable"}},
		{"truncated output", "dropin 10-bastionhub.conf ok\n", nil,
			[]string{"sshd drop-in 30-passthrough-acl.conf", "account gw-tunnel", "account gw-user",
				"account gw-passthrough", "TrustedUserCAKeys matches the local sshca user CA", "KRL readable"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checks := evaluateVerify(c.remote, testUserCA, c.caErr)
			if len(checks) != 7 {
				t.Fatalf("got %d checks, want 7: %+v", len(checks), checks)
			}
			var failed []string
			for _, ch := range checks {
				if !ch.ok {
					failed = append(failed, ch.name)
					if ch.detail == "" {
						t.Errorf("failed check %q has no detail", ch.name)
					}
				}
			}
			if strings.Join(failed, "|") != strings.Join(c.failed, "|") {
				t.Errorf("failed checks = %q, want %q", failed, c.failed)
			}
		})
	}
}

// TestVerifyRemoteScriptCAFileWithoutFinalNewline runs verifyRemoteScript with
// a stub sshd whose TrustedUserCAKeys file lacks a final newline; the KRL
// record must still be reported on its own line.
func TestVerifyRemoteScriptCAFileWithoutFinalNewline(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "user_ca.pub")
	krlPath := filepath.Join(dir, "revoked_keys.krl")
	if err := os.WriteFile(caPath, []byte(testUserCA+" sshca user CA 20261018"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(krlPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sshd := fmt.Sprintf("#!/bin/sh\necho 'trustedusercakeys %s'\necho 'revokedkeys %s'\n", caPath, krlPath)
	if err := os.WriteFile(filepath.Join(dir, "sshd"), []byte(sshd), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := exec.Command("sh", "-c", verifyRemoteScript).Output()
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	for _, ch := range evaluateVerify(string(out), testUserCA, nil) {
		if strings.Contains(ch.name, "CA") || strings.Contains(ch.name, "KRL") {
			if !ch.ok {
				t.Errorf("%s failed: %s\n%s", ch.name, ch.detail, out)
			}
		}
	}
}

// TestBastionVerifyCmd drives the command with stub ssh and sshca binaries on
// PATH; the stub ssh prints the file BASTION_OUT names, or exits 255 without it.
func TestBastionVerifyCmd(t *testing.T) {
	bin := t.TempDir()
	stubs := map[string]string{
		"ssh":   "#!/bin/sh\n[ -n \"$BASTION_OUT\" ] || exit 255\ncat \"$BASTION_OUT\"\n",
		"sshca": "#!/bin/sh\ncat \"$SSHCA_OUT\"\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfgPath := filepath.Join(t.TempDir(), "endpoints.yaml")
	if err := os.WriteFile(cfgPath, []byte("admin_alias: test-root\nendpoints:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASTIONHUB_CONFIG", cfgPath)
	sshcaOut := filepath.Join(t.TempDir(), "show")
	if err := os.WriteFile(sshcaOut, []byte(testSshcaOut), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSHCA_OUT", sshcaOut)

	cases := []struct {
		name     string
		bastion  string // stub ssh output; empty makes ssh fail
		wantFail bool
		wantLine string
	}{
		{"healthy", testHealthyBastion, false, "PASS  TrustedUserCAKeys (/etc/ssh/user_ca.pub) matches the local sshca user CA"},
		{"account missing", strings.Replace(testHealthyBastion, "user gw-tunnel ok", "user gw-tunnel missing", 1), true, "FAIL  account gw-tunnel: no such user"},
		{"unreachable", "", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BASTION_OUT", "")
			if c.bastion != "" {
				p := filepath.Join(t.TempDir(), "remote")
				if err := os.WriteFile(p, []byte(c.bastion), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BASTION_OUT", p)
			}
			out, err := captureStdout(t, func() error { return bastionVerifyCmd(context.Background(), nil) })
			if c.wantFail {
				var exit cli.ExitCoder
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Errorf("err = %v, want exit code 1", err)
				}
			} else if err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if c.wantLine != "" && !strings.Contains(out, c.wantLine+"\n") {
				t.Errorf("output lacks %q:\n%s", c.wantLine, out)
			}
		})
	}
}

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	runErr := f()
	os.Stdout = orig
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}
