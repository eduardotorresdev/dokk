package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Login por e-mail e senha com sessão em cookie assinado (HMAC), sem estado
// de sessão no servidor. O superusuário fica num arquivo (criado pelo
// formulário de primeira execução); enquanto ele não existir, o site só
// mostra esse formulário.
const (
	sessionCookie = "dokk_session"
	sessionTTL    = 30 * 24 * time.Hour
	pbkdf2Iter    = 600_000
	minPassword   = 10
)

// superuser é o que vai para o disco: a senha só como hash PBKDF2-SHA256, e
// a chave que assina as sessões (nova a cada troca de senha, o que derruba
// todas as sessões abertas).
type superuser struct {
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Salt       []byte    `json:"salt"`
	Hash       []byte    `json:"hash"`
	SessionKey []byte    `json:"session-key"`
	CreatedAt  time.Time `json:"created-at"`
}

type auth struct {
	path    string
	mu      sync.RWMutex
	user    *superuser // nil = primeira execução
	limiter *loginLimiter
	prefs   *prefs // idioma salvo; main.go liga depois do newAuth
}

var errSuperuserExists = errors.New("o superusuário já foi criado")

func newAuth(path string) (*auth, error) {
	a := &auth{path: path, limiter: &loginLimiter{fails: map[string][]time.Time{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	var u superuser
	if err := json.Unmarshal(b, &u); err != nil {
		return nil, err
	}
	a.user = &u
	return a, nil
}

func hashPassword(password string, salt []byte) []byte {
	h, _ := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, 32)
	return h
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// createSuperuser grava o superusuário (só se ainda não existir).
func (a *auth) createSuperuser(name, email, password string) (*superuser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.user != nil {
		return nil, errSuperuserExists
	}
	salt := randomBytes(16)
	u := &superuser{Name: name, Email: email, Salt: salt, Hash: hashPassword(password, salt), SessionKey: randomBytes(32), CreatedAt: time.Now().UTC()}
	b, _ := json.MarshalIndent(u, "", "  ")
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return nil, err
	}
	// Grava num temporário e renomeia: nunca fica um arquivo pela metade.
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return nil, err
	}
	a.user = u
	return u, nil
}

func (a *auth) current() *superuser {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.user
}

// token = base64(expiração unix) + "." + base64(hmac).
func (a *auth) sign(exp time.Time) string {
	u := a.current()
	if u == nil {
		return ""
	}
	payload := strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, u.SessionKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *auth) valid(token string) bool {
	u := a.current()
	if u == nil {
		return false
	}
	p64, sig64, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(p64)
	sig, err2 := base64.RawURLEncoding.DecodeString(sig64)
	if err1 != nil || err2 != nil {
		return false
	}
	mac := hmac.New(sha256.New, u.SessionKey)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	exp, err := strconv.ParseInt(string(payload), 10, 64)
	return err == nil && time.Now().Unix() < exp
}

func (a *auth) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.valid(c.Value)
}

func (a *auth) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *auth) startSession(w http.ResponseWriter, r *http.Request) {
	a.setCookie(w, r, a.sign(time.Now().Add(sessionTTL)), int(sessionTTL.Seconds()))
}

func userJSON(u *superuser) map[string]string {
	return map[string]string{"name": u.Name, "email": u.Email}
}

// setup cria o superusuário na primeira execução e já abre a sessão.
func (a *auth) setup(w http.ResponseWriter, r *http.Request) {
	if a.current() != nil {
		writeError(w, r, http.StatusConflict, "err.superuserExists")
		return
	}
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Lang     string `json:"lang"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	name := strings.TrimSpace(body.Name)
	email := strings.ToLower(strings.TrimSpace(body.Email))
	// Sem nome fica vazio: a UI mostra o nome padrão no idioma ativo.
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		writeError(w, r, http.StatusBadRequest, "err.invalidEmail")
		return
	}
	if len(body.Password) < minPassword {
		writeError(w, r, http.StatusBadRequest, "err.passwordTooShort", minPassword)
		return
	}
	u, err := a.createSuperuser(name, email, body.Password)
	if errors.Is(err, errSuperuserExists) {
		writeError(w, r, http.StatusConflict, "err.superuserExists")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	// O idioma escolhido na tela de setup vira a preferência da instalação.
	if a.prefs != nil && validLang(body.Lang) {
		if err := a.prefs.setLang(body.Lang); err != nil {
			log.Printf("prefs: %v", err)
		}
	}
	a.startSession(w, r)
	writeJSON(w, http.StatusOK, userJSON(u))
}

func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	u := a.current()
	if u == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": msg(r, "err.setupRequired"), "setup": true, "lang": a.prefs.lang()})
		return
	}
	ip := clientIP(r)
	if !a.limiter.allow(ip) {
		writeError(w, r, http.StatusTooManyRequests, "err.tooManyAttempts")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	email := strings.ToLower(strings.TrimSpace(body.Email))
	okEmail := subtle.ConstantTimeCompare([]byte(email), []byte(u.Email)) == 1
	okPass := subtle.ConstantTimeCompare(hashPassword(body.Password, u.Salt), u.Hash) == 1
	if !okEmail || !okPass {
		a.limiter.fail(ip)
		time.Sleep(400 * time.Millisecond)
		writeError(w, r, http.StatusUnauthorized, "err.badCredentials")
		return
	}
	a.limiter.reset(ip)
	a.startSession(w, r)
	writeJSON(w, http.StatusOK, userJSON(u))
}

func (a *auth) logout(w http.ResponseWriter, r *http.Request) {
	a.setCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]string{})
}

// session diz quem está logado; sem superusuário, avisa que é a primeira
// execução (setup: true) para a UI mostrar o formulário de criação. Toda
// resposta leva o idioma salvo ("lang", "" = não escolhido).
func (a *auth) session(w http.ResponseWriter, r *http.Request) {
	lang := a.prefs.lang()
	u := a.current()
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": msg(r, "err.setupRequired"), "setup": true, "lang": lang})
		return
	}
	if !a.loggedIn(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": msg(r, "err.unauthenticated"), "lang": lang})
		return
	}
	body := map[string]any{"lang": lang}
	for k, v := range userJSON(u) {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
}

// middleware: a API exige sessão (menos login/setup/sessão); os arquivos da
// UI são públicos (a própria UI manda para o login ou para o setup).
func (a *auth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login", "/api/logout", "/api/session", "/api/setup":
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !a.loggedIn(r) {
			writeError(w, r, http.StatusUnauthorized, "err.unauthenticated")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP usa o X-Real-IP do nginx quando a conexão vem do próprio host.
func clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if real := r.Header.Get("X-Real-IP"); real != "" {
			return real
		}
	}
	return host
}

// loginLimiter: no máximo 8 tentativas erradas por IP a cada 15 minutos.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

const (
	maxLoginFails = 8
	loginWindow   = 15 * time.Minute
)

func (l *loginLimiter) recent(ip string) []time.Time {
	var keep []time.Time
	for _, t := range l.fails[ip] {
		if time.Since(t) < loginWindow {
			keep = append(keep, t)
		}
	}
	if keep == nil {
		delete(l.fails, ip)
	} else {
		l.fails[ip] = keep
	}
	return keep
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) < maxLoginFails
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.recent(ip), time.Now())
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
