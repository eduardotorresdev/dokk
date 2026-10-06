package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	a, err := newAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	api := http.NewServeMux()
	api.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	api.HandleFunc("GET /api/session", a.session)
	api.HandleFunc("POST /api/login", a.login)
	api.HandleFunc("POST /api/setup", a.setup)
	api.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := sameOrigin(a.middleware(api))

	do := func(method, path, body string, cookie *http.Cookie, hdr bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if hdr {
			r.Header.Set("X-Dokk", "1")
		}
		r.Header.Set("X-Dokk-Lang", "pt-BR")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Primeira execução: a sessão avisa do setup e o login é recusado.
	if w := do("GET", "/api/session", "", nil, false); w.Code != 401 || !strings.Contains(w.Body.String(), `"setup":true`) {
		t.Fatalf("session antes do setup = %d %s", w.Code, w.Body)
	}
	if c := do("POST", "/api/login", `{"email":"a@b.co","password":"0123456789"}`, nil, true).Code; c != 409 {
		t.Fatalf("login antes do setup = %d", c)
	}
	if c := do("POST", "/api/setup", `{"email":"a@b.co","password":"curta"}`, nil, true).Code; c != 400 {
		t.Fatalf("senha curta = %d", c)
	}
	if c := do("POST", "/api/setup", `{"email":"nao-email","password":"0123456789"}`, nil, true).Code; c != 400 {
		t.Fatalf("e-mail inválido = %d", c)
	}
	w := do("POST", "/api/setup", `{"email":" Admin@Dokk.dev ","password":"s3nha-longa!"}`, nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":""`) {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	setupCookie := w.Result().Cookies()[0]
	if c := do("GET", "/api/apps", "", setupCookie, false).Code; c != 200 {
		t.Fatalf("sessão do setup = %d", c)
	}
	// Setup só uma vez.
	if c := do("POST", "/api/setup", `{"email":"x@y.co","password":"0123456789"}`, nil, true).Code; c != 409 {
		t.Fatalf("segundo setup = %d", c)
	}

	if c := do("GET", "/api/apps", "", nil, false).Code; c != 401 {
		t.Fatalf("sem sessão = %d", c)
	}
	if c := do("GET", "/", "", nil, false).Code; c != 200 {
		t.Fatalf("estático = %d", c)
	}
	if c := do("POST", "/api/login", `{"email":"admin@dokk.dev","password":"s3nha-longa!"}`, nil, false).Code; c != 403 {
		t.Fatalf("login sem X-Dokk = %d", c)
	}
	if c := do("POST", "/api/login", `{"email":"admin@dokk.dev","password":"errada"}`, nil, true).Code; c != 401 {
		t.Fatalf("senha errada = %d", c)
	}
	w = do("POST", "/api/login", `{"email":"ADMIN@dokk.dev","password":"s3nha-longa!"}`, nil, true)
	if w.Code != 200 {
		t.Fatalf("login = %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v", cookie)
	}
	forged := &http.Cookie{Name: sessionCookie, Value: a.sign(time.Now().Add(time.Hour))[:10] + "x.y"}
	if c := do("GET", "/api/apps", "", forged, false).Code; c != 401 {
		t.Fatalf("cookie forjado = %d", c)
	}
	if a.valid(a.sign(time.Now().Add(-time.Second))) {
		t.Error("token expirado aceito")
	}

	// O superusuário sobrevive a um restart (relido do disco).
	b, err := newAuth(path)
	if err != nil || b.current() == nil || !b.valid(cookie.Value) {
		t.Fatalf("recarregar do disco: %v", err)
	}
}

// Sem X-Dokk-Lang nem Accept-Language, o nome padrão e os erros saem em inglês.
func TestSetupDefaultNameEnglish(t *testing.T) {
	a, err := newAuth(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/setup", strings.NewReader(body))
		w := httptest.NewRecorder()
		a.setup(w, r)
		return w
	}
	if w := post(`{"email":"a@b.co","password":"curta"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "at least 10 characters") {
		t.Fatalf("senha curta = %d %s", w.Code, w.Body)
	}
	if w := post(`{"email":"a@b.co","password":"0123456789"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":""`) {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
}
