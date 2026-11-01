package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOAuthFlow(t *testing.T) {
	dir := t.TempDir()
	a, _ := newAuth(filepath.Join(dir, "auth.json"))
	if _, err := a.createSuperuser("", "a@b.co", "0123456789"); err != nil {
		t.Fatal(err)
	}
	a.tokens, _ = loadTokens(filepath.Join(dir, "tokens.json"))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("SPA")) })
	o := oauthRoutes(mux, a, spa)
	clock := time.Now()
	o.now = func() time.Time { return clock }
	h := sameOrigin(a.middleware(mux))
	cookie := &http.Cookie{Name: sessionCookie, Value: a.sign(time.Now().Add(time.Hour))}

	do := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "http://127.0.0.1:53111/callback"
	params := url.Values{"response_type": {"code"}, "client_id": {"dokk-cli"}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"}}

	// Metadados (RFC 8414) com a origem pública vinda do proxy.
	r := httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "dokk.example.com")
	w := do(r)
	var meta map[string]any
	json.Unmarshal(w.Body.Bytes(), &meta)
	if w.Code != 200 || meta["issuer"] != "https://dokk.example.com" || meta["token_endpoint"] != "https://dokk.example.com/oauth/token" {
		t.Fatalf("metadata = %d %s", w.Code, w.Body)
	}

	// GET authorize: válido vai para a SPA, mesmo sem sessão (o guard dela
	// manda para o login); inválido mostra página de erro, sem redirect.
	if w := do(httptest.NewRequest("GET", "/oauth/authorize?"+params.Encode(), nil)); w.Code != 200 || w.Body.String() != "SPA" {
		t.Fatalf("authorize válido = %d %s", w.Code, w.Body)
	}
	for name, mut := range map[string]func(url.Values){
		"não loopback": func(v url.Values) { v.Set("redirect_uri", "https://evil.example/cb") },
		"https":        func(v url.Values) { v.Set("redirect_uri", "https://127.0.0.1:1/cb") },
		"plain":        func(v url.Values) { v.Set("code_challenge_method", "plain") },
		"sem client":   func(v url.Values) { v.Del("client_id") },
	} {
		v := url.Values{}
		for k, x := range params {
			v[k] = x
		}
		mut(v)
		w := do(httptest.NewRequest("GET", "/oauth/authorize?"+v.Encode(), nil))
		if w.Code != 400 || w.Header().Get("Location") != "" || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s = %d %v", name, w.Code, w.Header())
		}
	}

	consent := func(approve bool, c *http.Cookie, v url.Values) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"client_id": v.Get("client_id"), "redirect_uri": v.Get("redirect_uri"), "code_challenge": v.Get("code_challenge"),
			"code_challenge_method": "S256", "state": v.Get("state"), "response_type": "code", "approve": approve})
		r := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(string(body)))
		r.Header.Set("X-Dokk", "1")
		if c != nil {
			r.AddCookie(c)
		}
		return do(r)
	}
	redirectOf := func(w *httptest.ResponseRecorder) *url.URL {
		var out struct{ Redirect string }
		json.Unmarshal(w.Body.Bytes(), &out)
		u, _ := url.Parse(out.Redirect)
		return u
	}

	if c := consent(true, nil, params).Code; c != 401 {
		t.Fatalf("consentimento sem cookie = %d", c)
	}
	if u := redirectOf(consent(false, cookie, params)); u.Query().Get("error") != "access_denied" || u.Query().Get("state") != "xyz" {
		t.Fatalf("negar = %v", u)
	}
	issue := func() string {
		w := consent(true, cookie, params)
		u := redirectOf(w)
		if w.Code != 200 || u.Host != "127.0.0.1:53111" || u.Query().Get("state") != "xyz" || u.Query().Get("code") == "" {
			t.Fatalf("autorizar = %d %s", w.Code, w.Body)
		}
		return u.Query().Get("code")
	}

	exchange := func(code, verifier, redirectURI string) (int, map[string]string) {
		form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "client_id": {"dokk-cli"}, "code_verifier": {verifier}}
		r := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := do(r)
		var out map[string]string
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// Verifier errado: invalid_grant, e o code morre.
	code := issue()
	if c, out := exchange(code, strings.Repeat("x", 50), redirect); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("verifier errado = %d %v", c, out)
	}
	if c, out := exchange(code, verifier, redirect); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("code depois de falha = %d %v", c, out)
	}
	// redirect_uri diferente do autorizado.
	code = issue()
	if c, out := exchange(code, verifier, "http://127.0.0.1:1/outro"); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("redirect trocado = %d %v", c, out)
	}
	// Expirado.
	code = issue()
	clock = clock.Add(61 * time.Second)
	if c, out := exchange(code, verifier, redirect); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("expirado = %d %v", c, out)
	}

	// Caminho feliz; reusar o code falha; o token funciona e aparece na lista.
	code = issue()
	c, out := exchange(code, verifier, redirect)
	if c != 200 || out["token_type"] != "Bearer" || !strings.HasPrefix(out["access_token"], "dokk_") {
		t.Fatalf("troca = %d %v", c, out)
	}
	if c, out := exchange(code, verifier, redirect); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("code reusado = %d %v", c, out)
	}
	r = httptest.NewRequest("GET", "/api/apps", nil)
	r.Header.Set("Authorization", "Bearer "+out["access_token"])
	if w := do(r); w.Code != 200 {
		t.Fatalf("token OAuth = %d", w.Code)
	}
	if l := a.tokens.all(); len(l) != 1 || l[0].Name != "dokk-cli (OAuth)" {
		t.Fatalf("tokens = %+v", l)
	}

	// grant_type desconhecido.
	r = httptest.NewRequest("POST", "/oauth/token", strings.NewReader("grant_type=password"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if w := do(r); w.Code != 400 || !strings.Contains(w.Body.String(), "unsupported_grant_type") {
		t.Fatalf("grant_type = %d %s", w.Code, w.Body)
	}
}

func TestLoopbackRedirect(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1:8080/cb":      true,
		"http://localhost:1/":           true,
		"http://[::1]:9/cb":             true,
		"http://127.0.0.1/cb":           true,
		"https://127.0.0.1:8080/cb":     false,
		"http://example.com:8080/cb":    false,
		"http://127.0.0.1.evil.com/cb":  false,
		"http://u:p@127.0.0.1:1/cb":     false,
		"http://127.0.0.1:1/cb#frag":    false,
		"http://localhost.evil.com:1/x": false,
	} {
		if got := loopbackRedirect(raw); got != want {
			t.Errorf("%s = %v", raw, got)
		}
	}
}
