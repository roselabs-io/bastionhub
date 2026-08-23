// serve.go — the invite/relay service (`bastionhub serve`) and the operator-side
// `bastionhub invite` that drives it.
//
// The service is a RENDEZVOUS POINT, NOT A SIGNING AUTHORITY.
// It never holds the CA and never signs anything. It
// stores public keys and certificates and hands them between two parties who
// cannot otherwise reach each other:
//
//	far end                  bastion (this service)         operator's laptop
//	   |                              |                              |
//	   |  GET  /j/<code>   ---------> |                              |
//	   |  <--- bootstrap script       |                              |
//	   |  POST /e/<code>/pubkey ----> | (stores pubkey)              |
//	   |                              | <---- GET /api/invite/<code> | (polls)
//	   |                              |       pubkey ---->           |
//	   |                              |                        [sshca cert sign]
//	   |                              | <---- POST .../cert          |
//	   |  GET  /e/<code>/cert  -----> |                              |
//	   |  <--- certificate            |                              |
//
// Everything crossing this service is public material. A full compromise of the
// VPS yields a list of public keys and some expired codes; it cannot mint a
// certificate. That property is the reason the operator must be online when an
// invite is redeemed.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v3"
)

// -----------------------------------------------------------------------------
// Invite state
// -----------------------------------------------------------------------------

// inviteAlphabet omits 0/O/1/I/L — codes get read aloud over a phone to someone
// standing next to a controller, and "was that a one or an ell" costs more than
// the two bits it saves. 32 symbols x 8 chars = 40 bits of entropy.
const inviteAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

const (
	inviteCodeLen     = 8
	defaultInviteTTL  = 30 * time.Minute
	adminTokenBytes   = 32
	maxPubkeyBytes    = 4 << 10  // an ed25519 pubkey line is ~100 bytes
	maxCertBytes      = 16 << 10 // a cert is larger but still small
	pollInterval      = 2 * time.Second
	maxCodeAttempts   = 20 // per source address, per TTL window
	attemptWindowSize = 30 * time.Minute
)

// inviteShape decides what the bootstrap script does once it holds a cert.
type inviteShape string

const (
	// shapeDevice: a machine that stays. The tunnel runs under systemd/launchd
	// and survives reboots.
	shapeDevice inviteShape = "device"
	// shapeSession: a laptop, one sitting. The tunnel runs in the foreground of
	// the terminal window; close it and nothing remains on the machine.
	shapeSession inviteShape = "session"
	// shapeAccess: a machine that needs to REACH the fleet rather than be
	// reached by it — the operator's other laptop. It gets a gw-user cert and
	// an ssh config block for ProxyJump, and opens no tunnel at all.
	//
	// The distinction is the whole security model: gw-tunnel may listen but
	// gets no shell and no local forwards; gw-user may open local forwards so
	// ProxyJump works, but may not listen. Neither can do the other's job, so
	// one script serving both would be handing out the wrong credential half
	// the time.
	shapeAccess inviteShape = "access"
)

// defaultPrincipalFor returns the only principal that makes sense for a shape.
// Getting this wrong is silent and confusing: a gw-user cert on a machine
// trying to open a reverse tunnel authenticates and then fails to listen.
func defaultPrincipalFor(shape inviteShape) string {
	if shape == shapeAccess {
		return "gw-user"
	}
	return "gw-tunnel"
}

// defaultValidityFor: a device that stays is renewed as a scheduled event; a
// laptop is a sitting.
func defaultValidityFor(shape inviteShape) string {
	if shape == shapeDevice {
		return "+52w"
	}
	return "+12h"
}

// Invite is one pending enrollment. It is created by the operator, redeemed by
// the far end, and holds only public material at every stage.
type Invite struct {
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	Port      int         `json:"port"`
	Principal string      `json:"principal"`
	Valid     string      `json:"valid"`
	Shape     inviteShape `json:"shape"`
	User      string      `json:"user,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	Pubkey   string    `json:"pubkey,omitempty"`
	PubkeyAt time.Time `json:"pubkey_at,omitempty"`

	Cert   string    `json:"cert,omitempty"`
	CertAt time.Time `json:"cert_at,omitempty"`

	// ClaimedAt is set the moment the far end successfully fetches the cert.
	// A claimed invite is spent: it cannot be redeemed again.
	ClaimedAt time.Time `json:"claimed_at,omitempty"`
}

func (i *Invite) expired(now time.Time) bool { return now.After(i.ExpiresAt) }
func (i *Invite) claimed() bool              { return !i.ClaimedAt.IsZero() }

// state reports where an invite is in its lifecycle. The operator's `invite`
// command drives off this string.
func (i *Invite) state(now time.Time) string {
	switch {
	case i.claimed():
		return "claimed"
	case i.expired(now):
		return "expired"
	case i.Cert != "":
		return "cert-ready"
	case i.Pubkey != "":
		return "pubkey-received"
	default:
		return "pending"
	}
}

// -----------------------------------------------------------------------------
// Store — invites on disk, guarded by a mutex
// -----------------------------------------------------------------------------

// inviteStore persists invites as one JSON file. The volume is a handful of
// records with a 30-minute TTL; a database would be more machinery than the
// problem has. Every mutation rewrites the file atomically.
type inviteStore struct {
	mu      sync.Mutex
	path    string
	invites map[string]*Invite

	// attempts tracks failed code lookups per source address, so a brute-force
	// sweep across the 40-bit code space runs out of budget rather than time.
	attempts map[string]*attemptCounter
}

type attemptCounter struct {
	n     int
	since time.Time
}

func newInviteStore(path string) (*inviteStore, error) {
	s := &inviteStore{
		path:     path,
		invites:  map[string]*Invite{},
		attempts: map[string]*attemptCounter{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.invites); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if s.invites == nil {
		s.invites = map[string]*Invite{}
	}
	return s, nil
}

// saveLocked writes the store to disk. Caller must hold s.mu.
func (s *inviteStore) saveLocked() error {
	if s.path == "" {
		return nil // in-memory (tests)
	}
	data, err := json.MarshalIndent(s.invites, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// gcLocked drops invites that expired long enough ago to be uninteresting.
// Claimed and expired invites are kept for one extra TTL so the operator sees
// "expired" rather than "no such code" when they are a few minutes late.
func (s *inviteStore) gcLocked(now time.Time) {
	for code, inv := range s.invites {
		if now.After(inv.ExpiresAt.Add(defaultInviteTTL)) {
			delete(s.invites, code)
		}
	}
	for addr, a := range s.attempts {
		if now.Sub(a.since) > attemptWindowSize {
			delete(s.attempts, addr)
		}
	}
}

func (s *inviteStore) create(inv *Invite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(time.Now().UTC())
	s.invites[inv.Code] = inv
	return s.saveLocked()
}

// get looks up an invite. It returns nil for both "no such code" and "expired
// past the grace window" — the far end must not be able to tell those apart.
func (s *inviteStore) get(code string) *Invite {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invites[code]
	if !ok {
		return nil
	}
	// Return a copy so callers can't mutate store state without going through
	// an update method.
	cp := *inv
	return &cp
}

// update applies fn to the stored invite under the lock and persists the result.
func (s *inviteStore) update(code string, fn func(*Invite) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invites[code]
	if !ok {
		return errNoSuchInvite
	}
	if err := fn(inv); err != nil {
		return err
	}
	return s.saveLocked()
}

func (s *inviteStore) list() []*Invite {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Invite, 0, len(s.invites))
	for _, inv := range s.invites {
		cp := *inv
		out = append(out, &cp)
	}
	return out
}

// recordAttempt counts a failed code lookup from addr and reports whether the
// source has burned its budget.
func (s *inviteStore) recordAttempt(addr string) (blocked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	a, ok := s.attempts[addr]
	if !ok || now.Sub(a.since) > attemptWindowSize {
		a = &attemptCounter{since: now}
		s.attempts[addr] = a
	}
	a.n++
	return a.n > maxCodeAttempts
}

func (s *inviteStore) blocked(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[addr]
	if !ok {
		return false
	}
	if time.Now().UTC().Sub(a.since) > attemptWindowSize {
		return false
	}
	return a.n > maxCodeAttempts
}

var errNoSuchInvite = errors.New("no such invite")

// -----------------------------------------------------------------------------
// Code + token generation
// -----------------------------------------------------------------------------

// newInviteCode returns a fresh code. Uses crypto/rand with rejection sampling
// so the alphabet stays uniform (len is 31, not a power of two).
func newInviteCode() (string, error) {
	b := make([]byte, 0, inviteCodeLen)
	buf := make([]byte, 1)
	limit := byte(256 - (256 % len(inviteAlphabet)))
	for len(b) < inviteCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= limit {
			continue // would bias the distribution; draw again
		}
		b = append(b, inviteAlphabet[int(buf[0])%len(inviteAlphabet)])
	}
	return string(b), nil
}

// newAdminToken returns a fresh admin token as lowercase hex.
func newAdminToken() (string, error) {
	b := make([]byte, adminTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// normalizeCode uppercases and strips separators, so an operator reading
// "k7p2-9xmn" aloud and a technician typing "K7P29XMN" agree.
func normalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if strings.ContainsRune(inviteAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// -----------------------------------------------------------------------------
// The server
// -----------------------------------------------------------------------------

type server struct {
	store      *inviteStore
	adminToken string
	bastion    string // public hostname the far end dials for SSH
	baseURL    string // public base URL of this service, for the bootstrap line
	now        func() time.Time
}

func (s *server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// authAdmin reports whether the request carries the admin token. Comparison is
// constant-time: the token is a bearer secret and a timing oracle on it would
// let an attacker mint invites, which is the one privileged operation here.
func (s *server) authAdmin(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.adminToken)) == 1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func sourceAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// lookupForFarEnd resolves a code submitted by an untrusted party. Every
// failure mode returns the same 404 and the same message: an attacker probing
// codes must not learn whether a code exists, is expired, or is already spent.
func (s *server) lookupForFarEnd(w http.ResponseWriter, r *http.Request, code string) *Invite {
	addr := sourceAddr(r)
	if s.store.blocked(addr) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts; wait and ask the operator for a fresh code")
		return nil
	}
	inv := s.store.get(normalizeCode(code))
	if inv == nil || inv.expired(s.clock()) || inv.claimed() {
		if s.store.recordAttempt(addr) {
			writeErr(w, http.StatusTooManyRequests, "too many attempts; wait and ask the operator for a fresh code")
			return nil
		}
		writeErr(w, http.StatusNotFound, "invite not found, already used, or expired — ask the operator for a fresh code")
		return nil
	}
	return inv
}

// --- Operator-facing API (admin token required) ------------------------------

// handleInviteCreate: POST /api/invite
func (s *server) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "bad or missing admin token")
		return
	}
	var req struct {
		Name       string `json:"name"`
		Port       int    `json:"port"`
		Principal  string `json:"principal"`
		Valid      string `json:"valid"`
		Shape      string `json:"shape"`
		User       string `json:"user"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Port == 0 && inviteShape(req.Shape) != shapeAccess {
		writeErr(w, http.StatusBadRequest, "port is required for device and session invites")
		return
	}
	shape := inviteShape(req.Shape)
	if shape != shapeDevice && shape != shapeSession && shape != shapeAccess {
		shape = shapeDevice
	}
	ttl := defaultInviteTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	code, err := newInviteCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not generate code")
		return
	}
	now := s.clock()
	inv := &Invite{
		Code:      code,
		Name:      req.Name,
		Port:      req.Port,
		Principal: req.Principal,
		Valid:     req.Valid,
		Shape:     shape,
		User:      req.User,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := s.store.create(inv); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not store invite")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       code,
		"expires_at": inv.ExpiresAt,
		"unix_cmd":   fmt.Sprintf("curl -sSL %s/j/%s | sh", s.baseURL, code),
		"windows_cmd": fmt.Sprintf("iwr -useb %s/j/%s | iex",
			s.baseURL, code),
	})
}

// handleInviteGet: GET /api/invite/<code> — the operator polls this.
func (s *server) handleInviteGet(w http.ResponseWriter, r *http.Request, code string) {
	if !s.authAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "bad or missing admin token")
		return
	}
	inv := s.store.get(normalizeCode(code))
	if inv == nil {
		writeErr(w, http.StatusNotFound, "no such invite")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       inv.Code,
		"name":       inv.Name,
		"port":       inv.Port,
		"principal":  inv.Principal,
		"valid":      inv.Valid,
		"shape":      inv.Shape,
		"state":      inv.state(s.clock()),
		"pubkey":     inv.Pubkey,
		"expires_at": inv.ExpiresAt,
	})
}

// handleInviteCert: POST /api/invite/<code>/cert — the operator uploads the
// signed certificate. This is the only way a cert enters the service, and it
// arrives already signed: the service has no CA and no signing path.
func (s *server) handleInviteCert(w http.ResponseWriter, r *http.Request, code string) {
	if !s.authAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "bad or missing admin token")
		return
	}
	body, err := readLimited(r, maxCertBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read body")
		return
	}
	cert := strings.TrimSpace(string(body))
	if !strings.Contains(cert, "-cert-v01@openssh.com") {
		writeErr(w, http.StatusBadRequest, "body does not look like an OpenSSH certificate")
		return
	}
	err = s.store.update(normalizeCode(code), func(inv *Invite) error {
		if inv.expired(s.clock()) {
			return errors.New("invite expired")
		}
		if inv.Pubkey == "" {
			return errors.New("no pubkey submitted yet")
		}
		inv.Cert = cert
		inv.CertAt = s.clock()
		return nil
	})
	if err != nil {
		if errors.Is(err, errNoSuchInvite) {
			writeErr(w, http.StatusNotFound, "no such invite")
			return
		}
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cert stored"})
}

// handleInviteList: GET /api/invites
func (s *server) handleInviteList(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "bad or missing admin token")
		return
	}
	now := s.clock()
	out := []map[string]any{}
	for _, inv := range s.store.list() {
		out = append(out, map[string]any{
			"code": inv.Code, "name": inv.Name, "port": inv.Port,
			"state": inv.state(now), "expires_at": inv.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleInviteRevoke: DELETE /api/invite/<code> — burn an unredeemed invite.
func (s *server) handleInviteRevoke(w http.ResponseWriter, r *http.Request, code string) {
	if !s.authAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "bad or missing admin token")
		return
	}
	err := s.store.update(normalizeCode(code), func(inv *Invite) error {
		inv.ExpiresAt = s.clock().Add(-time.Second)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such invite")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// --- Far-end API (invite code is the only credential) ------------------------

// handlePubkeySubmit: POST /e/<code>/pubkey
//
// The far end sends the public half of a keypair it generated locally. The
// private half never leaves that machine — which is the entire difference
// between this flow and the one that failed on 2026-08-20.
func (s *server) handlePubkeySubmit(w http.ResponseWriter, r *http.Request, code string) {
	inv := s.lookupForFarEnd(w, r, code)
	if inv == nil {
		return
	}
	body, err := readLimited(r, maxPubkeyBytes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read body")
		return
	}
	pubkey := strings.TrimSpace(string(body))
	if !looksLikeSSHPubkey(pubkey) {
		writeErr(w, http.StatusBadRequest, "body does not look like an SSH public key")
		return
	}
	err = s.store.update(normalizeCode(code), func(inv *Invite) error {
		if inv.expired(s.clock()) || inv.claimed() {
			return errors.New("invite no longer valid")
		}
		// First submission wins. Without this, anyone who learns the code
		// could overwrite a pubkey the operator is already looking at, and
		// the fingerprint the operator confirmed out-of-band would no longer
		// be the key that gets signed.
		if inv.Pubkey != "" && inv.Pubkey != pubkey {
			return errors.New("a different key was already submitted for this invite")
		}
		inv.Pubkey = pubkey
		inv.PubkeyAt = s.clock()
		return nil
	})
	if err != nil {
		if errors.Is(err, errNoSuchInvite) {
			writeErr(w, http.StatusNotFound, "invite not found")
			return
		}
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "waiting for operator to sign"})
}

// handleCertFetch: GET /e/<code>/cert
//
// Returns 202 while the operator has not signed yet, so the bootstrap script
// can poll. On success the invite is marked claimed and is spent.
func (s *server) handleCertFetch(w http.ResponseWriter, r *http.Request, code string) {
	inv := s.lookupForFarEnd(w, r, code)
	if inv == nil {
		return
	}
	if inv.Cert == "" {
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "not signed yet"})
		return
	}
	var cert string
	var meta *Invite
	err := s.store.update(normalizeCode(code), func(inv *Invite) error {
		if inv.claimed() {
			return errors.New("already claimed")
		}
		if inv.Cert == "" {
			return errors.New("not signed yet")
		}
		inv.ClaimedAt = s.clock()
		cert = inv.Cert
		cp := *inv
		meta = &cp
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cert":    cert,
		"port":    meta.Port,
		"bastion": s.bastion,
		"shape":   meta.Shape,
		"name":    meta.Name,
	})
}

// handleBootstrap: GET /j/<code> — serves the script the far end pipes to a
// shell. Windows clients get PowerShell, everyone else gets sh.
//
// This endpoint deliberately does NOT consume the invite: fetching the script
// is not redeeming it. Only fetching the cert is.
func (s *server) handleBootstrap(w http.ResponseWriter, r *http.Request, code string) {
	inv := s.lookupForFarEnd(w, r, code)
	if inv == nil {
		return
	}
	ps := wantsPowerShell(r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if ps {
		fmt.Fprint(w, powershellBootstrap(s.baseURL, inv))
		return
	}
	fmt.Fprint(w, shellBootstrap(s.baseURL, inv))
}

// wantsPowerShell guesses the far end's shell. `iwr -useb` sends a
// WindowsPowerShell user-agent; an explicit ?ps=1 overrides for the case where
// someone fetches the script in a browser to read it first.
func wantsPowerShell(r *http.Request) bool {
	if r.URL.Query().Get("ps") == "1" {
		return true
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	return strings.Contains(ua, "powershell") || strings.Contains(ua, "windowspowershell")
}

func readLimited(r *http.Request, n int64) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 4096)
	var total int64
	for {
		m, err := r.Body.Read(tmp)
		if m > 0 {
			total += int64(m)
			if total > n {
				return nil, errors.New("body too large")
			}
			buf = append(buf, tmp[:m]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, nil
		}
	}
}

// looksLikeSSHPubkey is a shape check, not validation. The authoritative check
// is ssh-keygen on the operator's machine, which refuses to sign garbage.
func looksLikeSSHPubkey(s string) bool {
	if strings.Contains(s, "PRIVATE KEY") {
		return false // never accept a private key, even by accident
	}
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return false
	}
	switch fields[0] {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "sk-ssh-ed25519@openssh.com":
		return true
	}
	return false
}

// routes wires the handlers. Paths carry the code as a segment, so this uses a
// small manual router rather than pulling in a dependency — bastionhub ships
// with two.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/invite", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		s.handleInviteCreate(w, r)
	})
	mux.HandleFunc("/api/invites", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		s.handleInviteList(w, r)
	})
	mux.HandleFunc("/api/invite/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/invite/")
		code, tail, _ := strings.Cut(rest, "/")
		switch {
		case tail == "cert" && r.Method == http.MethodPost:
			s.handleInviteCert(w, r, code)
		case tail == "" && r.Method == http.MethodGet:
			s.handleInviteGet(w, r, code)
		case tail == "" && r.Method == http.MethodDelete:
			s.handleInviteRevoke(w, r, code)
		default:
			writeErr(w, http.StatusNotFound, "no such route")
		}
	})
	mux.HandleFunc("/e/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/e/")
		code, tail, _ := strings.Cut(rest, "/")
		switch {
		case tail == "pubkey" && r.Method == http.MethodPost:
			s.handlePubkeySubmit(w, r, code)
		case tail == "cert" && r.Method == http.MethodGet:
			s.handleCertFetch(w, r, code)
		default:
			writeErr(w, http.StatusNotFound, "no such route")
		}
	})
	mux.HandleFunc("/j/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		s.handleBootstrap(w, r, strings.TrimPrefix(r.URL.Path, "/j/"))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Deliberately terse. This service is not a website and every byte it
		// serves to an unauthenticated stranger is surface.
		writeErr(w, http.StatusNotFound, "no such route")
	})
	return mux
}

// -----------------------------------------------------------------------------
// Bootstrap scripts
// -----------------------------------------------------------------------------
//
// These run on the far end, which is assumed to have NOTHING installed beyond
// what the OS ships: ssh, ssh-keygen, and curl (or PowerShell's iwr). No
// bastionhub, no autossh, no admin rights, no package manager. That constraint
// is what makes this usable at a customer site where site IT approves nothing.
//
// Persistence for a device therefore uses systemd's Restart=always (Linux) or
// launchd's KeepAlive (macOS) around plain `ssh -N -R`, with ServerAliveInterval
// doing the dead-peer detection autossh would otherwise provide.

// shellBootstrap returns the sh script served at /j/<code> for unix far ends.
//
// The tail is chosen by shape rather than branched at runtime: a session far
// end never receives the service-installation code, so "closing this window
// leaves nothing behind" is verifiable by reading the script you were asked
// to pipe into a shell.
func shellBootstrap(baseURL string, inv *Invite) string {
	tmpl := shellBootstrapCommon
	keydir, sshArgs := keydirBlockDevice, sshArgsBlock
	switch inv.Shape {
	case shapeSession:
		tmpl += shellBootstrapSessionTail
		keydir = keydirBlockSession
	case shapeAccess:
		tmpl += shellBootstrapAccessTail
		keydir, sshArgs = keydirBlockAccess, ""
	default:
		tmpl += shellBootstrapDeviceTail
	}
	r := strings.NewReplacer(
		"@@BASE@@", baseURL,
		"@@CODE@@", inv.Code,
		"@@NAME@@", shellQuote(inv.Name),
		"@@SHAPE@@", string(inv.Shape),
		"@@POLLS@@", strconv.Itoa(pollsUntil(inv)),
		"@@KEYDIR_BLOCK@@", keydir,
		"@@SSHARGS_BLOCK@@", sshArgs,
	)
	return r.Replace(tmpl)
}

// Where the keypair lives, per shape. A machine receives only its own block —
// shipping the other two would mean an access machine carrying tunnel code it
// will never run, which is exactly the kind of thing that reads as a promise.
const (
	keydirBlockDevice = `KEYDIR="$HOME/.bastionhub-tunnel"
mkdir -p "$KEYDIR"
chmod 700 "$KEYDIR"
KEY="$KEYDIR/id_ed25519"`

	// A session leaves nothing behind: the key dies with the shell.
	keydirBlockSession = `KEYDIR=$(mktemp -d "${TMPDIR:-/tmp}/bastionhub.XXXXXX")
trap 'rm -rf "$KEYDIR"' EXIT INT TERM
mkdir -p "$KEYDIR"
chmod 700 "$KEYDIR"
KEY="$KEYDIR/id_ed25519"`

	// The cert IS the access here, so it has to outlive this script.
	keydirBlockAccess = `KEYDIR="$HOME/.ssh"
mkdir -p "$KEYDIR"
KEY="$KEYDIR/bastionhub-user"`

	sshArgsBlock = `
SSH_ARGS="-N -T -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o StrictHostKeyChecking=accept-new -i $KEY -R $PORT:localhost:22 gw-tunnel@$BASTION"
`
)

// pollsUntil returns how many 2-second polls cover the invite's remaining life.
// Hardcoding this would mean a far end giving up while its invite is still
// valid — the failure looks like "timed out" on one end and "never claimed" on
// the other, with nothing actually wrong.
func pollsUntil(inv *Invite) int {
	remaining := time.Until(inv.ExpiresAt)
	if inv.ExpiresAt.IsZero() {
		remaining = defaultInviteTTL
	}
	n := int(remaining/pollInterval) + 15 // a little slack past expiry
	if n < 30 {
		n = 30
	}
	return n
}

const shellBootstrapCommon = `#!/bin/sh
# bastionhub enrollment. Generates a keypair, sends the PUBLIC half for signing,
# and waits for the certificate to come back. The private key never leaves this
# machine.
set -eu

BASE="@@BASE@@"
CODE="@@CODE@@"
NAME=@@NAME@@
SHAPE="@@SHAPE@@"

command -v ssh-keygen >/dev/null 2>&1 || { echo "error: ssh-keygen not found" >&2; exit 1; }
command -v curl       >/dev/null 2>&1 || { echo "error: curl not found" >&2; exit 1; }

@@KEYDIR_BLOCK@@

if [ ! -f "$KEY" ]; then
    echo "Generating a keypair on this machine..."
    ssh-keygen -t ed25519 -N '' -C "bastionhub-$NAME" -f "$KEY" >/dev/null
fi
chmod 600 "$KEY"

echo "Key fingerprint (read this to the operator if they ask):"
ssh-keygen -lf "$KEY.pub" | sed 's/^/    /'
echo

echo "Sending the public key..."
if ! curl -fsSL -X POST --data-binary "@$KEY.pub" "$BASE/e/$CODE/pubkey" >/dev/null; then
    echo "error: could not submit the public key. The code may be expired or already used." >&2
    exit 1
fi

echo "Waiting for the operator to sign (Ctrl-C to give up)..."
i=0
while : ; do
    i=$((i+1))
    if [ "$i" -gt @@POLLS@@ ]; then echo "error: timed out waiting for a certificate" >&2; exit 1; fi
    RESP=$(curl -fsSL -w '\n%{http_code}' "$BASE/e/$CODE/cert" 2>/dev/null) || { sleep 2; continue; }
    STATUS=$(printf '%s' "$RESP" | tail -n1)
    BODY=$(printf '%s' "$RESP" | sed '$d')
    [ "$STATUS" = "200" ] && break
    sleep 2
done

# Pull fields out of the JSON without requiring jq — the shapes are known and
# fixed, and adding a dependency here would defeat the point of this script.
extract() { printf '%s' "$BODY" | tr ',' '\n' | grep "\"$1\"" | head -n1 | sed 's/.*: *"*//; s/"*[}]*$//'; }
CERT=$(printf '%s' "$BODY" | sed 's/.*"cert" *: *"//; s/".*//' | sed 's/\\n/\n/g')
PORT=$(extract port)
BASTION=$(extract bastion)

printf '%s\n' "$CERT" > "$KEY-cert.pub"
chmod 644 "$KEY-cert.pub"
echo "Certificate received:"
ssh-keygen -Lf "$KEY-cert.pub" | grep -E 'Valid|Principals' -A1 | sed 's/^/    /'
echo

@@SSHARGS_BLOCK@@`

// shellBootstrapSessionTail holds the tunnel in the foreground. Nothing is
// installed, and the key directory is removed by the EXIT trap above.
//
// Deliberately NOT `exec ssh`: exec replaces this shell, which discards the
// trap, and the private key would outlive the session it was minted for.
// Running ssh as a child costs one process and keeps the promise.
const shellBootstrapSessionTail = `
echo "Connecting. Close this window to disconnect; nothing is left on this machine."
ssh $SSH_ARGS || true
rm -rf "$KEYDIR"
echo "Disconnected. Key material removed."
`

// shellBootstrapAccessTail sets this machine up to REACH the fleet rather than
// be reached by it. No tunnel, no service, nothing running: just a gw-user cert
// and an ssh config block, so `ssh -J bastion <host>` works.
const shellBootstrapAccessTail = `
CONF="$HOME/.ssh/config"
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh" 2>/dev/null || true

# Replace any block we wrote before, so re-running is idempotent rather than
# appending a second, conflicting Host stanza.
if [ -f "$CONF" ] && grep -q "^Host bastionhub\$" "$CONF"; then
    awk -v h="Host bastionhub" '
        $0 == h {skip=1; next}
        skip && /^Host / {skip=0}
        !skip {print}
    ' "$CONF" > "$CONF.bastionhub.tmp" && mv "$CONF.bastionhub.tmp" "$CONF"
fi

cat >> "$CONF" <<CFG
Host bastionhub
    HostName $BASTION
    User gw-user
    IdentityFile $KEY
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
CFG
chmod 600 "$CONF"

echo
echo "Done. This machine can now reach the fleet through the bastion."
echo
echo "  Test it:        ssh bastionhub true && echo ok"
echo "  Reach a device: ssh -J bastionhub <user>@localhost -p <port>"
echo
echo "The operator can tell you the port for a given device."
echo
echo "This access expires with the certificate:"
ssh-keygen -Lf "$KEY-cert.pub" | grep -i valid | sed 's/^/    /'
`

// shellBootstrapDeviceTail makes the tunnel survive reboots, using systemd or
// launchd around plain ssh — autossh would be another thing to install.
const shellBootstrapDeviceTail = `
OS=$(uname -s)
if [ "$OS" = "Linux" ] && command -v systemctl >/dev/null 2>&1 && [ "$(id -u)" = "0" ]; then
    cat > /etc/systemd/system/bastionhub-tunnel.service <<UNIT
[Unit]
Description=bastionhub reverse tunnel ($NAME)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/ssh $SSH_ARGS
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable --now bastionhub-tunnel.service
    echo "Tunnel installed as a systemd service and started."
    echo "  status: systemctl status bastionhub-tunnel"
elif [ "$OS" = "Darwin" ]; then
    PLIST="$HOME/Library/LaunchAgents/com.roselabs.bastionhub-tunnel.plist"
    mkdir -p "$HOME/Library/LaunchAgents"
    cat > "$PLIST" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.roselabs.bastionhub-tunnel</string>
  <key>ProgramArguments</key><array>
    <string>/usr/bin/ssh</string>$(for a in $SSH_ARGS; do printf '<string>%s</string>' "$a"; done)
  </array>
  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
</dict></plist>
PL
    launchctl unload "$PLIST" 2>/dev/null || true
    launchctl load "$PLIST"
    echo "Tunnel installed as a launch agent and started."
else
    echo "Could not install a service automatically (need root+systemd on Linux, or macOS)."
    echo "Run this to hold the tunnel in the foreground:"
    echo "    ssh $SSH_ARGS"
    exit 0
fi

echo
echo "Done. This machine is reachable by the operator as $NAME."
`

// powershellBootstrap returns the PowerShell equivalent for Windows far ends.
func powershellBootstrap(baseURL string, inv *Invite) string {
	r := strings.NewReplacer(
		"@@BASE@@", baseURL,
		"@@CODE@@", inv.Code,
		"@@NAME@@", inv.Name,
		"@@SHAPE@@", string(inv.Shape),
		"@@POLLS@@", strconv.Itoa(pollsUntil(inv)),
	)
	return r.Replace(powershellBootstrapTmpl)
}

const powershellBootstrapTmpl = `# bastionhub enrollment (Windows). Generates a keypair, sends the PUBLIC half
# for signing, waits for the certificate. The private key never leaves this box.
$ErrorActionPreference = "Stop"

$Base  = "@@BASE@@"
$Code  = "@@CODE@@"
$Name  = "@@NAME@@"
$Shape = "@@SHAPE@@"

if (-not (Get-Command ssh-keygen -ErrorAction SilentlyContinue)) {
    Write-Error "ssh-keygen not found. Enable the OpenSSH Client optional feature."
    exit 1
}

if ($Shape -eq "device") { $KeyDir = "$env:USERPROFILE\.bastionhub-tunnel" }
else { $KeyDir = Join-Path $env:TEMP ("bastionhub-" + [guid]::NewGuid().ToString("N").Substring(0,8)) }
New-Item -ItemType Directory -Force -Path $KeyDir | Out-Null
$Key = Join-Path $KeyDir "id_ed25519"

if (-not (Test-Path $Key)) {
    Write-Host "Generating a keypair on this machine..."
    ssh-keygen -t ed25519 -N '""' -C "bastionhub-$Name" -f $Key | Out-Null
}

Write-Host "Key fingerprint (read this to the operator if they ask):"
ssh-keygen -lf "$Key.pub" | ForEach-Object { "    $_" }

Write-Host "Sending the public key..."
$pub = Get-Content "$Key.pub" -Raw
Invoke-RestMethod -Method Post -Uri "$Base/e/$Code/pubkey" -Body $pub | Out-Null

Write-Host "Waiting for the operator to sign (Ctrl-C to give up)..."
$resp = $null
for ($i = 0; $i -lt @@POLLS@@; $i++) {
    try {
        $r = Invoke-WebRequest -Uri "$Base/e/$Code/cert" -UseBasicParsing
        if ($r.StatusCode -eq 200) { $resp = $r.Content | ConvertFrom-Json; break }
    } catch { }
    Start-Sleep -Seconds 2
}
if ($null -eq $resp) { Write-Error "Timed out waiting for a certificate"; exit 1 }

Set-Content -Path "$Key-cert.pub" -Value $resp.cert -NoNewline
Write-Host "Certificate received."

$sshArgs = @("-N","-T","-o","ExitOnForwardFailure=yes","-o","ServerAliveInterval=30",
             "-o","ServerAliveCountMax=3","-o","StrictHostKeyChecking=accept-new",
             "-i",$Key,"-R","$($resp.port):localhost:22","gw-tunnel@$($resp.bastion)")

if ($Shape -ne "device") {
    Write-Host "Connecting. Close this window to disconnect."
    # finally{} is this script's equivalent of the sh EXIT trap: the key must not
    # outlive the session, including when the operator hits Ctrl-C.
    try { & ssh @sshArgs }
    finally {
        Remove-Item -Recurse -Force $KeyDir -ErrorAction SilentlyContinue
        Write-Host "Disconnected. Key material removed."
    }
    exit 0
}

if ($Shape -eq "access") {
    # Reach the fleet rather than be reached by it: no tunnel, just a cert and
    # an ssh config block.
    $conf = "$env:USERPROFILE\.ssh\config"
    New-Item -ItemType Directory -Force -Path "$env:USERPROFILE\.ssh" | Out-Null
    if (Test-Path $conf) {
        # Drop any block we wrote before so re-running stays idempotent.
        $lines = Get-Content $conf; $out = @(); $skip = $false
        foreach ($l in $lines) {
            if ($l -match '^Host bastionhub\s*$') { $skip = $true; continue }
            if ($skip -and $l -match '^Host ') { $skip = $false }
            if (-not $skip) { $out += $l }
        }
        Set-Content -Path $conf -Value $out
    }
    Add-Content -Path $conf -Value @"
Host bastionhub
    HostName $($resp.bastion)
    User gw-user
    IdentityFile $Key
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
"@
    Write-Host ""
    Write-Host "Done. This machine can now reach the fleet through the bastion."
    Write-Host "  Test it:        ssh bastionhub true"
    Write-Host "  Reach a device: ssh -J bastionhub <user>@localhost -p <port>"
    exit 0
}

# A device that stays: register a scheduled task that runs at startup.
$action  = New-ScheduledTaskAction -Execute "ssh.exe" -Argument ($sshArgs -join " ")
$trigger = New-ScheduledTaskTrigger -AtStartup
$set     = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName "bastionhub-tunnel" -Action $action -Trigger $trigger -Settings $set -Force | Out-Null
Start-ScheduledTask -TaskName "bastionhub-tunnel"
Write-Host "Tunnel installed as a scheduled task and started."
Write-Host "Done. This machine is reachable by the operator as $Name."
`

// shellQuote wraps s in single quotes for safe interpolation into /bin/sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// -----------------------------------------------------------------------------
// `bastionhub serve` — runs on the bastion VPS
// -----------------------------------------------------------------------------

func serveStatePath() string {
	if p := os.Getenv("BASTIONHUB_SERVE_STATE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "bastionhub", "invites.json")
}

func adminTokenPath() string {
	if p := os.Getenv("BASTIONHUB_ADMIN_TOKEN_FILE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "bastionhub", "admin-token")
}

// loadOrCreateAdminToken reads the admin token, minting one on first run.
func loadOrCreateAdminToken(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, false, nil
		}
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	tok, err := newAdminToken()
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return tok, true, nil
}

func serveCmd(ctx context.Context, cmd *cli.Command) error {
	bastion := cmd.String("bastion")
	if bastion == "" {
		return cli.Exit("--bastion is required: the public hostname far-end devices dial for SSH (e.g. bastion.example.io)", 1)
	}
	listen := cmd.String("listen")
	baseURL := cmd.String("base-url")
	if baseURL == "" {
		baseURL = "https://" + bastion
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	token, fresh, err := loadOrCreateAdminToken(adminTokenPath())
	if err != nil {
		return cli.Exit(fmt.Sprintf("admin token: %v", err), 1)
	}
	store, err := newInviteStore(serveStatePath())
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}

	srv := &server{store: store, adminToken: token, bastion: bastion, baseURL: baseURL}

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("bastionhub serve %s\n", version)
	fmt.Printf("  listening:  %s\n", listen)
	fmt.Printf("  public URL: %s\n", baseURL)
	fmt.Printf("  bastion:    %s (what far ends dial for SSH)\n", bastion)
	fmt.Printf("  state:      %s\n", serveStatePath())
	fmt.Println()
	if fresh {
		fmt.Println("A new admin token was generated. Put it on the operator's laptop:")
		fmt.Printf("\n    export BASTIONHUB_ADMIN_TOKEN=%s\n\n", token)
		fmt.Printf("It is stored at %s (0600). This is the last time it is printed in full.\n\n", adminTokenPath())
	} else {
		fmt.Printf("Admin token loaded from %s (%s…).\n\n", adminTokenPath(), token[:8])
	}
	fmt.Println("This service holds no CA and signs nothing. Signing stays on the operator's machine.")

	certFile, keyFile := cmd.String("tls-cert"), cmd.String("tls-key")
	if (certFile == "") != (keyFile == "") {
		return cli.Exit("--tls-cert and --tls-key must be given together", 1)
	}

	errc := make(chan error, 1)
	go func() {
		if certFile != "" {
			errc <- httpSrv.ListenAndServeTLS(certFile, keyFile)
			return
		}
		if !isLoopback(listen) {
			fmt.Println("\nWARNING: serving plain HTTP on a non-loopback address.")
			fmt.Println("Put a TLS-terminating proxy in front, or pass --tls-cert/--tls-key.")
			fmt.Println("The bootstrap line tells strangers to pipe this URL into a shell;")
			fmt.Println("without TLS, anyone on the path can change what they run.")
		}
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return cli.Exit(err.Error(), 1)
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return nil
	}
}

// isLoopback reports whether a listen address is bound to loopback only.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// -----------------------------------------------------------------------------
// `bastionhub invite` — runs on the operator's laptop, where the CA is
// -----------------------------------------------------------------------------

// inviteClient talks to a `bastionhub serve` instance as the operator.
type inviteClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newInviteClient(baseURL, token string) *inviteClient {
	return &inviteClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *inviteClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr *strings.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s: %s", path, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func inviteCmd(ctx context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if name == "" {
		return cli.Exit("usage: bastionhub invite <name> [--shape device|session] [--port N] [--valid +52w]", 1)
	}
	serviceURL := cmd.String("url")
	if serviceURL == "" {
		serviceURL = os.Getenv("BASTIONHUB_SERVE_URL")
	}
	if serviceURL == "" {
		return cli.Exit("--url is required (or set BASTIONHUB_SERVE_URL) — the public URL of `bastionhub serve`", 1)
	}
	token := cmd.String("admin-token")
	if token == "" {
		token = os.Getenv("BASTIONHUB_ADMIN_TOKEN")
	}
	if token == "" {
		return cli.Exit("--admin-token is required (or set BASTIONHUB_ADMIN_TOKEN) — printed by `bastionhub serve` on first run", 1)
	}
	if _, err := exec.LookPath("sshca"); err != nil {
		return cli.Exit("sshca not found in PATH — this laptop holds the CA and does the signing. Install from https://github.com/roselabs-io/sshca", 1)
	}

	shape := inviteShape(cmd.String("shape"))
	switch shape {
	case shapeDevice, shapeSession, shapeAccess:
	default:
		return cli.Exit("--shape must be one of:\n"+
			"  device   a machine that stays and must be REACHABLE (controller, gateway box)\n"+
			"  session  a machine that must be reachable for one sitting, leaving nothing behind\n"+
			"  access   a machine that needs to REACH the fleet (your other laptop)", 1)
	}

	// Principal follows from shape unless overridden. Mismatching them is
	// silent and confusing — a gw-user cert authenticates fine and then cannot
	// open the tunnel the script just set up.
	principal := cmd.String("principal")
	if principal == "" {
		principal = defaultPrincipalFor(shape)
	} else if principal != defaultPrincipalFor(shape) {
		fmt.Printf("note: --shape %s normally uses principal %q; you asked for %q.\n",
			shape, defaultPrincipalFor(shape), principal)
	}
	valid := cmd.String("valid")
	if valid == "" {
		valid = defaultValidityFor(shape)
	}

	// An access invite grants a machine the ability to reach the fleet. It is
	// not an endpoint: nothing listens for it, so it takes no port and gets no
	// registry entry.
	isEndpoint := shape != shapeAccess

	cfg, err := loadConfig()
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if _, exists := cfg.Endpoints[name]; isEndpoint && exists {
		return cli.Exit(fmt.Sprintf("endpoint %q already registered — `bastionhub endpoint unregister %s` first, or pick another name", name, name), 1)
	}
	port := int(cmd.Int("port"))
	if !isEndpoint {
		port = 0
	} else if port == 0 {
		if port, err = allocatePort(cfg); err != nil {
			return cli.Exit(err.Error(), 1)
		}
	} else {
		for n, e := range cfg.Endpoints {
			if e.Port == port {
				return cli.Exit(fmt.Sprintf("port %d already used by endpoint %q", port, n), 1)
			}
		}
	}

	client := newInviteClient(serviceURL, token)

	// 1. Mint the invite.
	reqBody, _ := json.Marshal(map[string]any{
		"name": name, "port": port, "principal": principal,
		"valid": valid, "shape": string(shape), "user": cmd.String("user"),
		"ttl_seconds": int(cmd.Duration("ttl").Seconds()),
	})
	var created struct {
		Code       string    `json:"code"`
		ExpiresAt  time.Time `json:"expires_at"`
		UnixCmd    string    `json:"unix_cmd"`
		WindowsCmd string    `json:"windows_cmd"`
	}
	if err := client.do(ctx, http.MethodPost, "/api/invite", reqBody, &created); err != nil {
		return cli.Exit(fmt.Sprintf("could not create invite: %v", err), 1)
	}

	mins := int(time.Until(created.ExpiresAt).Round(time.Minute).Minutes())
	fmt.Printf("Read this to whoever is on site (expires in %d min):\n\n", mins)
	fmt.Printf("    %s      # mac / linux\n", created.UnixCmd)
	fmt.Printf("    %s     # windows\n\n", created.WindowsCmd)
	fmt.Printf("Code: %s\n\n", formatCodeForReading(created.Code))

	// 2. Wait for the far end to submit its public key.
	fmt.Print("waiting for the far end… ")
	pubkey, err := waitForPubkey(ctx, client, created.Code, created.ExpiresAt)
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	fmt.Println("✓ public key received")

	// 3. Show the operator what they are about to sign. The fingerprint is the
	//    only thing tying this key to the person on the phone; anyone who
	//    learned the code could have raced them to it.
	fp, err := fingerprintOf(pubkey)
	if err == nil {
		fmt.Printf("\n  fingerprint: %s\n", fp)
		fmt.Printf("  will sign as: principal=%s valid=%s shape=%s\n\n", principal, valid, shape)
	}
	if !cmd.Bool("yes") {
		fmt.Print("Ask them to read their fingerprint back. Sign it? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			_ = client.do(ctx, http.MethodDelete, "/api/invite/"+created.Code, nil, nil)
			return cli.Exit("aborted; invite revoked", 1)
		}
	}

	// 4. Sign locally. The CA never leaves this machine.
	certPEM, keyID, err := signPubkeyLocally(pubkey, name, principal, valid, cmd.String("ca-dir"))
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}

	// 5. Hand the certificate back through the service.
	if err := client.do(ctx, http.MethodPost, "/api/invite/"+created.Code+"/cert", []byte(certPEM), nil); err != nil {
		return cli.Exit(fmt.Sprintf("signed, but could not upload the cert: %v", err), 1)
	}
	fmt.Println("✓ certificate signed and sent")

	// 6. Register the endpoint locally.
	if isEndpoint {
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
			return cli.Exit(fmt.Sprintf("cert delivered but registry save failed: %v", err), 1)
		}
	}

	// 7. Confirm the far end actually picked it up.
	claimed := waitForClaim(ctx, client, created.Code, 60*time.Second)
	switch {
	case !isEndpoint && claimed:
		fmt.Printf("\n✓ %s can now reach the fleet\n", name)
	case !isEndpoint:
		fmt.Printf("\n✓ %s: cert waiting (they have not run the command yet)\n", name)
	case claimed:
		fmt.Printf("\n✓ %s enrolled on port %d\n", name, port)
	default:
		fmt.Printf("\n✓ %s registered on port %d (cert waiting; the far end has not fetched it yet)\n", name, port)
	}
	fmt.Printf("  key-id: %s\n", keyID)
	fmt.Printf("  verify: bastionhub status\n")
	switch shape {
	case shapeDevice:
		fmt.Printf("  reach:  bastionhub ssh %s\n", name)
	case shapeAccess:
		fmt.Printf("  they run: ssh bastionhub true\n")
	}
	return nil
}

// formatCodeForReading splits a code into two groups so it survives being read
// aloud over a bad phone line.
func formatCodeForReading(code string) string {
	if len(code) != inviteCodeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

func waitForPubkey(ctx context.Context, c *inviteClient, code string, deadline time.Time) (string, error) {
	for {
		var st struct {
			State  string `json:"state"`
			Pubkey string `json:"pubkey"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/invite/"+code, nil, &st); err != nil {
			return "", fmt.Errorf("polling: %w", err)
		}
		switch st.State {
		case "pubkey-received", "cert-ready":
			return st.Pubkey, nil
		case "expired":
			return "", errors.New("invite expired before anyone redeemed it")
		case "claimed":
			return "", errors.New("invite was already claimed")
		}
		if time.Now().After(deadline) {
			return "", errors.New("invite expired before anyone redeemed it")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func waitForClaim(ctx context.Context, c *inviteClient, code string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var st struct {
			State string `json:"state"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/invite/"+code, nil, &st); err == nil && st.State == "claimed" {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollInterval):
		}
	}
	return false
}

// signPubkeyLocally writes the pubkey to a temp file, shells out to
// `sshca cert sign`, and returns the resulting certificate.
//
// This is the same path `bastionhub endpoint enroll` already uses. The service
// is not involved: the CA, the signing, and the audit-log entry all stay here.
func signPubkeyLocally(pubkey, name, principal, valid, caDir string) (cert string, keyID string, err error) {
	dir, err := os.MkdirTemp("", "bastionhub-sign-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)

	pubPath := filepath.Join(dir, "invite.pub")
	if err := os.WriteFile(pubPath, []byte(strings.TrimSpace(pubkey)+"\n"), 0o600); err != nil {
		return "", "", err
	}
	keyID = fmt.Sprintf("%s-%s", name, time.Now().UTC().Format("20060102T1504Z"))

	args := []string{"cert", "sign", "--ca", "user", "--principal", principal,
		"--valid", valid, "--key-id", keyID}
	if caDir != "" {
		args = append(args, "--dir", caDir)
	}
	args = append(args, pubPath)

	if out, err := exec.Command("sshca", args...).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("sshca cert sign failed: %v\n%s", err, out)
	}
	certPath := filepath.Join(dir, "invite-cert.pub")
	data, err := os.ReadFile(certPath)
	if err != nil {
		return "", "", fmt.Errorf("sshca succeeded but no cert at %s: %w", certPath, err)
	}
	return strings.TrimSpace(string(data)), keyID, nil
}

// fingerprintOf returns the SHA256 fingerprint of a pubkey, via ssh-keygen so
// the format matches exactly what the far end prints.
func fingerprintOf(pubkey string) (string, error) {
	f, err := os.CreateTemp("", "bhfp-*.pub")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(strings.TrimSpace(pubkey) + "\n"); err != nil {
		return "", err
	}
	f.Close()
	out, err := exec.Command("ssh-keygen", "-lf", f.Name()).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
