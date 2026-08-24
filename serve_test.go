package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const testPubkey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH0mzT2Zw0z7hK6l0YKq1L5Xn5cM9pQ2rS3tU4vW5xY6 test@example"
const testCert = "ssh-ed25519-cert-v01@openssh.com AAAAIHNzaC1lZDI1NTE5LWNlcnQ test@example"

func newTestServer(t *testing.T) *server {
	t.Helper()
	store, err := newInviteStore("") // in-memory
	if err != nil {
		t.Fatalf("newInviteStore: %v", err)
	}
	return &server{
		store:      store,
		adminToken: "test-admin-token",
		bastion:    "bastion.example.io",
		baseURL:    "https://bastion.example.io",
	}
}

func (s *server) req(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.RemoteAddr = "203.0.113.7:54321"
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

// mintInvite creates an invite through the API and returns its code.
func mintInvite(t *testing.T, s *server, name string, port int) string {
	t.Helper()
	body := `{"name":"` + name + `","port":` + itoa(port) + `,"principal":"gw-tunnel","valid":"+8h","shape":"device"}`
	w := s.req(t, http.MethodPost, "/api/invite", body, s.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("create invite: status %d body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Code
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// -----------------------------------------------------------------------------
// The property the whole design rests on: no CA, no signing, on the service.
// -----------------------------------------------------------------------------

func TestServiceNeverSigns(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)

	// Far end submits a pubkey.
	if w := s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, ""); w.Code != http.StatusOK {
		t.Fatalf("pubkey submit: status %d body %s", w.Code, w.Body.String())
	}
	// Without an operator uploading a cert, the far end gets 202 forever.
	// If the service could sign, this would return 200 with a certificate.
	for i := 0; i < 3; i++ {
		w := s.req(t, http.MethodGet, "/e/"+code+"/cert", "", "")
		if w.Code != http.StatusAccepted {
			t.Fatalf("attempt %d: expected 202 (nothing can sign), got %d body %s", i, w.Code, w.Body.String())
		}
	}
	inv := s.store.get(code)
	if inv.Cert != "" {
		t.Fatal("a cert materialized without an operator — the service is signing")
	}
}

// -----------------------------------------------------------------------------
// Invite lifecycle
// -----------------------------------------------------------------------------

func TestFullRelayFlow(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)

	if got := s.store.get(code).state(time.Now().UTC()); got != "pending" {
		t.Fatalf("state = %q, want pending", got)
	}
	if w := s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, ""); w.Code != http.StatusOK {
		t.Fatalf("pubkey: %d", w.Code)
	}
	if got := s.store.get(code).state(time.Now().UTC()); got != "pubkey-received" {
		t.Fatalf("state = %q, want pubkey-received", got)
	}

	// Operator sees the pubkey.
	w := s.req(t, http.MethodGet, "/api/invite/"+code, "", s.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("operator poll: %d", w.Code)
	}
	var st struct {
		Pubkey string `json:"pubkey"`
		State  string `json:"state"`
	}
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Pubkey != testPubkey {
		t.Fatalf("pubkey relayed wrong: %q", st.Pubkey)
	}

	// Operator uploads the signed cert.
	if w := s.req(t, http.MethodPost, "/api/invite/"+code+"/cert", testCert, s.adminToken); w.Code != http.StatusOK {
		t.Fatalf("cert upload: %d body %s", w.Code, w.Body.String())
	}
	// Far end fetches it.
	w = s.req(t, http.MethodGet, "/e/"+code+"/cert", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("cert fetch: %d body %s", w.Code, w.Body.String())
	}
	var got struct {
		Cert    string `json:"cert"`
		Port    int    `json:"port"`
		Bastion string `json:"bastion"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Cert != testCert {
		t.Fatalf("cert = %q", got.Cert)
	}
	if got.Port != 12005 || got.Bastion != "bastion.example.io" {
		t.Fatalf("port/bastion = %d/%s", got.Port, got.Bastion)
	}
}

func TestInviteIsSingleUse(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, "")
	s.req(t, http.MethodPost, "/api/invite/"+code+"/cert", testCert, s.adminToken)

	if w := s.req(t, http.MethodGet, "/e/"+code+"/cert", "", ""); w.Code != http.StatusOK {
		t.Fatalf("first fetch: %d", w.Code)
	}
	// Second fetch must fail: the invite is spent.
	if w := s.req(t, http.MethodGet, "/e/"+code+"/cert", "", ""); w.Code == http.StatusOK {
		t.Fatal("invite was redeemable twice")
	}
}

func TestExpiredInviteRejected(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	// Fast-forward past expiry.
	s.now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }

	if w := s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, ""); w.Code == http.StatusOK {
		t.Fatal("expired invite accepted a pubkey")
	}
}

// A second party who learns the code must not be able to swap in their own key
// after the operator has already seen (and confirmed) the first one.
func TestFirstPubkeyWins(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	if w := s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, ""); w.Code != http.StatusOK {
		t.Fatalf("first pubkey: %d", w.Code)
	}
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDIFFERENTKEYDIFFERENTKEYDIFFERENTKEYxyz attacker@example"
	if w := s.req(t, http.MethodPost, "/e/"+code+"/pubkey", other, ""); w.Code == http.StatusOK {
		t.Fatal("a second, different pubkey overwrote the first")
	}
	if s.store.get(code).Pubkey != testPubkey {
		t.Fatal("stored pubkey was replaced")
	}
}

// -----------------------------------------------------------------------------
// Auth
// -----------------------------------------------------------------------------

func TestAdminRoutesRequireToken(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)

	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/invite", `{"name":"x","port":12006}`},
		{http.MethodGet, "/api/invite/" + code, ""},
		{http.MethodPost, "/api/invite/" + code + "/cert", testCert},
		{http.MethodGet, "/api/invites", ""},
		{http.MethodDelete, "/api/invite/" + code, ""},
	}
	for _, c := range cases {
		if w := s.req(t, c.method, c.path, c.body, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token: got %d, want 401", c.method, c.path, w.Code)
		}
		if w := s.req(t, c.method, c.path, c.body, "wrong-token"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with bad token: got %d, want 401", c.method, c.path, w.Code)
		}
	}
}

// The far end must not learn whether a code exists, is expired, or is spent.
func TestUnknownCodeIsIndistinguishable(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, "")
	s.req(t, http.MethodPost, "/api/invite/"+code+"/cert", testCert, s.adminToken)
	s.req(t, http.MethodGet, "/e/"+code+"/cert", "", "") // claim it

	spent := s.req(t, http.MethodGet, "/j/"+code, "", "")
	unknown := s.req(t, http.MethodGet, "/j/ZZZZZZZZ", "", "")

	if spent.Code != unknown.Code {
		t.Errorf("spent=%d unknown=%d — status leaks invite existence", spent.Code, unknown.Code)
	}
	if spent.Body.String() != unknown.Body.String() {
		t.Errorf("body differs:\n spent:   %s\n unknown: %s", spent.Body.String(), unknown.Body.String())
	}
}

func TestPrivateKeyIsNeverAccepted(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	priv := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"
	if w := s.req(t, http.MethodPost, "/e/"+code+"/pubkey", priv, ""); w.Code == http.StatusOK {
		t.Fatal("service accepted a private key")
	}
}

func TestCertUploadRejectsNonCert(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, "")
	// A bare pubkey is not a certificate.
	if w := s.req(t, http.MethodPost, "/api/invite/"+code+"/cert", testPubkey, s.adminToken); w.Code == http.StatusOK {
		t.Fatal("accepted a non-certificate as a cert")
	}
}

// -----------------------------------------------------------------------------
// Codes
// -----------------------------------------------------------------------------

func TestInviteCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c, err := newInviteCode()
		if err != nil {
			t.Fatalf("newInviteCode: %v", err)
		}
		if len(c) != inviteCodeLen {
			t.Fatalf("len(%q) = %d, want %d", c, len(c), inviteCodeLen)
		}
		for _, r := range c {
			if !strings.ContainsRune(inviteAlphabet, r) {
				t.Fatalf("code %q contains %q, outside the alphabet", c, r)
			}
		}
		if seen[c] {
			t.Fatalf("duplicate code %q in 500 draws", c)
		}
		seen[c] = true
	}
}

func TestAlphabetHasNoAmbiguousCharacters(t *testing.T) {
	for _, bad := range []rune{'0', 'O', '1', 'I', 'L'} {
		if strings.ContainsRune(inviteAlphabet, bad) {
			t.Errorf("alphabet contains %q — codes get read aloud", bad)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	cases := map[string]string{
		"k7p2-9xmn": "K7P29XMN",
		"K7P2 9XMN": "K7P29XMN",
		" k7p29xmn": "K7P29XMN",
		"K7P29XMN":  "K7P29XMN",
	}
	for in, want := range cases {
		if got := normalizeCode(in); got != want {
			t.Errorf("normalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatCodeForReading(t *testing.T) {
	if got := formatCodeForReading("K7P29XMN"); got != "K7P2-9XMN" {
		t.Errorf("got %q", got)
	}
}

// -----------------------------------------------------------------------------
// Bootstrap scripts
// -----------------------------------------------------------------------------

func TestBootstrapScriptNeverCarriesSecrets(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	w := s.req(t, http.MethodGet, "/j/"+code, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d", w.Code)
	}
	script := w.Body.String()
	if strings.Contains(script, s.adminToken) {
		t.Fatal("bootstrap script leaks the admin token")
	}
	for _, forbidden := range []string{"PRIVATE KEY", "user_ca", "host_ca", "sshca"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("bootstrap script mentions %q — the far end must not touch the CA", forbidden)
		}
	}
	// It must generate a key locally rather than receive one.
	if !strings.Contains(script, "ssh-keygen -t ed25519") {
		t.Error("bootstrap script does not generate a keypair on the far end")
	}
}

func TestBootstrapDoesNotConsumeInvite(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	// Fetching the script twice must both work — reading is not redeeming.
	for i := 0; i < 2; i++ {
		if w := s.req(t, http.MethodGet, "/j/"+code, "", ""); w.Code != http.StatusOK {
			t.Fatalf("fetch %d: %d", i, w.Code)
		}
	}
	if s.store.get(code).claimed() {
		t.Fatal("fetching the script claimed the invite")
	}
}

func TestPowerShellVariantServed(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	r := httptest.NewRequest(http.MethodGet, "/j/"+code, nil)
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT; WindowsPowerShell/5.1.19041)")
	r.RemoteAddr = "203.0.113.7:1234"
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "Invoke-RestMethod") {
		t.Error("PowerShell user-agent did not get the PowerShell script")
	}
}

func TestSessionShapeLeavesNothingBehind(t *testing.T) {
	s := newTestServer(t)
	body := `{"name":"work-mac","port":12003,"principal":"gw-user","valid":"+12h","shape":"session"}`
	w := s.req(t, http.MethodPost, "/api/invite", body, s.adminToken)
	var resp struct {
		Code string `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	script := s.req(t, http.MethodGet, "/j/"+resp.Code, "", "").Body.String()
	if !strings.Contains(script, "trap 'rm -rf") {
		t.Error("session script does not clean up its key directory")
	}
	if strings.Contains(script, "systemctl enable") {
		t.Error("session script installs a persistent service")
	}
}

func TestDeviceShapeInstallsService(t *testing.T) {
	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	script := s.req(t, http.MethodGet, "/j/"+code, "", "").Body.String()
	if !strings.Contains(script, "systemctl enable --now") {
		t.Error("device script does not install a systemd service")
	}
	if !strings.Contains(script, "launchctl load") {
		t.Error("device script has no macOS path")
	}
}

func TestShellQuoteHandlesQuotes(t *testing.T) {
	if got := shellQuote(`o'brien`); got != `'o'\''brien'` {
		t.Errorf("shellQuote = %s", got)
	}
}

// A name with shell metacharacters must not escape into the script.
func TestBootstrapQuotesName(t *testing.T) {
	s := newTestServer(t)
	body := `{"name":"evil; rm -rf /","port":12009,"shape":"device"}`
	w := s.req(t, http.MethodPost, "/api/invite", body, s.adminToken)
	var resp struct {
		Code string `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	script := s.req(t, http.MethodGet, "/j/"+resp.Code, "", "").Body.String()
	if strings.Contains(script, "NAME=evil; rm -rf /") {
		t.Fatal("endpoint name injected unquoted into the bootstrap script")
	}
	if !strings.Contains(script, `NAME='evil; rm -rf /'`) {
		t.Errorf("name not single-quoted; script has:\n%s", firstLineContaining(script, "NAME="))
	}
}

func firstLineContaining(s, sub string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return "(not found)"
}

// -----------------------------------------------------------------------------
// Rate limiting
// -----------------------------------------------------------------------------

func TestCodeGuessingIsRateLimited(t *testing.T) {
	s := newTestServer(t)
	var lastCode int
	for i := 0; i < maxCodeAttempts+5; i++ {
		w := s.req(t, http.MethodGet, "/j/ZZZZZZZZ", "", "")
		lastCode = w.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("after %d bad guesses, status = %d, want 429", maxCodeAttempts+5, lastCode)
	}
}

func TestPubkeyShapeCheck(t *testing.T) {
	good := []string{
		testPubkey,
		"ssh-rsa AAAAB3NzaC1yc2EAAAA test@host",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNh test@host",
	}
	bad := []string{
		"",
		"not a key",
		"ssh-ed25519",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"ssh-dss AAAAB3NzaC1kc3M test@host", // deliberately unsupported
	}
	for _, g := range good {
		if !looksLikeSSHPubkey(g) {
			t.Errorf("rejected valid pubkey: %q", g)
		}
	}
	for _, b := range bad {
		if looksLikeSSHPubkey(b) {
			t.Errorf("accepted invalid pubkey: %q", b)
		}
	}
}

// -----------------------------------------------------------------------------
// Store
// -----------------------------------------------------------------------------

func TestStorePersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/invites.json"
	s1, err := newInviteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	inv := &Invite{Code: "TESTCODE", Name: "tex", Port: 12005,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), Pubkey: testPubkey}
	if err := s1.create(inv); err != nil {
		t.Fatal(err)
	}

	s2, err := newInviteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.get("TESTCODE")
	if got == nil {
		t.Fatal("invite did not survive restart")
	}
	if got.Pubkey != testPubkey || got.Port != 12005 {
		t.Fatalf("invite corrupted across restart: %+v", got)
	}
}

func TestStoreGCDropsOldInvites(t *testing.T) {
	s, _ := newInviteStore("")
	old := time.Now().UTC().Add(-3 * time.Hour)
	s.invites["OLDCODE"] = &Invite{Code: "OLDCODE", CreatedAt: old, ExpiresAt: old.Add(time.Minute)}
	s.mu.Lock()
	s.gcLocked(time.Now().UTC())
	s.mu.Unlock()
	if s.get("OLDCODE") != nil {
		t.Error("expired invite was not garbage-collected")
	}
}

func TestGetReturnsCopy(t *testing.T) {
	s, _ := newInviteStore("")
	now := time.Now().UTC()
	s.invites["C"] = &Invite{Code: "C", Name: "orig", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	got := s.get("C")
	got.Name = "mutated"
	if s.get("C").Name != "orig" {
		t.Error("get() handed out a pointer into the store")
	}
}

// The bootstrap scripts are shipped as text and piped straight into a shell on
// a machine we do not control. `sh -n` is the cheapest way to be sure a syntax
// error never reaches a technician standing next to a controller.
func TestBootstrapScriptsAreValidShell(t *testing.T) {
	s := newTestServer(t)
	for _, shape := range []inviteShape{shapeDevice, shapeSession} {
		inv := &Invite{Code: "TESTCODE", Name: "tex-mmv2", Port: 12005, Shape: shape}
		script := shellBootstrap(s.baseURL, inv)
		dir := t.TempDir()
		path := dir + "/bootstrap.sh"
		if err := osWriteFile(path, script); err != nil {
			t.Fatal(err)
		}
		if out, err := runCmd("sh", "-n", path); err != nil {
			t.Errorf("shape %s: script is not valid sh: %v\n%s", shape, err, out)
		}
	}
}

func osWriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// A session invite promises that closing the window leaves nothing behind.
// `exec ssh` would replace the shell and discard the EXIT trap, stranding the
// private key in TMPDIR — which is exactly what happened the first time this
// script was run end to end.
func TestSessionScriptDoesNotExecAwayItsCleanup(t *testing.T) {
	s := newTestServer(t)
	inv := &Invite{Code: "TESTCODE", Name: "work-mac", Port: 12003, Shape: shapeSession}
	script := shellBootstrap(s.baseURL, inv)

	if strings.Contains(script, "exec ssh") {
		t.Error("session script uses `exec ssh` — that discards the EXIT trap and the key survives the session")
	}
	if !strings.Contains(script, `rm -rf "$KEYDIR"`) {
		t.Error("session script never removes its key directory")
	}
	// The removal must come after ssh returns, not before it runs.
	sshAt := strings.Index(script, "ssh $SSH_ARGS")
	rmAt := strings.LastIndex(script, `rm -rf "$KEYDIR"`)
	if sshAt < 0 || rmAt < sshAt {
		t.Error("cleanup does not follow the ssh invocation")
	}
}

func TestPowerShellSessionCleansUpOnInterrupt(t *testing.T) {
	s := newTestServer(t)
	inv := &Invite{Code: "TESTCODE", Name: "work-mac", Port: 12003, Shape: shapeSession}
	script := powershellBootstrap(s.baseURL, inv)
	if !strings.Contains(script, "finally {") {
		t.Error("PowerShell session path has no finally{} — Ctrl-C would strand the key")
	}
}

// The far end must not give up while its invite is still live. Hardcoding the
// poll bound made `--ttl 2h` fail at 30 minutes with nothing actually wrong.
func TestPollBoundTracksInviteTTL(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	for _, tc := range []struct{ ttl, minPolls time.Duration }{
		{30 * time.Minute, 30 * time.Minute},
		{2 * time.Hour, 2 * time.Hour},
		{5 * time.Minute, 5 * time.Minute},
	} {
		inv := &Invite{Code: "TESTCODE", Name: "x", Port: 12005, Shape: shapeDevice,
			CreatedAt: now, ExpiresAt: now.Add(tc.ttl)}
		covered := time.Duration(pollsUntil(inv)) * pollInterval
		if covered < tc.minPolls {
			t.Errorf("ttl %v: script polls cover only %v", tc.ttl, covered)
		}
		for _, script := range []string{shellBootstrap(s.baseURL, inv), powershellBootstrap(s.baseURL, inv)} {
			if strings.Contains(script, "900") {
				t.Errorf("ttl %v: script still carries the hardcoded bound", tc.ttl)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// The access shape — a machine that reaches the fleet rather than being reached
// -----------------------------------------------------------------------------

func TestAccessShapeOpensNoTunnel(t *testing.T) {
	s := newTestServer(t)
	inv := &Invite{Code: "TESTCODE", Name: "work-mac", Shape: shapeAccess}
	script := shellBootstrap(s.baseURL, inv)

	for _, forbidden := range []string{"-R ", "systemctl enable", "launchctl load", "gw-tunnel@"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("access script contains %q — it must not open or persist a tunnel", forbidden)
		}
	}
	if !strings.Contains(script, "User gw-user") {
		t.Error("access script does not configure gw-user")
	}
	if !strings.Contains(script, "Host bastionhub") {
		t.Error("access script writes no ssh config block")
	}
}

// The cert is the access; a temp dir with a cleanup trap would throw it away.
func TestAccessShapeKeepsItsCert(t *testing.T) {
	s := newTestServer(t)
	inv := &Invite{Code: "TESTCODE", Name: "work-mac", Shape: shapeAccess}
	script := shellBootstrap(s.baseURL, inv)
	if strings.Contains(script, "trap 'rm -rf") {
		t.Error("access script deletes its own key on exit")
	}
	if !strings.Contains(script, `KEYDIR="$HOME/.ssh"`) {
		t.Error("access script does not write into ~/.ssh")
	}
}

// Re-running must not stack duplicate Host stanzas in ~/.ssh/config.
func TestAccessShapeIsIdempotent(t *testing.T) {
	s := newTestServer(t)
	inv := &Invite{Code: "TESTCODE", Name: "work-mac", Shape: shapeAccess}
	script := shellBootstrap(s.baseURL, inv)
	if !strings.Contains(script, "awk") || !strings.Contains(script, "grep -q") {
		t.Error("access script does not strip a previously written block before appending")
	}
}

func TestPrincipalFollowsShape(t *testing.T) {
	cases := map[inviteShape]string{
		shapeDevice:  "gw-tunnel",
		shapeSession: "gw-tunnel",
		shapeAccess:  "gw-user",
	}
	for shape, want := range cases {
		if got := defaultPrincipalFor(shape); got != want {
			t.Errorf("defaultPrincipalFor(%s) = %q, want %q", shape, got, want)
		}
	}
}

func TestAccessInviteNeedsNoPort(t *testing.T) {
	s := newTestServer(t)
	body := `{"name":"work-mac","shape":"access","principal":"gw-user","valid":"+12h"}`
	w := s.req(t, http.MethodPost, "/api/invite", body, s.adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("access invite without a port was rejected: %d %s", w.Code, w.Body.String())
	}
	// Device invites still require one — nothing would listen otherwise.
	body = `{"name":"tex","shape":"device","principal":"gw-tunnel"}`
	if w := s.req(t, http.MethodPost, "/api/invite", body, s.adminToken); w.Code == http.StatusOK {
		t.Error("device invite without a port was accepted")
	}
}

func TestAllThreeShapesProduceValidShell(t *testing.T) {
	s := newTestServer(t)
	for _, shape := range []inviteShape{shapeDevice, shapeSession, shapeAccess} {
		inv := &Invite{Code: "TESTCODE", Name: "x", Port: 12005, Shape: shape}
		dir := t.TempDir()
		path := dir + "/b.sh"
		if err := osWriteFile(path, shellBootstrap(s.baseURL, inv)); err != nil {
			t.Fatal(err)
		}
		if out, err := runCmd("sh", "-n", path); err != nil {
			t.Errorf("shape %s: invalid sh: %v\n%s", shape, err, out)
		}
	}
}

// The bastion needs a registry of its own; a copy of the operator's file
// drifts from the moment it is made.
func TestClaimedInviteIsRecordedLocally(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONHUB_CONFIG", dir+"/endpoints.yaml")

	s := newTestServer(t)
	code := mintInvite(t, s, "tex-mmv2", 12005)
	s.req(t, http.MethodPost, "/e/"+code+"/pubkey", testPubkey, "")
	s.req(t, http.MethodPost, "/api/invite/"+code+"/cert", testCert, s.adminToken)
	if w := s.req(t, http.MethodGet, "/e/"+code+"/cert", "", ""); w.Code != http.StatusOK {
		t.Fatalf("cert fetch: %d", w.Code)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("registry not written: %v", err)
	}
	ep, ok := cfg.Endpoints["tex-mmv2"]
	if !ok {
		t.Fatalf("endpoint absent from registry: %+v", cfg.Endpoints)
	}
	if ep.Port != 12005 {
		t.Errorf("port = %d, want 12005", ep.Port)
	}
}

// An access invite grants a machine the ability to reach the fleet. Nothing
// listens for it, so it is not an endpoint.
func TestAccessInviteIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONHUB_CONFIG", dir+"/endpoints.yaml")

	s := newTestServer(t)
	body := `{"name":"work-mac","shape":"access","principal":"gw-user","valid":"+12h"}`
	w := s.req(t, http.MethodPost, "/api/invite", body, s.adminToken)
	var resp struct {
		Code string `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	s.req(t, http.MethodPost, "/e/"+resp.Code+"/pubkey", testPubkey, "")
	s.req(t, http.MethodPost, "/api/invite/"+resp.Code+"/cert", testCert, s.adminToken)
	s.req(t, http.MethodGet, "/e/"+resp.Code+"/cert", "", "")

	if cfg, err := loadConfig(); err == nil {
		if _, ok := cfg.Endpoints["work-mac"]; ok {
			t.Error("access invite was recorded as an endpoint")
		}
	}
}

// A re-enrolled name updates in place rather than accumulating duplicates.
func TestRecordEndpointUpserts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASTIONHUB_CONFIG", dir+"/endpoints.yaml")
	now := time.Now().UTC()

	for _, port := range []int{12005, 12006} {
		if err := recordEndpoint(&Invite{
			Name: "tex-mmv2", Port: port, Shape: shapeDevice, ClaimedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Endpoints) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(cfg.Endpoints), cfg.Endpoints)
	}
	if cfg.Endpoints["tex-mmv2"].Port != 12006 {
		t.Errorf("port = %d, want the later 12006", cfg.Endpoints["tex-mmv2"].Port)
	}
}
