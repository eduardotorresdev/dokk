package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrefs(t *testing.T) {
	dir := t.TempDir()
	a, err := newAuth(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.prefs = loadPrefs(filepath.Join(dir, "prefs.json"))
	mux := http.NewServeMux()
	prefsRoutes(mux, a.prefs)
	mux.HandleFunc("GET /api/session", a.session)
	mux.HandleFunc("POST /api/setup", a.setup)
	h := sameOrigin(a.middleware(mux))

	do := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Dokk", "1")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Antes do setup a sessão já diz que não há idioma salvo.
	if w := do("GET", "/api/session", "", nil); w.Code != 401 || !strings.Contains(w.Body.String(), `"lang":""`) {
		t.Fatalf("session antes do setup = %d %s", w.Code, w.Body)
	}
	if c := do("PUT", "/api/prefs", `{"lang":"es"}`, nil).Code; c != 401 {
		t.Fatalf("PUT sem sessão = %d", c)
	}
	w := do("POST", "/api/setup", `{"email":"a@b.co","password":"0123456789","lang":"pt-BR"}`, nil)
	if w.Code != 200 {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	if got := loadPrefs(filepath.Join(dir, "prefs.json")).lang(); got != "pt-BR" {
		t.Fatalf("setup não salvou o idioma: %q", got)
	}
	cookie := w.Result().Cookies()[0]

	for _, bad := range []string{`{"lang":"fr"}`, `{"lang":"pt"}`, `{"lang":""}`, `nada`} {
		if w := do("PUT", "/api/prefs", bad, cookie); w.Code != 400 {
			t.Errorf("PUT %s = %d", bad, w.Code)
		}
	}
	if w := do("PUT", "/api/prefs", `{"lang":"es"}`, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"lang":"es"`) {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/session", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"lang":"es"`) {
		t.Fatalf("session = %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/session", "", nil); w.Code != 401 || !strings.Contains(w.Body.String(), `"lang":"es"`) {
		t.Fatalf("session sem login = %d %s", w.Code, w.Body)
	}
	if got := loadPrefs(filepath.Join(dir, "prefs.json")).lang(); got != "es" {
		t.Fatalf("idioma não persistiu: %q", got)
	}
	var nilPrefs *prefs
	if nilPrefs.lang() != "" {
		t.Error("prefs nil deveria responder vazio")
	}
}
