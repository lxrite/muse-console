package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func testAuth(t *testing.T) *Auth {
	t.Helper()
	return NewAuth([]User{{Username: "admin", Password: "s3cret"}}, "", nil)
}

func TestSessionRoundtrip(t *testing.T) {
	a := testAuth(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	http.SetCookie(rr, a.issueSession("admin"))

	req.AddCookie(rr.Result().Cookies()[0])
	username, _, ok := a.parseSession(req)
	if !ok || username != "admin" {
		t.Fatalf("valid session rejected: ok=%v username=%q", ok, username)
	}
}

func TestSessionTampered(t *testing.T) {
	a := testAuth(t)
	rr := httptest.NewRecorder()
	http.SetCookie(rr, a.issueSession("admin"))
	c := rr.Result().Cookies()[0]

	for _, mutate := range []func(string) string{
		func(v string) string { return v[:len(v)-2] + "ff" },        // bad MAC
		func(v string) string { return "eA.eB." + v[strings.LastIndex(v, ".")+1:] }, // bad user part
		func(v string) string { return "garbage" },                   // malformed
	} {
		c.Value = mutate(c.Value)
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(c)
		if _, _, ok := a.parseSession(req); ok {
			t.Fatalf("tampered session accepted: %q", c.Value)
		}
	}
}

func TestSessionExpired(t *testing.T) {
	a := testAuth(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(a.sessionCookie("admin", time.Now().Add(-time.Hour).Unix()))
	if _, _, ok := a.parseSession(req); ok {
		t.Fatal("expired session accepted")
	}
}

func TestCheckPassword(t *testing.T) {
	a := testAuth(t)
	if !a.checkPassword("admin", "s3cret") {
		t.Fatal("correct password rejected")
	}
	if a.checkPassword("admin", "wrong") {
		t.Fatal("wrong password accepted")
	}
	if a.checkPassword("nobody", "s3cret") {
		t.Fatal("unknown user accepted")
	}
	if a.checkPassword("admin", "") {
		t.Fatal("empty password accepted")
	}
}

func TestSafeNext(t *testing.T) {
	if got := safeNext("/term"); got != "/term" {
		t.Fatalf("safeNext(/term) = %q", got)
	}
	for _, bad := range []string{"", "https://evil.example", "//evil.example/x", "/\\evil"} {
		if got := safeNext(bad); got != "/" {
			t.Fatalf("safeNext(%q) = %q, want /", bad, got)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	a := testAuth(t)
	tpl := mustParseTemplates()

	// Wrong password -> 401 with error message.
	form := strings.NewReader("username=admin&password=nope&next=%2Fterm")
	req := httptest.NewRequest("POST", "/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	rr := httptest.NewRecorder()
	a.handleLogin(tpl)(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "用户名或密码错误") {
		t.Fatal("bad login page missing error message")
	}

	// No Accept-Language -> English fallback.
	form = strings.NewReader("username=admin&password=nope&next=%2Fterm")
	req = httptest.NewRequest("POST", "/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr = httptest.NewRecorder()
	a.handleLogin(tpl)(rr, req)
	if !strings.Contains(rr.Body.String(), "Invalid username or password") {
		t.Fatal("bad login page missing English fallback error message")
	}

	// Right password -> 302 with session cookie, honoring next.
	form = strings.NewReader("username=admin&password=s3cret&next=%2Fterm")
	req = httptest.NewRequest("POST", "/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr = httptest.NewRecorder()
	a.handleLogin(tpl)(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("good login status = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/term" {
		t.Fatalf("good login Location = %q, want /term", loc)
	}
	var session *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("good login set no session cookie")
	}

	// Session cookie grants access through the middleware.
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(session)
	rr = httptest.NewRecorder()
	a.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("protected"))
	})).ServeHTTP(rr, req)
	if rr.Body.String() != "protected" {
		t.Fatal("session cookie did not pass middleware")
	}

	// No credentials on a page -> redirect to /login.
	req = httptest.NewRequest("GET", "/files", nil)
	rr = httptest.NewRecorder()
	a.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || !strings.HasPrefix(rr.Header().Get("Location"), "/login?next=") {
		t.Fatalf("unauthenticated page: status=%d location=%q", rr.Code, rr.Header().Get("Location"))
	}

	// No credentials on the API -> 401 JSON, not a redirect.
	req = httptest.NewRequest("GET", "/api/files/list?path=/", nil)
	rr = httptest.NewRecorder()
	a.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated api status = %d, want 401", rr.Code)
	}

	// Basic Auth grants access without a cookie.
	req = httptest.NewRequest("GET", "/api/files/list?path=/", nil)
	req.SetBasicAuth("admin", "s3cret")
	rr = httptest.NewRecorder()
	a.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("api-ok"))
	})).ServeHTTP(rr, req)
	if rr.Body.String() != "api-ok" {
		t.Fatal("basic auth did not pass middleware")
	}
}

func TestChangePassword(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.toml"
	content := "# comment to preserve\naddr = \"127.0.0.1:8080\"\n\n[[users]]\nusername = \"admin\"\npassword = \"oldpass\"  # keep me\n\n[[users]]\nusername = \"guest\"\npassword = \"guestpw\"\n"
	if err := os.WriteFile(cfg, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a := NewAuth([]User{
		{Username: "admin", Password: "oldpass"},
		{Username: "guest", Password: "guestpw"},
	}, cfg, nil)

	// Wrong old password -> error, nothing changes.
	if err := a.changePassword("admin", "wrong", "newpass1"); err == nil {
		t.Fatal("expected error for wrong old password")
	}
	// Too short -> error.
	if err := a.changePassword("admin", "oldpass", "12345"); err == nil {
		t.Fatal("expected error for short password")
	}
	// Happy path.
	if err := a.changePassword("admin", "oldpass", "newpass1"); err != nil {
		t.Fatalf("changePassword: %v", err)
	}
	if !a.checkPassword("admin", "newpass1") {
		t.Fatal("in-memory password not updated")
	}
	if a.checkPassword("admin", "oldpass") {
		t.Fatal("old password still works")
	}
	// Other user untouched.
	if !a.checkPassword("guest", "guestpw") {
		t.Fatal("other user's password changed unexpectedly")
	}

	// Config file: only admin's password line changed, comment preserved.
	updated, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(updated)
	if !strings.Contains(s, `password = "newpass1"  # keep me`) {
		t.Errorf("password line not updated correctly:\n%s", s)
	}
	if !strings.Contains(s, "# comment to preserve") {
		t.Errorf("comment lost:\n%s", s)
	}
	if !strings.Contains(s, `username = "guest"`) || !strings.Contains(s, `password = "guestpw"`) {
		t.Errorf("guest block damaged:\n%s", s)
	}
	// Unknown user.
	if err := updatePasswordInConfig(cfg, "nobody", "x"); err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestChangePasswordHandler(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.toml"
	os.WriteFile(cfg, []byte("[[users]]\nusername = \"admin\"\npassword = \"oldpass\"\n"), 0600)
	a := NewAuth([]User{{Username: "admin", Password: "oldpass"}}, cfg, nil)
	tpl := mustParseTemplates()
	h := a.handleChangePassword(tpl)

	// Unauthenticated -> redirect to login.
	req := httptest.NewRequest("GET", "/change-password", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("unauthenticated status = %d, want 302", rr.Code)
	}

	// Authenticated GET -> form.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/change-password", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	sess := httptest.NewRecorder()
	http.SetCookie(sess, a.issueSession("admin"))
	req.AddCookie(sess.Result().Cookies()[0])
	h(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "修改密码") {
		t.Fatalf("GET form status = %d", rr.Code)
	}

	// POST with mismatched confirmation -> error, no change.
	form := url.Values{"old_password": {"oldpass"}, "new_password": {"new1new1"}, "confirm_password": {"different"}}
	req = httptest.NewRequest("POST", "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.AddCookie(sess.Result().Cookies()[0])
	rr = httptest.NewRecorder()
	h(rr, req)
	if !strings.Contains(rr.Body.String(), "不一致") {
		t.Fatal("expected mismatch error")
	}
	if !a.checkPassword("admin", "oldpass") {
		t.Fatal("password changed despite mismatch")
	}

	// POST happy path -> redirect to login?changed=1, session cleared.
	form = url.Values{"old_password": {"oldpass"}, "new_password": {"brandnew"}, "confirm_password": {"brandnew"}}
	req = httptest.NewRequest("POST", "/change-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sess.Result().Cookies()[0])
	rr = httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "/login?changed=1") {
		t.Fatalf("POST redirect = %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if !a.checkPassword("admin", "brandnew") {
		t.Fatal("password not updated after POST")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter()
	ip := "1.2.3.4"
	if l.blocked(ip) {
		t.Fatal("fresh IP should not be blocked")
	}
	for i := 0; i < maxLoginAttempts-1; i++ {
		if l.recordFailure(ip) {
			t.Fatalf("blocked after %d failures, want block at %d", i+1, maxLoginAttempts)
		}
	}
	if l.blocked(ip) {
		t.Fatal("should not be blocked before reaching max attempts")
	}
	if !l.recordFailure(ip) {
		t.Fatal("expected block on reaching max attempts")
	}
	if !l.blocked(ip) {
		t.Fatal("expected blocked after max attempts")
	}
	// Other IP unaffected.
	if l.blocked("5.6.7.8") {
		t.Fatal("different IP should not be blocked")
	}
	// Success clears.
	l.recordSuccess(ip)
	if l.blocked(ip) {
		t.Fatal("success should clear the block")
	}
}

func TestClientIP(t *testing.T) {
	a := NewAuth([]User{{Username: "admin", Password: "s3cret"}}, "", nil)

	// Default: direct peer.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if got := a.clientIP(req); got != "127.0.0.1" {
		t.Errorf("direct peer: got %q", got)
	}
	// Untrusted header ignored by default.
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	if got := a.clientIP(req); got != "127.0.0.1" {
		t.Errorf("untrusted header should be ignored, got %q", got)
	}

	// Trusted: X-Forwarded-For list takes leftmost.
	b := NewAuth([]User{{Username: "admin", Password: "s3cret"}}, "",
		[]string{"X-Forwarded-For", "X-Real-IP"})
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.Header.Set("X-Forwarded-For", "9.9.9.9, 10.0.0.1")
	if got := b.clientIP(req2); got != "9.9.9.9" {
		t.Errorf("XFF leftmost: got %q", got)
	}
	// Falls back to next configured header.
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.RemoteAddr = "127.0.0.1:12345"
	req3.Header.Set("X-Real-IP", "8.8.8.8")
	if got := b.clientIP(req3); got != "8.8.8.8" {
		t.Errorf("X-Real-IP: got %q", got)
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := testAuth(t)
	tpl := mustParseTemplates()
	h := a.handleLogin(tpl)
	post := func(pw string) *httptest.ResponseRecorder {
		form := url.Values{"username": {"admin"}, "password": {pw}}
		req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		req.RemoteAddr = "127.0.0.1:9999"
		rr := httptest.NewRecorder()
		h(rr, req)
		return rr
	}
	// maxLoginAttempts failures -> next attempt gets 429.
	for i := 0; i < maxLoginAttempts; i++ {
		if code := post("wrong").Code; code == http.StatusTooManyRequests {
			t.Fatalf("429 too early at attempt %d", i+1)
		}
	}
	rr := post("wrong")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after %d failures, got %d", maxLoginAttempts, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "尝试次数过多") {
		t.Fatal("expected rate-limit message in body")
	}
	// Even the correct password is rejected while blocked.
	if code := post("s3cret").Code; code != http.StatusTooManyRequests {
		t.Fatalf("correct password while blocked: got %d, want 429", code)
	}
}

func TestBasicAuthRateLimit(t *testing.T) {
	a := testAuth(t)
	api := a.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("api-ok"))
	}))
	call := func(pw, ip string) int {
		req := httptest.NewRequest("GET", "/api/files/list", nil)
		req.RemoteAddr = ip + ":9999"
		req.SetBasicAuth("admin", pw)
		rr := httptest.NewRecorder()
		api.ServeHTTP(rr, req)
		return rr.Code
	}
	for i := 0; i < maxLoginAttempts; i++ {
		call("wrong", "10.9.8.7")
	}
	if code := call("wrong", "10.9.8.7"); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", code)
	}
	// A different IP is unaffected.
	if code := call("s3cret", "10.9.8.8"); code != http.StatusOK {
		t.Fatalf("different IP with correct password: got %d, want 200", code)
	}
}
