package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBridgeBrowserAuthLogin(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(http.StatusOK) })
	handler := srv.bridgeBrowserAuth(next)

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, bridgeLoginPath+"?client_id=x&state=y", nil))
	if get.Code != http.StatusOK || called != 0 {
		t.Fatalf("GET status=%d called=%d", get.Code, called)
	}
	if get.Header().Get("WWW-Authenticate") != "" || !strings.Contains(get.Body.String(), `type="password"`) {
		t.Fatal("browser login must render an HTML form without an HTTP authentication challenge")
	}
	csrf := cookieNamed(t, get, "webcodex_bridge_csrf")
	if !csrf.HttpOnly || !csrf.Secure || csrf.SameSite != http.SameSiteLaxMode || csrf.Path != bridgeLoginPath {
		t.Fatalf("csrf cookie flags: %+v", csrf)
	}
	for name, want := range map[string]string{"Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY", "Content-Security-Policy": "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"} {
		if got := get.Header().Get(name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}

	for _, tc := range []struct{ name, origin, cookie, token string }{
		{"missing csrf", srv.publicURL, "", ""},
		{"wrong csrf", srv.publicURL, csrf.Value, "wrong"},
		{"cross origin", "https://attacker.example", csrf.Value, csrf.Value},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"username": {srv.adminUser}, "password": {srv.adminPassword}, "login_csrf": {tc.token}}
			req := httptest.NewRequest(http.MethodPost, bridgeLoginPath, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", tc.origin)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "webcodex_bridge_csrf", Value: tc.cookie})
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || called != 0 {
				t.Fatalf("status=%d called=%d", rec.Code, called)
			}
		})
	}

	// Invalid credentials redisplay the form and never reflect the submitted password.
	badPass := "submitted-secret-must-not-leak"
	form := url.Values{"username": {srv.adminUser}, "password": {badPass}, "login_csrf": {csrf.Value}}
	req := httptest.NewRequest(http.MethodPost, bridgeLoginPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.publicURL)
	req.AddCookie(&http.Cookie{Name: "webcodex_bridge_csrf", Value: csrf.Value})
	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, req)
	if bad.Code != http.StatusOK || called != 0 || strings.Contains(bad.Body.String(), badPass) {
		t.Fatalf("bad login status=%d called=%d leaked=%v", bad.Code, called, strings.Contains(bad.Body.String(), badPass))
	}

	// A valid form creates a restricted session and redirects back to the same relative authorize URL.
	csrf = cookieNamed(t, bad, "webcodex_bridge_csrf")
	form = url.Values{"username": {srv.adminUser}, "password": {srv.adminPassword}, "login_csrf": {csrf.Value}}
	req = httptest.NewRequest(http.MethodPost, bridgeLoginPath+"?client_id=x&state=opaque", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.publicURL)
	req.AddCookie(&http.Cookie{Name: "webcodex_bridge_csrf", Value: csrf.Value})
	ok := httptest.NewRecorder()
	handler.ServeHTTP(ok, req)
	wantLocation := bridgeLoginPath + "?client_id=x&state=opaque"
	if ok.Code != http.StatusSeeOther || ok.Header().Get("Location") != wantLocation {
		t.Fatalf("login status=%d location=%q", ok.Code, ok.Header().Get("Location"))
	}
	session := cookieNamed(t, ok, "webcodex_bridge_login")
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode || session.Path != bridgeLoginPath {
		t.Fatalf("session cookie flags: %+v", session)
	}
	if !srv.validBridgeLoginToken("session", session.Value) || srv.validBridgeLoginToken("csrf", session.Value) {
		t.Fatal("session token purpose not restricted")
	}
	passReq := httptest.NewRequest(http.MethodGet, bridgeLoginPath, nil)
	passReq.AddCookie(session)
	pass := httptest.NewRecorder()
	handler.ServeHTTP(pass, passReq)
	if pass.Code != http.StatusOK || called != 1 {
		t.Fatalf("session status=%d called=%d", pass.Code, called)
	}
	adminReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	adminReq.AddCookie(session)
	admin := httptest.NewRecorder()
	srv.adminAuth(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })(admin, adminReq)
	if admin.Code != http.StatusUnauthorized {
		t.Fatalf("session authenticated admin: status=%d", admin.Code)
	}
}

func TestBridgeBrowserAuthBasicCompatibilityAndTokens(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	called := 0
	handler := srv.bridgeBrowserAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called++; w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, bridgeLoginPath, nil)
	req.SetBasicAuth(srv.adminUser, srv.adminPassword)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("valid Basic status=%d called=%d", rec.Code, called)
	}
	if srv.validBridgeLoginToken("session", fmt.Sprintf("%d.nonce.bad", time.Now().Add(time.Minute).Unix())) {
		t.Fatal("tampered session accepted")
	}
	if srv.validBridgeLoginToken("session", fmt.Sprintf("%d.nonce.%s", time.Now().Add(-time.Minute).Unix(), srv.bridgeLoginMAC("session", fmt.Sprintf("%d.nonce", time.Now().Add(-time.Minute).Unix())))) {
		t.Fatal("expired session accepted")
	}
	token, err := srv.newBridgeLoginToken("csrf")
	if err != nil {
		t.Fatal(err)
	}
	if srv.validBridgeLoginToken("session", token) {
		t.Fatal("csrf token accepted as session")
	}
	oldCSRF := srv.adminCSRF
	srv.adminCSRF += "-rotated"
	if srv.validBridgeLoginToken("csrf", token) {
		t.Fatal("token survived adminCSRF rotation")
	}
	srv.adminCSRF = oldCSRF
}

func TestBridgeBrowserAuthRateLimit(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	handler := srv.bridgeBrowserAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest(http.MethodPost, bridgeLoginPath, strings.NewReader(""))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		want := http.StatusForbidden
		if i == 10 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("attempt %d status=%d want=%d", i+1, rec.Code, want)
		}
	}
}

func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response missing %s cookie", name)
	return nil
}
