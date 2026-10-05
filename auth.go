package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const sessionCookieName = "cast_session"

func getOrInitSecret(path string) []byte {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b
	}
	buf := make([]byte, 32)
	rand.Read(buf)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, buf, 0600)
	return buf
}

func signSession(secret []byte, user string, expiresAt int64) string {
	msg := user + ":" + strconv.FormatInt(expiresAt, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return sig + "." + user
}

func (s *server) validSession(r *http.Request) bool {
	if s.password == "" {
		return true // sem senha configurada: acesso liberado (ex: rede local de confiança)
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return false
	}
	sig, user := parts[0], parts[1]
	expected := signSession(s.secret, user, 0)
	expectedSig := strings.Split(expected, ".")[0]
	return hmac.Equal([]byte(sig), []byte(expectedSig))
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		pwd := r.FormValue("password")
		if pwd == s.password {
			token := signSession(s.secret, "admin", 0)
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   86400 * 30, // 30 dias
			})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/login?err=1", http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>eajdias-cast - Login</title>
<style>
  @font-face { font-family: "Inter"; src: url("/static/inter-700.woff2") format("woff2"); font-weight: 700; font-display: swap; }
  body { background: #0b0b0e; color: #ece7df; font-family: "Inter", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
  .box { background: #131318; padding: 2.5rem; border-radius: 8px; border: 1px solid rgba(255,255,255,0.07); width: 100%; max-width: 340px; text-align: center; }
  h1 { margin: 0 0 1.5rem; font-size: 1.5rem; color: #ece7df; letter-spacing: -0.02em; }
  h1 b { color: #e8b25a; }
  input { width: 100%; padding: 0.75rem 1rem; box-sizing: border-box; background: #0b0b0e; border: 1px solid #33322e; border-radius: 6px; color: #fff; margin-bottom: 1.2rem; font-size: 1rem; }
  input:focus { outline: none; border-color: #e8b25a; }
  button { width: 100%; padding: 0.8rem; background: #e8b25a; color: #1a1206; font-weight: bold; border: none; border-radius: 6px; cursor: pointer; font-size: 1rem; transition: opacity 0.2s; }
  button:hover { opacity: 0.9; }
  .err { color: #ff5555; margin-bottom: 1rem; font-size: 0.85rem; }
</style>
</head>
<body>
<div class="box">
  <h1>eajdias<b>&middot;cast</b></h1>
  ` + func() string {
		if r.URL.Query().Get("err") != "" {
			return `<div class="err">Senha incorreta</div>`
		}
		return ""
	}() + `
  <form method="POST" action="/login">
    <input type="password" name="password" placeholder="Senha de acesso" autofocus required>
    <button type="submit">Entrar</button>
  </form>
</div>
</body>
</html>`))
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}
