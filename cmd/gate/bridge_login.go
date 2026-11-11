package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const bridgeLoginPath = "/bridge/v1/authorize"

// Browser OAuth windows need an HTML login page rather than a Basic Auth challenge.
func (s *server) bridgeBrowserAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bridgeNoStore(w)
		user, pass, basic := r.BasicAuth()
		if basic && user == s.adminUser && secureCompare(pass, s.adminPassword) {
			next(w, r)
			return
		}
		if cookie, err := r.Cookie("webcodex_bridge_login"); err == nil && s.validBridgeLoginToken("session", cookie.Value) {
			next(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		errorText := ""
		if r.Method == http.MethodPost {
			if !s.allowRequest("bridge-login:"+remoteHost(r), 10, time.Minute) {
				http.Error(w, "too many login attempts", http.StatusTooManyRequests)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid login form", http.StatusBadRequest)
				return
			}
			cookie, err := r.Cookie("webcodex_bridge_csrf")
			if r.Header.Get("Origin") != s.publicURL || err != nil || !s.validBridgeLoginToken("csrf", cookie.Value) || !secureCompare(cookie.Value, r.PostForm.Get("login_csrf")) {
				http.Error(w, "login form expired or invalid; restart connection", http.StatusForbidden)
				return
			}
			if r.PostForm.Get("username") == s.adminUser && secureCompare(r.PostForm.Get("password"), s.adminPassword) {
				token, err := s.newBridgeLoginToken("session")
				if err != nil {
					http.Error(w, "could not create login session", http.StatusInternalServerError)
					return
				}
				s.setBridgeLoginCookie(w, "webcodex_bridge_login", token)
				http.Redirect(w, r, bridgeLoginPath+"?"+r.URL.Query().Encode(), http.StatusSeeOther)
				return
			}
			errorText = "Неверный логин или пароль Gate."
		}
		token, err := s.newBridgeLoginToken("csrf")
		if err != nil {
			http.Error(w, "could not create login form", http.StatusInternalServerError)
			return
		}
		s.setBridgeLoginCookie(w, "webcodex_bridge_csrf", token)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := renderAdminTemplate(w, "admin-bridge-login.html", map[string]any{
			"Action": bridgeLoginPath + "?" + r.URL.Query().Encode(), "CSRF": token, "Error": errorText,
		}); err != nil {
			http.Error(w, "could not render login form", http.StatusInternalServerError)
		}
	}
}

func (s *server) setBridgeLoginCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: bridgeLoginPath,
		HttpOnly: true, Secure: strings.HasPrefix(s.publicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: 600})
}

func (s *server) bridgeLoginMAC(purpose, payload string) string {
	mac := hmac.New(sha256.New, []byte(s.adminCSRF))
	mac.Write([]byte(purpose + "\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *server) newBridgeLoginToken(purpose string) (string, error) {
	nonce, err := generateSecret("login_")
	if err != nil {
		return "", err
	}
	payload := strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10) + "." + nonce
	return payload + "." + s.bridgeLoginMAC(purpose, payload), nil
}

func (s *server) validBridgeLoginToken(purpose, token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expires <= time.Now().Unix() || expires > time.Now().Add(10*time.Minute).Unix() {
		return false
	}
	return secureCompare(parts[2], s.bridgeLoginMAC(purpose, parts[0]+"."+parts[1]))
}
