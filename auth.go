package main

// Session-based authentication, replacing the old shared-token scheme.
//
// A successful login issues an HMAC-signed session cookie:
//
//	muse-console-session = base64url(username) + "." + expiryUnix + "." + hex(HMAC_SHA256(username + "." + expiry, key))
//
// The signing key is derived from the user's password, so sessions
// survive restarts but are invalidated by a password change.
// API clients can alternatively use HTTP Basic Auth on every request.
import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName = "muse-console-session"
	sessionTTL        = 30 * 24 * time.Hour
	refreshThreshold  = 15 * 24 * time.Hour // re-issue when less than this remains
)

// Auth holds the configured users (username -> password, plaintext;
// the config file itself is the secret — keep it chmod 600).
type Auth struct {
	mu             sync.RWMutex
	users          map[string]string
	configPath     string   // config.toml location, for persisting password changes
	trustedHeaders []string // headers trusted for client-IP detection (empty = direct peer)
	limiter        *LoginLimiter
}

func NewAuth(users []User, configPath string, trustedHeaders []string) *Auth {
	a := &Auth{
		users:          make(map[string]string, len(users)),
		configPath:     configPath,
		trustedHeaders: trustedHeaders,
		limiter:        NewLoginLimiter(),
	}
	for _, u := range users {
		a.users[u.Username] = u.Password
	}
	return a
}

// Anti-brute-force rate limiting for password attempts.
//
// muse-console is typically exposed through a TCP tunnel (rathole), so all
// external clients appear to come from the tunnel endpoint's IP — the limit
// is therefore effectively global. This is intentional: it still stops
// password brute-forcing, at the cost of temporarily locking out legitimate
// users during an active attack.
const (
	maxLoginAttempts = 10               // failures before blocking
	loginWindow      = 10 * time.Minute // sliding window for counting failures
	loginBlockFor    = 30 * time.Minute // how long a block lasts
)

type loginEntry struct {
	failures     []time.Time
	blockedUntil time.Time
}

type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{entries: make(map[string]*loginEntry)}
}

// prune removes failures outside the window; returns the surviving slice.
func pruneFailures(failures []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-loginWindow)
	kept := failures[:0]
	for _, t := range failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// blocked reports whether ip is currently blocked.
func (l *LoginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		return false
	}
	now := time.Now()
	if now.Before(e.blockedUntil) {
		return true
	}
	if kept := pruneFailures(e.failures, now); len(kept) == 0 {
		delete(l.entries, ip)
	} else {
		e.failures = kept
	}
	return false
}

// recordFailure records a failed attempt, returning whether ip is now blocked.
func (l *LoginLimiter) recordFailure(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.entries[ip]
	if !ok {
		e = &loginEntry{}
		l.entries[ip] = e
	}
	e.failures = append(pruneFailures(e.failures, now), now)
	if len(e.failures) >= maxLoginAttempts {
		e.blockedUntil = now.Add(loginBlockFor)
	}
	return now.Before(e.blockedUntil)
}

// recordSuccess clears the record for ip (e.g. after a successful login).
func (l *LoginLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

// clientIP returns the best-effort client IP for rate limiting.
//
// By default this is the direct TCP peer. Behind a rathole tunnel that is
// the tunnel endpoint itself, so the limit is effectively global (see the
// LoginLimiter note). If trusted_proxy_headers is configured — only do this
// behind a trusted reverse proxy that sets them — the first configured
// header present on the request is used instead; X-Forwarded-For takes its
// leftmost (original client) entry.
func (a *Auth) clientIP(r *http.Request) string {
	for _, h := range a.trustedHeaders {
		if v := r.Header.Get(h); v != "" {
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			if ip := strings.TrimSpace(v); ip != "" {
				return ip
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// signingKey derives a per-user HMAC key from the password.
func (a *Auth) signingKey(username string) []byte {
	a.mu.RLock()
	pw := a.users[username]
	a.mu.RUnlock()
	sum := sha256.Sum256([]byte("muse-console/v1|" + pw))
	return sum[:]
}

func (a *Auth) issueSession(username string) *http.Cookie {
	expiry := time.Now().Add(sessionTTL).Unix()
	return a.sessionCookie(username, expiry)
}

func (a *Auth) sessionCookie(username string, expiry int64) *http.Cookie {
	userPart := base64.RawURLEncoding.EncodeToString([]byte(username))
	expPart := strconv.FormatInt(expiry, 10)
	mac := hmac.New(sha256.New, a.signingKey(username))
	mac.Write([]byte(userPart + "." + expPart))
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    userPart + "." + expPart + "." + hex.EncodeToString(mac.Sum(nil)),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	}
}

// parseSession verifies the cookie and returns the username and expiry.
func (a *Auth) parseSession(r *http.Request) (string, int64, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", 0, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return "", 0, false
	}
	username, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(username) == 0 {
		return "", 0, false
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Unix(expiry, 0).Before(time.Now()) {
		return "", 0, false
	}
	name := string(username)
	a.mu.RLock()
	password, ok := a.users[name]
	a.mu.RUnlock()
	if !ok || password == "" {
		return "", 0, false
	}
	mac := hmac.New(sha256.New, a.signingKey(name))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(want)) != 1 {
		return "", 0, false
	}
	return name, expiry, true
}

func (a *Auth) checkPassword(username, password string) bool {
	a.mu.RLock()
	expected, ok := a.users[username]
	a.mu.RUnlock()
	if !ok || password == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(expected)) == 1
}

// changePassword verifies the old password and sets a new one, persisting
// it to config.toml. Sessions are HMAC-signed with a key derived from the
// password, so changing it invalidates all existing sessions.
func (a *Auth) changePassword(username, oldPw, newPw string) error {
	a.mu.RLock()
	_, ok := a.users[username]
	a.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown user")
	}
	if !a.checkPassword(username, oldPw) {
		return errWrongPassword
	}
	if len(newPw) < 6 {
		return errPasswordTooShort
	}
	if err := updatePasswordInConfig(a.configPath, username, newPw); err != nil {
		return err
	}
	a.mu.Lock()
	a.users[username] = newPw
	a.mu.Unlock()
	return nil
}

var (
	errWrongPassword   = errors.New("wrong password")
	errPasswordTooShort = errors.New("password too short")
)

var (
	tomlTableRe   = regexp.MustCompile(`^\s*\[\[?([^\]]+)\]?\]\s*$`)
	tomlStringRe  = regexp.MustCompile(`^(\s*[A-Za-z0-9_-]+\s*=\s*)"((?:[^"\\]|\\.)*)"(.*)$`)
	tomlUsernameRe = regexp.MustCompile(`^\s*username\s*=\s*"((?:[^"\\]|\\.)*)"\s*(?:#.*)?$`)
)

// tomlEscape quotes a string for a TOML basic string value.
func tomlEscape(s string) string {
	r := strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(r, "\"", "\\\"")
}

// updatePasswordInConfig rewrites the password of one [[users]] entry in
// the TOML file, preserving everything else (comments, formatting).
// It writes atomically via temp file + rename, keeping mode 600.
func updatePasswordInConfig(path, username, newPassword string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	lines := strings.Split(string(data), "\n")

	// Locate the [[users]] block belonging to username.
	type block struct{ start, end int } // [start, end)
	var blocks []block
	for i, line := range lines {
		if m := tomlTableRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			if strings.TrimSpace(m[1]) == "users" {
				blocks = append(blocks, block{i, len(lines)})
				if len(blocks) > 1 {
					blocks[len(blocks)-2].end = i
				}
			} else if len(blocks) > 0 {
				blocks[len(blocks)-1].end = i
			}
		}
	}
	target := -1
	for bi, b := range blocks {
		for i := b.start; i < b.end; i++ {
			if m := tomlUsernameRe.FindStringSubmatch(lines[i]); m != nil && m[1] == username {
				target = bi
				break
			}
		}
		if target >= 0 {
			break
		}
	}
	if target < 0 {
		return fmt.Errorf("user %q not found in config", username)
	}
	b := blocks[target]
	updated := false
	for i := b.start; i < b.end; i++ {
		m := tomlStringRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		key := strings.TrimSpace(strings.SplitN(strings.TrimSpace(m[1]), "=", 2)[0])
		if key != "password" {
			continue
		}
		lines[i] = m[1] + `"` + tomlEscape(newPassword) + `"` + m[3]
		updated = true
		break
	}
	if !updated {
		return fmt.Errorf("no password field for user %q", username)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(strings.Join(lines, "\n")); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// authenticate accepts a valid session cookie or HTTP Basic Auth.
// authenticate verifies a session cookie or HTTP Basic Auth.
// Returns (username, ok, rateLimited): rateLimited is true when the client
// IP is currently blocked for too many failed password attempts.
func (a *Auth) authenticate(r *http.Request) (string, bool, bool) {
	if username, _, ok := a.parseSession(r); ok {
		return username, true, false
	}
	if username, password, ok := r.BasicAuth(); ok {
		ip := a.clientIP(r)
		if a.limiter.blocked(ip) {
			return "", false, true
		}
		if a.checkPassword(username, password) {
			a.limiter.recordSuccess(ip)
			return username, true, false
		}
		a.limiter.recordFailure(ip)
	}
	return "", false, false
}

// maybeRefresh slides the session expiry when it is past half-life.
func (a *Auth) maybeRefresh(w http.ResponseWriter, r *http.Request) {
	username, expiry, ok := a.parseSession(r)
	if !ok {
		return
	}
	// Only refresh cookie-based logins, not Basic Auth requests.
	if _, err := r.Cookie(sessionCookieName); err != nil {
		return
	}
	if time.Until(time.Unix(expiry, 0)) < refreshThreshold {
		http.SetCookie(w, a.issueSession(username))
	}
}

func (a *Auth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, ok, limited := a.authenticate(r)
		if ok {
			_ = username
			a.maybeRefresh(w, r)
			next.ServeHTTP(w, r)
			return
		}
		if limited {
			msg := "too many login attempts, try again later"
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]string{"error": msg})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(fmt.Sprintf(T(langOf(r), "login.err_limit"), int(loginBlockFor.Minutes()))))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

type loginData struct {
	Lang    string
	Next    string
	Error   string
	Changed bool
}

func safeNext(next string) string {
	// Only allow same-origin paths; reject protocol-relative URLs and
	// backslashes (some browsers normalize "\" to "/").
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") &&
		!strings.Contains(next, "\\") {
		return next
	}
	return "/"
}

func (a *Auth) handleLogin(tpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := langOf(r)
		switch r.Method {
		case http.MethodGet:
			renderTemplate(w, tpl, "login.html", loginData{
				Lang:    lang,
				Next:    r.URL.Query().Get("next"),
				Changed: r.URL.Query().Get("changed") == "1",
			})
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				renderTemplate(w, tpl, "login.html", loginData{Lang: lang, Error: T(lang, "form.err_invalid")})
				return
			}
			username := r.Form.Get("username")
			next := safeNext(r.Form.Get("next"))
			ip := a.clientIP(r)
			if a.limiter.blocked(ip) {
				w.WriteHeader(http.StatusTooManyRequests)
				renderTemplate(w, tpl, "login.html", loginData{Next: next, Lang: lang,
					Error: fmt.Sprintf(T(lang, "login.err_limit"), int(loginBlockFor.Minutes()))})
				return
			}
			if !a.checkPassword(username, r.Form.Get("password")) {
				a.limiter.recordFailure(ip)
				w.WriteHeader(http.StatusUnauthorized)
				renderTemplate(w, tpl, "login.html", loginData{Next: next, Lang: lang, Error: T(lang, "login.err_bad")})
				return
			}
			a.limiter.recordSuccess(ip)
			http.SetCookie(w, a.issueSession(username))
			http.Redirect(w, r, next, http.StatusFound)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

type changePwData struct {
	Lang    string
	Error   string
	Changed bool
}

// handleChangePassword serves the password-change form. The user must be
// authenticated (via the middleware); after a successful change the session
// is cleared and the user re-logs in, since the session signing key is
// derived from the password.
func (a *Auth) handleChangePassword(tpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lang := langOf(r)
		username, ok, _ := a.authenticate(r)
		if !ok {
			http.Redirect(w, r, "/login?next="+url.QueryEscape("/change-password"), http.StatusFound)
			return
		}
		// Only cookie sessions can change passwords, not Basic Auth callers.
		if _, err := r.Cookie(sessionCookieName); err != nil {
			http.Error(w, T(lang, "changepw.err_cookie"), http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			renderTemplate(w, tpl, "change_password.html", changePwData{Lang: lang})
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				renderTemplate(w, tpl, "change_password.html", changePwData{Lang: lang, Error: T(lang, "form.err_invalid")})
				return
			}
			newPw := r.Form.Get("new_password")
			if newPw != r.Form.Get("confirm_password") {
				renderTemplate(w, tpl, "change_password.html", changePwData{Lang: lang, Error: T(lang, "changepw.err_mismatch")})
				return
			}
			if err := a.changePassword(username, r.Form.Get("old_password"), newPw); err != nil {
				msg := err.Error()
				switch {
				case errors.Is(err, errWrongPassword):
					msg = T(lang, "changepw.err_wrongold")
				case errors.Is(err, errPasswordTooShort):
					msg = T(lang, "changepw.err_short")
				}
				renderTemplate(w, tpl, "change_password.html", changePwData{Lang: lang, Error: msg})
				return
			}
			// Password changed: drop the session, ask to log in again.
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
			http.Redirect(w, r, "/login?changed=1", http.StatusFound)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
