// bastionhub — self-hosted SSH bastion + reverse-tunnel substrate.
//
// Single Go binary. Manages a small fleet of "endpoints" that hold persistent
// reverse SSH tunnels to a central bastion VPS. Pairs with sshca for cert auth.
//
// See docs/decisions/inherited.md for the upstream ADRs that motivated this tool.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"
)

// version is the bastionhub tool version. Defaults to "0.1.0-dev"
// during local development; release builds override via:
//   go build -ldflags "-X main.version=<tag>"
// See .github/workflows/release.yml.
var version = "0.1.0-dev"

// -----------------------------------------------------------------------------
// Endpoint-side paths (where bastionhub installs autossh on a Linux endpoint)
// -----------------------------------------------------------------------------

const (
	endpointKeyDir      = "/etc/bastionhub-tunnel"
	endpointKeyPath     = "/etc/bastionhub-tunnel/id_ed25519"
	endpointKnownHosts  = "/etc/bastionhub-tunnel/known_hosts"
	endpointServiceUnit = "/etc/systemd/system/bastionhub-tunnel.service"
)

// -----------------------------------------------------------------------------
// Config (endpoints.yaml)
// -----------------------------------------------------------------------------

// Endpoint is one entry in endpoints.yaml — a host that holds a reverse SSH
// tunnel to the bastion. From bastionhub's POV, endpoints are
// network-topology nodes; customer/role/principal metadata belongs upstream
// (e.g. in the gateway product's registry).
type Endpoint struct {
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	// Identity (optional): path to SSH private key / cert to use when
	// authenticating to THIS endpoint's sshd (after ProxyJump). When set,
	// `bastionhub ssh` passes -i and IdentitiesOnly=yes so only this key is
	// offered (avoids MaxAuthTries blow-out from offering every key in ~/.ssh/).
	Identity    string `yaml:"identity,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// Config is the whole endpoints.yaml.
type Config struct {
	BastionAlias string              `yaml:"bastion_alias"` // ProxyJump target; default "bastion"
	AdminAlias   string              `yaml:"admin_alias"`   // for status/admin queries; default "bastion-root"
	Endpoints    map[string]Endpoint `yaml:"endpoints"`
}

func configPath() string {
	if p := os.Getenv("BASTIONHUB_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "bastionhub", "endpoints.yaml")
}

func loadConfig() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no config at %s — run `bastionhub init` to create one", path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg := &Config{Endpoints: map[string]Endpoint{}}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.Endpoints == nil {
		// yaml.Unmarshal of `endpoints:` with no children (or only commented
		// children) overwrites the pre-initialized empty map with nil. Re-init.
		cfg.Endpoints = map[string]Endpoint{}
	}
	if cfg.BastionAlias == "" {
		cfg.BastionAlias = "bastion"
	}
	if cfg.AdminAlias == "" {
		cfg.AdminAlias = "bastion-root"
	}
	return cfg, nil
}

func saveConfig(cfg *Config) error {
	path := configPath()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	header := "# bastionhub endpoints registry. Managed by `bastionhub endpoint register/unregister` — hand-edits are fine but format matters.\n\n"
	return os.WriteFile(path, append([]byte(header), data...), 0o600)
}

func allocatePort(cfg *Config) (int, error) {
	used := map[int]bool{}
	for _, e := range cfg.Endpoints {
		used[e.Port] = true
	}
	for p := 12001; p <= 12099; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port in 12001-12099")
}

// -----------------------------------------------------------------------------
// Daily-use top-level commands
// -----------------------------------------------------------------------------

// sshCmd: bastionhub ssh <endpoint> [extra ssh args...]
//
// Uses exec.Command + Run() with inherited stdio rather than syscall.Exec
// so it works on Windows. On Unix this means Go's runtime stays in the
// middle of the process tree (vs. syscall.Exec which would replace it),
// but signal forwarding via inherited stdio is correct: Ctrl+C reaches
// ssh as expected.
func sshCmd(_ context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: bastionhub ssh <endpoint-name> [extra ssh args]", 1)
	}
	cfg, err := loadConfig()
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	ep, ok := cfg.Endpoints[name]
	if !ok {
		return cli.Exit(fmt.Sprintf("unknown endpoint %q (run `bastionhub list` to see available)", name), 1)
	}

	args := []string{
		"-J", cfg.BastionAlias,
		fmt.Sprintf("%s@127.0.0.1", ep.User),
		"-p", fmt.Sprintf("%d", ep.Port),
	}
	if ep.Identity != "" {
		args = append(args, "-i", ep.Identity, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, cmd.Args().Tail()...)

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return cli.Exit("ssh binary not found in PATH (install OpenSSH client: brew/apt install openssh-client, or Windows Optional Feature 'OpenSSH Client')", 1)
	}
	sshExec := exec.Command(sshPath, args...)
	sshExec.Stdin = os.Stdin
	sshExec.Stdout = os.Stdout
	sshExec.Stderr = os.Stderr
	if err := sshExec.Run(); err != nil {
		// Propagate ssh's exit code (e.g. 255 for connection failure)
		// so callers can react. Other Go-side errors fall through to cli.Exit.
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return cli.Exit(err.Error(), 1)
	}
	return nil
}

func listCmd(_ context.Context, _ *cli.Command) error {
	cfg, err := loadConfig()
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if len(cfg.Endpoints) == 0 {
		fmt.Printf("(no endpoints configured in %s)\n", configPath())
		return nil
	}
	names := make([]string, 0, len(cfg.Endpoints))
	for n := range cfg.Endpoints {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("Bastion alias: %s   (config: %s)\n\n", cfg.BastionAlias, configPath())
	fmt.Printf("%-20s %-7s %-25s %s\n", "NAME", "PORT", "USER", "DESCRIPTION")
	for _, n := range names {
		ep := cfg.Endpoints[n]
		fmt.Printf("%-20s %-7d %-25s %s\n", n, ep.Port, ep.User, ep.Description)
	}
	return nil
}

func statusCmd(_ context.Context, _ *cli.Command) error {
	cfg, err := loadConfig()
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if len(cfg.Endpoints) == 0 {
		fmt.Printf("(no endpoints configured in %s)\n", configPath())
		return nil
	}
	remoteCmd := `ss -Hltnp 2>/dev/null | awk '/127\.0\.0\.1:/ {
        n = split($4, parts, ":"); port = parts[n]
        match($6, /pid=[0-9]+/); pid = substr($6, RSTART+4, RLENGTH-4)
        cmd = "ps -o etime= -p " pid " 2>/dev/null"
        cmd | getline etime; close(cmd)
        gsub(/ /, "", etime)
        print port " " etime
    }'`
	out, err := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		cfg.AdminAlias,
		remoteCmd,
	).Output()
	if err != nil {
		return cli.Exit(fmt.Sprintf("status query to %s failed: %v", cfg.AdminAlias, err), 1)
	}
	live := map[int]string{}
	lineRE := regexp.MustCompile(`^(\d+)\s+(\S*)$`)
	for _, line := range strings.Split(string(out), "\n") {
		m := lineRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		port, _ := strconv.Atoi(m[1])
		live[port] = m[2]
	}
	names := make([]string, 0, len(cfg.Endpoints))
	for n := range cfg.Endpoints {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("Querying %s — %d configured endpoint(s), %d live listener(s)\n\n",
		cfg.AdminAlias, len(cfg.Endpoints), len(live))
	fmt.Printf("%-20s %-7s %-6s %-12s %s\n", "NAME", "PORT", "STATUS", "UPTIME", "DESCRIPTION")
	for _, n := range names {
		ep := cfg.Endpoints[n]
		status, uptime := "DOWN", "—"
		if u, ok := live[ep.Port]; ok {
			status = "UP"
			uptime = u
			if uptime == "" {
				uptime = "?"
			}
		}
		fmt.Printf("%-20s %-7d %-6s %-12s %s\n", n, ep.Port, status, uptime, ep.Description)
	}
	return nil
}

func initCmd(_ context.Context, _ *cli.Command) error {
	path := configPath()
	if _, err := os.Stat(path); err == nil {
		return cli.Exit(fmt.Sprintf("config already exists at %s (refusing to overwrite)", path), 1)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	starter := `# bastionhub endpoints registry — see ../docs/

bastion_alias: bastion         # ProxyJump target for daily use
admin_alias: bastion-root      # for status/admin queries

endpoints:
  # Example — uncomment and adapt:
  # customer-001:
  #   port: 12001
  #   user: root
  #   description: "Example endpoint at customer site"
`
	if err := os.WriteFile(path, []byte(starter), 0o600); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	fmt.Printf("Created %s — edit to add your endpoints, or use `bastionhub endpoint register`.\n", path)
	return nil
}

// -----------------------------------------------------------------------------
// Endpoint management — engineer-side
// -----------------------------------------------------------------------------

func endpointRegisterCmd(_ context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: bastionhub endpoint register <name> [--user root] [--port N] [--description '...']", 1)
	}
	cfg, err := loadConfig()
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if _, exists := cfg.Endpoints[name]; exists {
		return cli.Exit(fmt.Sprintf("endpoint %q already exists (use `bastionhub endpoint unregister %s` first)", name, name), 1)
	}
	port := int(cmd.Int("port"))
	if port == 0 {
		port, err = allocatePort(cfg)
		if err != nil {
			return cli.Exit(err.Error(), 1)
		}
	} else {
		for _, e := range cfg.Endpoints {
			if e.Port == port {
				return cli.Exit(fmt.Sprintf("port %d already used by another endpoint", port), 1)
			}
		}
	}
	user := cmd.String("user")
	if user == "" {
		user = "root"
	}

	cfg.Endpoints[name] = Endpoint{
		Port:        port,
		User:        user,
		Identity:    cmd.String("identity"),
		Description: cmd.String("description"),
	}
	if err := saveConfig(cfg); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	fmt.Printf("✓ endpoint %q registered\n  port: %d\n  user: %s\n", name, port, user)
	if id := cmd.String("identity"); id != "" {
		fmt.Printf("  identity: %s\n", id)
	}
	fmt.Println()
	fmt.Printf("Next: issue a tunnel cert for this endpoint, then run setup on the endpoint device.\n")
	fmt.Printf("  sshca cert sign --ca user --principal gw-tunnel --valid +52w \\\n")
	fmt.Printf("      --key-id %q <path-to-endpoint-pubkey>\n", name+"-tunnel-"+time.Now().UTC().Format("20060102"))
	fmt.Printf("  sudo bastionhub endpoint setup --port %d --bastion <bastion-host>   # on the endpoint device\n", port)
	return nil
}

func endpointUnregisterCmd(_ context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: bastionhub endpoint unregister <name>", 1)
	}
	cfg, err := loadConfig()
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if _, ok := cfg.Endpoints[name]; !ok {
		return cli.Exit(fmt.Sprintf("unknown endpoint %q", name), 1)
	}
	delete(cfg.Endpoints, name)
	if err := saveConfig(cfg); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	fmt.Printf("✓ endpoint %q unregistered\n", name)
	fmt.Printf("Note: any active cert for this endpoint remains valid until expiry. To revoke immediately:\n")
	fmt.Printf("  sshca cert revoke --ca user --key-id <key-id> --ship <bastion>:/etc/ssh/revoked_keys.krl\n")
	return nil
}

// endpointEnrollCmd: one-shot — sign tunnel cert (via sshca) + register endpoint.
func endpointEnrollCmd(_ context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: bastionhub endpoint enroll <name> --pubkey-file <path> [--port N] [--user root] [--description '...']", 1)
	}
	pubkeyFile := cmd.String("pubkey-file")
	if pubkeyFile == "" {
		return cli.Exit("--pubkey-file is required (the endpoint's outbound SSH pubkey, e.g. from `bastionhub endpoint install`)", 1)
	}
	if _, err := os.Stat(pubkeyFile); err != nil {
		return cli.Exit(fmt.Sprintf("pubkey file: %v", err), 1)
	}
	if _, err := exec.LookPath("sshca"); err != nil {
		return cli.Exit("sshca not found in PATH — install from https://github.com/roselabs-io/sshca first", 1)
	}
	cfg, err := loadConfig()
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if _, exists := cfg.Endpoints[name]; exists {
		return cli.Exit(fmt.Sprintf("endpoint %q already registered. To re-issue its cert, run `sshca cert sign ...`. To start over: `bastionhub endpoint unregister %s` first.", name, name), 1)
	}
	port := int(cmd.Int("port"))
	if port == 0 {
		port, err = allocatePort(cfg)
		if err != nil {
			return cli.Exit(err.Error(), 1)
		}
	} else {
		for _, e := range cfg.Endpoints {
			if e.Port == port {
				return cli.Exit(fmt.Sprintf("port %d already used by another endpoint", port), 1)
			}
		}
	}
	user := cmd.String("user")
	if user == "" {
		user = "root"
	}
	valid := cmd.String("valid")
	if valid == "" {
		valid = "+52w"
	}
	principal := cmd.String("principal")
	if principal == "" {
		principal = "gw-tunnel"
	}
	keyID := fmt.Sprintf("%s-tunnel-%s", name, time.Now().UTC().Format("20060102T1504Z"))

	sshcaArgs := []string{
		"cert", "sign",
		"--ca", "user",
		"--principal", principal,
		"--valid", valid,
		"--key-id", keyID,
	}
	if caDir := cmd.String("ca-dir"); caDir != "" {
		sshcaArgs = append(sshcaArgs, "--dir", caDir)
	}
	sshcaArgs = append(sshcaArgs, pubkeyFile)

	out, err := exec.Command("sshca", sshcaArgs...).CombinedOutput()
	if err != nil {
		return cli.Exit(fmt.Sprintf("sshca cert sign failed: %v\n%s", err, out), 1)
	}
	fmt.Print(string(out))

	certFile := strings.TrimSuffix(pubkeyFile, ".pub") + "-cert.pub"
	if _, err := os.Stat(certFile); err != nil {
		return cli.Exit(fmt.Sprintf("sshca succeeded but expected cert at %s not found", certFile), 1)
	}

	cfg.Endpoints[name] = Endpoint{
		Port:        port,
		User:        user,
		Identity:    cmd.String("identity"),
		Description: cmd.String("description"),
	}
	if err := saveConfig(cfg); err != nil {
		return cli.Exit(fmt.Sprintf("cert signed but registry save failed: %v", err), 1)
	}
	fmt.Printf("\n✓ endpoint %q enrolled\n  port:   %d\n  user:   %s\n  cert:   %s\n  valid:  %s\n  key-id: %s\n\n",
		name, port, user, certFile, valid, keyID)
	fmt.Println("Next:")
	fmt.Printf("  1. Ship the cert back to the endpoint (path depends on its OS):\n")
	fmt.Printf("       Linux:  scp %s endpoint:/etc/bastionhub-tunnel/id_ed25519-cert.pub\n", certFile)
	fmt.Printf("       macOS:  scp %s endpoint:~/.bastionhub-tunnel/id_ed25519-cert.pub\n", certFile)
	fmt.Printf("  2. On the endpoint device, run setup (sudo on Linux; no sudo on macOS):\n")
	fmt.Printf("       bastionhub endpoint setup --port %d --bastion <bastion-host>\n", port)
	fmt.Printf("  3. From this laptop, verify: `bastionhub status`\n")
	return nil
}

// -----------------------------------------------------------------------------
// Endpoint install / setup — runs ON the endpoint device (Linux or macOS)
// -----------------------------------------------------------------------------

func sanitizeForComment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// endpointInstallCmd dispatches on GOOS.
func endpointInstallCmd(ctx context.Context, cmd *cli.Command) error {
	switch runtime.GOOS {
	case "linux":
		return endpointInstallLinux(ctx, cmd)
	case "darwin":
		return endpointInstallDarwin(ctx, cmd)
	default:
		return cli.Exit("`bastionhub endpoint install` only supports Linux and macOS (current OS: "+runtime.GOOS+")", 1)
	}
}

// endpointSetupCmd dispatches on GOOS.
func endpointSetupCmd(ctx context.Context, cmd *cli.Command) error {
	switch runtime.GOOS {
	case "linux":
		return endpointSetupLinux(ctx, cmd)
	case "darwin":
		return endpointSetupDarwin(ctx, cmd)
	default:
		return cli.Exit("`bastionhub endpoint setup` only supports Linux and macOS (current OS: "+runtime.GOOS+")", 1)
	}
}

// -----------------------------------------------------------------------------
// Linux — systemd + apt/dnf/yum/apk + /etc/bastionhub-tunnel/
// -----------------------------------------------------------------------------

func installAutosshLinux() error {
	for _, mgr := range []struct {
		bin     string
		preArgs [][]string
		args    []string
	}{
		{"apt-get", [][]string{{"apt-get", "update", "-qq"}}, []string{"install", "-y", "autossh"}},
		{"dnf", nil, []string{"install", "-y", "autossh"}},
		{"yum", nil, []string{"install", "-y", "autossh"}},
		{"apk", nil, []string{"add", "--no-cache", "autossh"}},
	} {
		if _, err := exec.LookPath(mgr.bin); err != nil {
			continue
		}
		for _, pre := range mgr.preArgs {
			if out, err := exec.Command(pre[0], pre[1:]...).CombinedOutput(); err != nil {
				return fmt.Errorf("%v: %w\n%s", pre, err, out)
			}
		}
		out, err := exec.Command(mgr.bin, mgr.args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %v: %w\n%s", mgr.bin, mgr.args, err, out)
		}
		return nil
	}
	return fmt.Errorf("no supported package manager (need apt-get / dnf / yum / apk)")
}

func endpointInstallLinux(_ context.Context, _ *cli.Command) error {
	if os.Geteuid() != 0 {
		return cli.Exit("on Linux, must run as root: `sudo bastionhub endpoint install`", 1)
	}

	if _, err := exec.LookPath("autossh"); err != nil {
		fmt.Println("Installing autossh…")
		if err := installAutosshLinux(); err != nil {
			return cli.Exit(fmt.Sprintf("autossh install failed: %v", err), 1)
		}
	} else {
		fmt.Println("autossh already installed")
	}

	if err := os.MkdirAll(endpointKeyDir, 0o700); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if _, err := os.Stat(endpointKeyPath); os.IsNotExist(err) {
		host, _ := os.Hostname()
		comment := fmt.Sprintf("bastionhub-%s-%s", sanitizeForComment(host), time.Now().Format("20060102"))
		out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", endpointKeyPath, "-N", "", "-C", comment, "-q").CombinedOutput()
		if err != nil {
			return cli.Exit(fmt.Sprintf("ssh-keygen failed: %v\n%s", err, out), 1)
		}
		fmt.Println("Generated", endpointKeyPath)
	} else {
		fmt.Println("Key exists at", endpointKeyPath, "(reusing)")
	}

	pubkey, err := os.ReadFile(endpointKeyPath + ".pub")
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	pubkeyStr := strings.TrimSpace(string(pubkey))

	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  Public key (ship this to your engineer):")
	fmt.Println("============================================================")
	fmt.Println(pubkeyStr)
	fmt.Println("============================================================")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Ship the pubkey to the engineer's laptop, then run there:")
	fmt.Printf("       bastionhub endpoint enroll <name> --pubkey-file <path>\n")
	fmt.Println("  2. Note the assigned port (from the enroll output) and the bastion hostname.")
	fmt.Printf("  3. Ship the resulting -cert.pub back here next to %s, then:\n", endpointKeyPath)
	fmt.Println("       sudo bastionhub endpoint setup --port <N> --bastion <host>")
	return nil
}

func endpointSetupLinux(_ context.Context, cmd *cli.Command) error {
	if os.Geteuid() != 0 {
		return cli.Exit("on Linux, must run as root: `sudo bastionhub endpoint setup ...`", 1)
	}
	port := int(cmd.Int("port"))
	bastion := cmd.String("bastion")
	bastionUser := cmd.String("bastion-user")
	if bastionUser == "" {
		bastionUser = "gw-tunnel"
	}
	if port == 0 || bastion == "" {
		return cli.Exit("--port and --bastion are required", 1)
	}
	if _, err := os.Stat(endpointKeyPath); os.IsNotExist(err) {
		return cli.Exit(fmt.Sprintf("no key at %s — run `sudo bastionhub endpoint install` first", endpointKeyPath), 1)
	}

	unit := fmt.Sprintf(`[Unit]
Description=Reverse SSH tunnel to bastion (port %d) — bastionhub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Environment=AUTOSSH_GATETIME=30
Environment=AUTOSSH_LOGFILE=/var/log/bastionhub-tunnel-autossh.log
ExecStart=/usr/bin/autossh -M 0 -N \
    -i %s \
    -o IdentitiesOnly=yes \
    -o StrictHostKeyChecking=accept-new \
    -o UserKnownHostsFile=%s \
    -o ServerAliveInterval=30 \
    -o ServerAliveCountMax=3 \
    -o ExitOnForwardFailure=yes \
    -R %d:localhost:22 \
    %s@%s
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, port, endpointKeyPath, endpointKnownHosts, port, bastionUser, bastion)

	if cmd.Bool("dry-run") {
		fmt.Printf("--dry-run: would write %s:\n\n%s\n", endpointServiceUnit, unit)
		fmt.Println("--dry-run: would run: systemctl daemon-reload && systemctl enable --now bastionhub-tunnel.service")
		return nil
	}

	if err := os.WriteFile(endpointServiceUnit, []byte(unit), 0o644); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	fmt.Println("Wrote", endpointServiceUnit)

	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return cli.Exit(fmt.Sprintf("daemon-reload failed: %v\n%s", err, out), 1)
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "bastionhub-tunnel.service").CombinedOutput(); err != nil {
		return cli.Exit(fmt.Sprintf("enable+start failed: %v\n%s", err, out), 1)
	}
	time.Sleep(3 * time.Second)
	out, _ := exec.Command("systemctl", "status", "bastionhub-tunnel.service", "--no-pager", "-l").CombinedOutput()
	fmt.Println(string(out))
	fmt.Println("Done. From the engineer laptop: `bastionhub status` to verify UP.")
	return nil
}

// -----------------------------------------------------------------------------
// macOS — launchd + brew + per-user ~/.bastionhub-tunnel + ~/Library/LaunchAgents
// -----------------------------------------------------------------------------

const darwinLaunchLabel = "com.roselabs.bastionhub-tunnel"

func darwinKeyDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".bastionhub-tunnel")
}

func darwinKeyPath() string {
	return filepath.Join(darwinKeyDir(), "id_ed25519")
}

func darwinKnownHostsPath() string {
	return filepath.Join(darwinKeyDir(), "known_hosts")
}

func darwinLaunchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", darwinLaunchLabel+".plist")
}

func darwinLogDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "bastionhub")
}

func endpointInstallDarwin(_ context.Context, _ *cli.Command) error {
	if os.Geteuid() == 0 {
		return cli.Exit("on macOS, run `bastionhub endpoint install` as your user (not sudo) — launchd agents live in ~/Library/LaunchAgents/", 1)
	}

	if _, err := exec.LookPath("brew"); err != nil {
		return cli.Exit("Homebrew not found. Install from https://brew.sh, then re-run.", 1)
	}

	if _, err := exec.LookPath("autossh"); err != nil {
		fmt.Println("Installing autossh via Homebrew…")
		out, err := exec.Command("brew", "install", "autossh").CombinedOutput()
		if err != nil {
			return cli.Exit(fmt.Sprintf("brew install autossh failed: %v\n%s", err, out), 1)
		}
	} else {
		fmt.Println("autossh already installed")
	}

	keyDir := darwinKeyDir()
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	keyPath := darwinKeyPath()
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		host, _ := os.Hostname()
		comment := fmt.Sprintf("bastionhub-%s-%s", sanitizeForComment(host), time.Now().Format("20060102"))
		out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-C", comment, "-q").CombinedOutput()
		if err != nil {
			return cli.Exit(fmt.Sprintf("ssh-keygen failed: %v\n%s", err, out), 1)
		}
		fmt.Println("Generated", keyPath)
	} else {
		fmt.Println("Key exists at", keyPath, "(reusing)")
	}

	pubkey, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	pubkeyStr := strings.TrimSpace(string(pubkey))

	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  Public key (ship this to your engineer):")
	fmt.Println("============================================================")
	fmt.Println(pubkeyStr)
	fmt.Println("============================================================")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Ship the pubkey to the engineer's laptop, then run there:")
	fmt.Printf("       bastionhub endpoint enroll <name> --pubkey-file <path>\n")
	fmt.Println("  2. Note the assigned port (from the enroll output) and the bastion hostname.")
	fmt.Printf("  3. Ship the resulting -cert.pub back here next to %s, then:\n", keyPath)
	fmt.Println("       bastionhub endpoint setup --port <N> --bastion <host>")
	return nil
}

func endpointSetupDarwin(_ context.Context, cmd *cli.Command) error {
	if os.Geteuid() == 0 {
		return cli.Exit("on macOS, run `bastionhub endpoint setup` as your user (not sudo)", 1)
	}
	port := int(cmd.Int("port"))
	bastion := cmd.String("bastion")
	bastionUser := cmd.String("bastion-user")
	if bastionUser == "" {
		bastionUser = "gw-tunnel"
	}
	if port == 0 || bastion == "" {
		return cli.Exit("--port and --bastion are required", 1)
	}

	keyPath := darwinKeyPath()
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return cli.Exit(fmt.Sprintf("no key at %s — run `bastionhub endpoint install` first", keyPath), 1)
	}
	certPath := keyPath + "-cert.pub"
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		fmt.Printf("Note: no cert at %s — autossh will rely on raw-key auth, which a cert-only bastion will reject.\n", certPath)
		fmt.Printf("Run `bastionhub endpoint enroll <name> --pubkey-file %s.pub` on the engineer laptop, then ship the resulting -cert.pub back here.\n\n", keyPath)
	}

	brewPrefixOut, err := exec.Command("brew", "--prefix").Output()
	if err != nil {
		return cli.Exit("can't detect brew prefix — is Homebrew installed? Run `bastionhub endpoint install` first", 1)
	}
	brewPrefix := strings.TrimSpace(string(brewPrefixOut))
	autosshPath := filepath.Join(brewPrefix, "bin", "autossh")
	if _, err := os.Stat(autosshPath); os.IsNotExist(err) {
		return cli.Exit(fmt.Sprintf("autossh not found at %s — run `bastionhub endpoint install` first", autosshPath), 1)
	}

	logDir := darwinLogDir()
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	knownHostsPath := darwinKnownHostsPath()

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>-M</string><string>0</string>
        <string>-N</string>
        <string>-i</string><string>%s</string>
        <string>-o</string><string>IdentitiesOnly=yes</string>
        <string>-o</string><string>StrictHostKeyChecking=accept-new</string>
        <string>-o</string><string>UserKnownHostsFile=%s</string>
        <string>-o</string><string>ServerAliveInterval=30</string>
        <string>-o</string><string>ServerAliveCountMax=3</string>
        <string>-o</string><string>ExitOnForwardFailure=yes</string>
        <string>-R</string><string>%d:localhost:22</string>
        <string>%s@%s</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>AUTOSSH_GATETIME</key><string>30</string>
        <key>AUTOSSH_LOGFILE</key><string>%s/autossh.log</string>
    </dict>
    <key>StandardOutPath</key>
    <string>%s/stdout.log</string>
    <key>StandardErrorPath</key>
    <string>%s/stderr.log</string>
</dict>
</plist>
`, darwinLaunchLabel, autosshPath, keyPath, knownHostsPath, port, bastionUser, bastion, logDir, logDir, logDir)

	plistPath := darwinLaunchAgentPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return cli.Exit(err.Error(), 1)
	}

	if cmd.Bool("dry-run") {
		fmt.Printf("--dry-run: would write %s:\n\n%s\n", plistPath, plist)
		fmt.Printf("--dry-run: would run: launchctl bootstrap gui/%d %s\n", os.Getuid(), plistPath)
		return nil
	}

	// If already loaded, bootout first (idempotent re-setup). Failure is OK
	// if it wasn't loaded.
	if _, err := os.Stat(plistPath); err == nil {
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), plistPath).Run()
	}

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return cli.Exit(err.Error(), 1)
	}
	fmt.Println("Wrote", plistPath)

	bootstrapOut, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath).CombinedOutput()
	if err != nil {
		return cli.Exit(fmt.Sprintf("launchctl bootstrap failed: %v\n%s", err, bootstrapOut), 1)
	}

	time.Sleep(2 * time.Second)

	listOut, _ := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), darwinLaunchLabel)).CombinedOutput()
	fmt.Println()
	fmt.Println(string(listOut))
	fmt.Println("Done. From the engineer laptop: `bastionhub status` to verify UP.")
	fmt.Printf("Logs: %s/{stdout,stderr,autossh}.log\n", logDir)
	return nil
}

// -----------------------------------------------------------------------------
// main — wire up commands
// -----------------------------------------------------------------------------

func main() {
	app := &cli.Command{
		Name:    "bastionhub",
		Usage:   "self-hosted SSH bastion + reverse-tunnel substrate (pairs with sshca for cert auth)",
		Version: version,
		Commands: []*cli.Command{
			{
				Name:      "ssh",
				Usage:     "SSH into a configured endpoint via the bastion",
				ArgsUsage: "<endpoint-name> [-- extra-ssh-args]",
				Action:    sshCmd,
			},
			{
				Name:   "list",
				Usage:  "list configured endpoints",
				Action: listCmd,
			},
			{
				Name:   "status",
				Usage:  "show fleet liveness (which endpoints have an active reverse tunnel)",
				Action: statusCmd,
			},
			{
				Name:   "init",
				Usage:  "create a starter endpoints.yaml at ~/.config/bastionhub/",
				Action: initCmd,
			},
			{
				Name:  "endpoint",
				Usage: "manage endpoints (hosts that hold reverse tunnels)",
				Commands: []*cli.Command{
					{
						Name:      "register",
						Usage:     "[engineer-side] add an endpoint to endpoints.yaml",
						ArgsUsage: "<name>",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "user", Usage: "the user to SSH as on the endpoint", Value: "root"},
							&cli.IntFlag{Name: "port", Usage: "bastion-side listen port (default: next free in 12001-12099)"},
							&cli.StringFlag{Name: "identity", Usage: "path to SSH key to use for auth to this endpoint (sets -i + IdentitiesOnly=yes)"},
							&cli.StringFlag{Name: "description", Aliases: []string{"d"}, Usage: "free-form description"},
						},
						Action: endpointRegisterCmd,
					},
					{
						Name:      "unregister",
						Usage:     "[engineer-side] remove an endpoint from endpoints.yaml (cert revocation is separate; see `sshca cert revoke`)",
						ArgsUsage: "<name>",
						Action:    endpointUnregisterCmd,
					},
					{
						Name:      "enroll",
						Usage:     "[engineer-side] one-shot: sign a tunnel cert (via sshca) + register the endpoint",
						ArgsUsage: "<name>",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "pubkey-file", Usage: "path to the endpoint's outbound SSH public key (from `bastionhub endpoint install`)", Required: true},
							&cli.StringFlag{Name: "user", Usage: "the user to SSH as on the endpoint", Value: "root"},
							&cli.IntFlag{Name: "port", Usage: "bastion-side listen port (default: next free in 12001-12099)"},
							&cli.StringFlag{Name: "identity", Usage: "path to SSH key to use for auth to this endpoint"},
							&cli.StringFlag{Name: "description", Aliases: []string{"d"}, Usage: "free-form description"},
							&cli.StringFlag{Name: "valid", Usage: "cert validity window (default '+52w' for daemons)"},
							&cli.StringFlag{Name: "principal", Usage: "cert principal (default 'gw-tunnel')"},
							&cli.StringFlag{Name: "ca-dir", Usage: "passed through to `sshca cert sign --dir`"},
						},
						Action: endpointEnrollCmd,
					},
					{
						Name:   "install",
						Usage:  "[endpoint-side, Linux/macOS] install autossh + generate key + print pubkey",
						Action: endpointInstallCmd,
					},
					{
						Name:  "setup",
						Usage: "[endpoint-side, Linux/macOS] write service unit/plist and start the tunnel",
						Flags: []cli.Flag{
							&cli.IntFlag{Name: "port", Usage: "the port allocated by enroll/register", Required: true},
							&cli.StringFlag{Name: "bastion", Usage: "the bastion's public hostname or IP", Required: true},
							&cli.StringFlag{Name: "bastion-user", Usage: "the bastion-side user (default: gw-tunnel)", Value: "gw-tunnel"},
							&cli.BoolFlag{Name: "dry-run", Usage: "print the service unit / plist + commands without writing or loading"},
						},
						Action: endpointSetupCmd,
					},
				},
			},
		},
	}
	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
