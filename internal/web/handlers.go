// handlers.go — routes, the Host / cookie / CSRF middleware, the cerr-kind →
// HTTP status mapping, and every mutating endpoint (accounts, sessions,
// settings, auto-switch).
//
// Implements DESIGN A25 "Security model" and "API". Method+pattern routing is
// Go 1.22 net/http (`POST /api/switch/{id}`); every mutation broadcasts a
// fresh state to SSE subscribers when it returns, success or not, so the UI
// converges on what the store actually holds. Request bodies are JSON,
// capped at maxBody bytes; the add-token body is input only and is never
// echoed or logged.
package web

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/brand"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/settings"
)

// maxBody bounds a JSON request body.
const maxBody = 64 << 10

// routes wires the mux and wraps it in the Host check.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: embedded static dir missing: " + err.Error())
	}
	// Static assets need the session cookie (not the CSRF header: <script>
	// and <link> cannot send one). Unauthenticated, they would let any page
	// fingerprint the dashboard's port by loading /static/app.js.
	mux.Handle("GET /static/", s.requireCookie(http.StripPrefix("/static/", noDirListing(http.FileServerFS(sub)))))
	mux.Handle("GET /static/accent.css", s.requireCookie(http.HandlerFunc(s.handleAccent)))
	mux.HandleFunc("GET /{$}", s.handleIndex)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/state", s.handleState)
	api.HandleFunc("GET /api/events", s.handleEvents)

	// accounts
	api.HandleFunc("POST /api/switch", s.handleSwitchStrategy)
	api.HandleFunc("POST /api/switch/{id}", s.handleSwitch)
	api.HandleFunc("POST /api/accounts/add", s.handleAddCurrent)
	api.HandleFunc("POST /api/accounts/add-token", s.handleAddToken)
	api.HandleFunc("POST /api/accounts/swap", s.handleSwap)
	api.HandleFunc("POST /api/accounts/{id}/disable", s.handleDisable(true))
	api.HandleFunc("POST /api/accounts/{id}/enable", s.handleDisable(false))
	api.HandleFunc("POST /api/accounts/{id}/remove", s.handleRemove)
	api.HandleFunc("POST /api/accounts/{id}/alias", s.handleAlias)
	api.HandleFunc("POST /api/accounts/{id}/move", s.handleMove)

	// sessions
	api.HandleFunc("POST /api/sessions/{pid}/stop", s.handleStop)

	// settings
	api.HandleFunc("GET /api/settings", s.handleSettingsList)
	api.HandleFunc("POST /api/settings/{key}", s.handleSettingSet)
	api.HandleFunc("DELETE /api/settings/{key}", s.handleSettingUnset)
	api.HandleFunc("POST /api/settings/{key}/unset", s.handleSettingUnset)

	// auto-switch
	api.HandleFunc("POST /api/auto/start", s.handleAutoStart)
	api.HandleFunc("POST /api/auto/stop", s.handleAutoSimple("stop"))
	api.HandleFunc("POST /api/auto/wake", s.handleAutoSimple("wake"))
	api.HandleFunc("POST /api/auto/threshold", s.handleAutoThreshold)
	api.HandleFunc("POST /api/auto/model", s.handleAutoModel)

	mux.Handle("/api/", s.requireAuth(api))
	return securityHeaders(s.checkHost(mux)) // headers on every response, the 421 included
}

// -- middleware ---------------------------------------------------------------

// checkHost answers 421 unless Host is 127.0.0.1:<port> or localhost:<port>
// with a numeric port. Before Start (port 0, handler mounted elsewhere) any
// port on those two names passes.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "misdirected request: unexpected Host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(host string) bool {
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	if h != "127.0.0.1" && h != "localhost" {
		return false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return false
	}
	port := s.Port()
	if port == 0 {
		return true
	}
	return n == port
}

// securityHeaders sets a strict CSP (the UI is self-hosted, no inline script
// or style) and the usual hardening headers on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// noDirListing hides directory indexes of the embedded tree.
func noDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hasCookie reports whether the request carries the session cookie
// (constant-time compare). The cookie value is NOT the CSRF token: browsers
// send a 127.0.0.1 cookie to every port on 127.0.0.1, so a cookie alone must
// never be enough to read or drive the dashboard.
func (s *Server) hasCookie(r *http.Request) bool {
	c, err := r.Cookie(s.cookieName())
	if err != nil {
		return false
	}
	return tokenEqual(c.Value, s.cookie)
}

// hasCSRF reports whether the request proves it came from the dashboard
// page: the token from the page's <meta>, in the X-CSRF-Token header or — for
// EventSource, which cannot set headers, and ONLY on its route — in the csrf
// query parameter. A token in a URL can leak (history, logs), so no other
// route accepts it there.
func (s *Server) hasCSRF(r *http.Request) bool {
	if tokenEqual(r.Header.Get(csrfHeader), s.token) {
		return true
	}
	return r.Method == http.MethodGet && r.URL.Path == "/api/events" && tokenEqual(r.URL.Query().Get("csrf"), s.token)
}

// requireCookie is the static-asset gate: cookie only (<script> and <link>
// cannot send a header).
func (s *Server) requireCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasCookie(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized: missing or invalid session cookie")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tokenEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// requireAuth enforces BOTH factors on every /api route — the session cookie
// and the page's CSRF token (reads included, so a cookie leaked to another
// loopback port cannot even read the state) — and, for non-safe methods, the
// Origin / Sec-Fetch-Site same-origin rules on top.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasCookie(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized: missing or invalid session cookie")
			return
		}
		if !s.hasCSRF(r) {
			writeError(w, http.StatusForbidden, "forbidden: missing or invalid CSRF token")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && !strings.EqualFold(o, "http://"+r.Host) {
				writeError(w, http.StatusForbidden, "forbidden: cross-origin request")
				return
			}
			if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
				writeError(w, http.StatusForbidden, "forbidden: cross-site request")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// -- page + state -------------------------------------------------------------

// handleIndex bootstraps the session from ?token= (cookie + 303) and serves
// the templated index.html to a cookie-bearing visitor. Anyone else gets a
// 401 page: the page embeds the CSRF token, so it is as guarded as the API.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("token"); t != "" {
		if !s.consumeLaunch(t) {
			http.Error(w, "forbidden: invalid or already used token — restart "+brand.Sanitized().Name+" web for a fresh URL", http.StatusForbidden)
			return
		}
		// Lax, not Strict: the launcher opens the token URL from a file://
		// redirect page (DESIGN A25), and browsers treat the whole navigation
		// — including this 303 — as cross-site, so a Strict cookie would be
		// withheld on the very next request and the visitor would see 401.
		// Lax still keeps the cookie off cross-site POSTs; every mutation is
		// additionally gated by the CSRF header and the Origin check.
		http.SetCookie(w, &http.Cookie{
			Name:     s.cookieName(),
			Value:    s.cookie,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.hasCookie(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		b := brand.Sanitized()
		_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>" + b.DisplayName + "</title><p>Unauthorized. Open the URL printed by <code>" + b.Name + " web</code> (it carries a one-time token).</p>"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	b := brand.Sanitized()
	if err := s.index.Execute(w, map[string]string{"CSRF": s.token, "Name": b.Name, "DisplayName": b.DisplayName, "Accent": b.AccentColor}); err != nil {
		s.d.Logger("web: index: " + err.Error())
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	o := stateOpts{tokenStatus: isTruthy(r.URL.Query().Get("tokenStatus"))}
	body, err := s.stateJSON(o)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// isTruthy reads a query flag: 1/true/yes/on.
func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// -- plumbing -----------------------------------------------------------------

// mutate serialises a façade mutation, maps its error and broadcasts the
// resulting state either way. The result payload is returned as
// {"ok":true,"result":<payload>}.
func (s *Server) mutate(w http.ResponseWriter, fn func() (map[string]any, error)) {
	s.mutMu.Lock()
	payload, err := fn()
	s.mutMu.Unlock()
	s.broadcast()
	if err != nil {
		s.d.Logger("web: " + err.Error())
		writeError(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": payload})
}

// decodeBody reads an optional JSON object body into v. An empty body is
// accepted (v stays zero); malformed JSON or an oversized body is a 400 and
// false is returned after the error has been written.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read body")
		return false
	}
	if len(raw) > maxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// unavailable answers 503 for a nil optional façade and reports whether it did.
func unavailable(w http.ResponseWriter, present bool, what string) bool {
	if present {
		return false
	}
	writeError(w, http.StatusServiceUnavailable, what+" not configured")
	return true
}

// -- accounts -----------------------------------------------------------------

// claudeKey resolves an account key from the API ("claude:2", "claude:work",
// "claude:me@example.com"; AccountSnapshot.Key for a row) to the Claude
// account reference the façade takes. Slot numbers are per provider, so a
// bare reference is refused rather than guessed, and a key for another
// provider never reaches the Claude façade: this dashboard drives Claude
// accounts only.
func claudeKey(raw string) (string, error) {
	provider, ref, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || provider == "" || strings.TrimSpace(ref) == "" {
		return "", cerr.Validation("address the account by its key, e.g. claude:2 (got %q)", raw)
	}
	if !strings.EqualFold(provider, reporting.ProviderClaude) {
		return "", cerr.AccountNotFound("no %s account operations in this dashboard: %s", provider, raw)
	}
	return strings.TrimSpace(ref), nil
}

// pathKey is claudeKey over the {id} path segment; false means the error was
// written.
func pathKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := claudeKey(r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return "", false
	}
	return id, true
}

type switchBody struct {
	Strategy string   `json:"strategy"`
	Models   []string `json:"models"`
}

// handleSwitchStrategy is the bare `tycswap switch --strategy …`.
func (s *Server) handleSwitchStrategy(w http.ResponseWriter, r *http.Request) {
	var b switchBody
	if !decodeBody(w, r, &b) {
		return
	}
	if b.Strategy == "" {
		writeError(w, http.StatusBadRequest, "strategy is required")
		return
	}
	valid := false
	for _, st := range Strategies {
		if st == b.Strategy {
			valid = true
		}
	}
	if !valid {
		writeError(w, http.StatusBadRequest, "unknown strategy "+strconv.Quote(b.Strategy))
		return
	}
	strategy := b.Strategy
	s.mutate(w, func() (map[string]any, error) { return s.d.Facade.Switch(&strategy, true, b.Models, nil) })
}

// handleSwitch switches to one account; ?force=1 skips the backup via
// AccountOps.SwitchToForce.
func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathKey(w, r)
	if !ok {
		return
	}
	if isTruthy(r.URL.Query().Get("force")) {
		if unavailable(w, s.d.Accounts != nil, "account operations") {
			return
		}
		s.mutate(w, func() (map[string]any, error) { return s.d.Accounts.SwitchToForce(id, true, true) })
		return
	}
	s.mutate(w, func() (map[string]any, error) { return s.d.Facade.SwitchTo(id, true) })
}

func (s *Server) handleDisable(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathKey(w, r)
		if !ok {
			return
		}
		s.mutate(w, func() (map[string]any, error) {
			return nil, s.d.Facade.SetAccountDisabled(id, disabled)
		})
	}
}

func (s *Server) handleAddCurrent(w http.ResponseWriter, r *http.Request) {
	s.mutate(w, func() (map[string]any, error) { return nil, s.d.Facade.AddAccount(nil, true, nil) })
}

type addTokenBody struct {
	Token string `json:"token"`
	Email string `json:"email"`
	Slot  string `json:"slot"`
	Alias string `json:"alias"`
}

// handleAddToken registers a setup-token / API key. The token is input only:
// it is never echoed in the response, the state, or the log.
func (s *Server) handleAddToken(w http.ResponseWriter, r *http.Request) {
	var b addTokenBody
	if !decodeBody(w, r, &b) {
		return
	}
	b.Token = strings.TrimSpace(b.Token)
	if b.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	// "-" tells the CLI to read the token from stdin. Here stdin is the
	// terminal running `web`: the request would block every mutation until
	// someone typed a line there, and that line would become the token.
	if b.Token == "-" {
		writeError(w, http.StatusBadRequest, "token must be the token itself, not \"-\"")
		return
	}
	if b.Alias != "" && unavailable(w, s.d.Accounts != nil, "account operations") {
		return
	}
	var email, slot *string
	if b.Email != "" {
		email = &b.Email
	}
	if b.Slot != "" {
		slot = &b.Slot
	}
	s.mutate(w, func() (map[string]any, error) {
		if err := s.d.Facade.AddAccountFromToken(b.Token, email, slot, true); err != nil {
			return nil, err
		}
		res := map[string]any{"added": true}
		if b.Alias == "" {
			return res, nil
		}
		id := b.Slot
		if id == "" {
			id = s.findNumberByEmail(b.Email)
		}
		if id == "" {
			return res, cerr.AccountNotFound("account added, but no slot was found to alias (give a slot or email)")
		}
		num, normalized, err := s.d.Accounts.SetAlias(id, b.Alias)
		if err != nil {
			return res, fmt.Errorf("account added, but alias not set: %w", err)
		}
		res["number"], res["alias"] = num, normalized
		return res, nil
	})
}

// findNumberByEmail looks the freshly added account up in a store-only
// snapshot (empty fetch set: no network).
func (s *Server) findNumberByEmail(email string) string {
	if email == "" {
		return ""
	}
	snap := s.d.Facade.AccountsSnapshot(map[string]bool{})
	if snap == nil {
		return ""
	}
	for _, a := range snap.Accounts {
		if strings.EqualFold(a.Email, email) {
			return a.Number
		}
	}
	return ""
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathKey(w, r)
	if !ok {
		return
	}
	s.mutate(w, func() (map[string]any, error) { return nil, s.d.Facade.RemoveAccount(id, true) })
}

func (s *Server) handleAlias(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Accounts != nil, "account operations") {
		return
	}
	var b struct {
		Alias string `json:"alias"`
	}
	id, ok := pathKey(w, r)
	if !ok {
		return
	}
	if !decodeBody(w, r, &b) {
		return
	}
	alias := strings.TrimSpace(b.Alias)
	s.mutate(w, func() (map[string]any, error) {
		if alias == "" {
			num, err := s.d.Accounts.UnsetAlias(id)
			if err != nil {
				return nil, err
			}
			return map[string]any{"number": num, "alias": ""}, nil
		}
		num, normalized, err := s.d.Accounts.SetAlias(id, alias)
		if err != nil {
			return nil, err
		}
		return map[string]any{"number": num, "alias": normalized}, nil
	})
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Accounts != nil, "account operations") {
		return
	}
	var b struct {
		Slot string `json:"slot"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.Slot) == "" {
		writeError(w, http.StatusBadRequest, "slot is required")
		return
	}
	id, ok := pathKey(w, r)
	if !ok {
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		src, tgt, swapped, err := s.d.Accounts.MoveAccount(id, b.Slot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": src, "to": tgt, "swapped": swapped}, nil
	})
}

func (s *Server) handleSwap(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Accounts != nil, "account operations") {
		return
	}
	var b struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.A) == "" || strings.TrimSpace(b.B) == "" {
		writeError(w, http.StatusBadRequest, "a and b are required")
		return
	}
	first, err := claudeKey(b.A)
	if err == nil {
		var second string
		if second, err = claudeKey(b.B); err == nil {
			b.A, b.B = first, second
		}
	}
	if err != nil {
		writeError(w, statusFor(err), err.Error())
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		na, nb, err := s.d.Accounts.SwapAccounts(b.A, b.B)
		if err != nil {
			return nil, err
		}
		return map[string]any{"a": na, "b": nb}, nil
	})
}

// -- sessions -----------------------------------------------------------------

// handleStop stops a PID (SIGTERM; TerminateProcess on Windows, where there
// is no SIGTERM) only when procdetect currently lists it as a
// Claude Code session (A25: never an arbitrary PID).
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 1 {
		writeError(w, http.StatusBadRequest, "invalid pid")
		return
	}
	listed := false
	for _, c := range s.d.Sessions().Claude {
		if c.PID == pid {
			listed = true
			break
		}
	}
	if !listed {
		writeError(w, http.StatusNotFound, "no running Claude Code session with pid "+strconv.Itoa(pid))
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		// Re-check under the lock: the listing above and the signal below
		// must see the same process (a session that exited meanwhile could
		// have had its PID reused).
		still := false
		for _, c := range s.d.Sessions().Claude {
			if c.PID == pid {
				still = true
				break
			}
		}
		if !still {
			return nil, cerr.AccountNotFound("session %d is no longer running", pid)
		}
		if err := s.d.Kill(pid); err != nil {
			return nil, err
		}
		return map[string]any{"pid": pid, "signal": stopSignalName}, nil
	})
}

// -- settings -----------------------------------------------------------------

func (s *Server) handleSettingsList(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Settings != nil, "settings") {
		return
	}
	list := s.d.Settings.Effective()
	if list == nil {
		list = []SettingView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": list})
}

// handleSettingSet accepts {"value": <string|number|bool>}; non-strings are
// rendered with fmt so a JS client may send the natural JSON type.
func (s *Server) handleSettingSet(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Settings != nil, "settings") {
		return
	}
	var b struct {
		Value any `json:"value"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if b.Value == nil {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}
	raw := valueString(b.Value)
	key := r.PathValue("key")
	s.mutate(w, func() (map[string]any, error) {
		v, err := s.d.Settings.Set(key, raw)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "value": v}, nil
	})
}

// valueString renders a JSON scalar the way a CLI user would type it.
func valueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

func (s *Server) handleSettingUnset(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Settings != nil, "settings") {
		return
	}
	key := r.PathValue("key")
	s.mutate(w, func() (map[string]any, error) {
		removed, err := s.d.Settings.Unset(key)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": key, "removed": removed}, nil
	})
}

// -- auto-switch --------------------------------------------------------------

func (s *Server) handleAutoStart(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Auto != nil, "auto-switch") {
		return
	}
	var b struct {
		DryRun bool `json:"dryRun"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		if err := s.d.Auto.Start(b.DryRun); err != nil {
			return nil, err
		}
		return map[string]any{"running": true, "dryRun": b.DryRun}, nil
	})
}

func (s *Server) handleAutoSimple(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if unavailable(w, s.d.Auto != nil, "auto-switch") {
			return
		}
		s.mutate(w, func() (map[string]any, error) {
			var err error
			if action == "stop" {
				err = s.d.Auto.Stop()
			} else {
				err = s.d.Auto.Wake()
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"action": action}, nil
		})
	}
}

// handleAutoThreshold applies a session threshold. The value must lie in the
// range the autoswitch.threshold setting allows (50–99.9), the same bounds
// the CLI and the TUI's +/- keys enforce: below 50 the engine would treat
// every account as over the limit.
func (s *Server) handleAutoThreshold(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Auto != nil, "auto-switch") {
		return
	}
	var b struct {
		Threshold *float64 `json:"threshold"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if b.Threshold == nil {
		writeError(w, http.StatusBadRequest, "threshold is required")
		return
	}
	t := *b.Threshold
	if err := checkThreshold(t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mutate(w, func() (map[string]any, error) {
		if err := s.d.Auto.ApplyThreshold(t); err != nil {
			return nil, err
		}
		return map[string]any{"threshold": t}, nil
	})
}

// handleAutoModel retargets the running engine's per-model windows: "all", a
// comma-separated list of window names, or "" for 5h + 7d only. The page saves
// autoswitch.model and then calls this, so the running engine's headroom,
// at-limit verdict and ranking follow at once.
func (s *Server) handleAutoModel(w http.ResponseWriter, r *http.Request) {
	if unavailable(w, s.d.Auto != nil, "auto-switch") {
		return
	}
	var b struct {
		Model *string `json:"model"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if b.Model == nil {
		writeError(w, http.StatusBadRequest, "model is required (\"\" counts 5h and 7d only)")
		return
	}
	model := strings.TrimSpace(*b.Model)
	s.mutate(w, func() (map[string]any, error) {
		if err := s.d.Auto.ApplyModels(model); err != nil {
			return nil, err
		}
		return map[string]any{"model": model}, nil
	})
}

// checkThreshold validates a live threshold against the autoswitch.threshold
// spec's bounds.
func checkThreshold(t float64) error {
	spec, err := settings.SpecFor("autoswitch.threshold")
	if err != nil {
		return err
	}
	if t != t || t < spec.Lo || t > spec.Hi {
		return cerr.Validation("threshold must be between %g and %g", spec.Lo, spec.Hi)
	}
	return nil
}

// -- errors -------------------------------------------------------------------

// statusFor maps a cerr kind to the HTTP status (A25 / DESIGN §3.1).
func statusFor(err error) int {
	var e *cerr.Error
	if !errors.As(err, &e) {
		return http.StatusInternalServerError
	}
	switch e.Kind {
	case cerr.KindAccountNotFound:
		return http.StatusNotFound
	case cerr.KindValidation:
		return http.StatusBadRequest
	case cerr.KindLock:
		return http.StatusConflict
	case cerr.KindClaudeCodeLockTimeout:
		return http.StatusLocked
	case cerr.KindConfig:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
