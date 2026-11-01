package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPITokens(t *testing.T) {
	dir := t.TempDir()
	a, _ := newAuth(filepath.Join(dir, "auth.json"))
	if _, err := a.createSuperuser("", "a@b.co", "0123456789"); err != nil {
		t.Fatal(err)
	}
	store, err := loadTokens(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.tokens = store
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /api/apps", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	tokenRoutes(mux, a, store)
	h := sameOrigin(a.middleware(mux))
	cookie := &http.Cookie{Name: sessionCookie, Value: a.sign(time.Now().Add(time.Hour))}

	do := func(method, path, body string, c *http.Cookie, bearerTok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if c != nil {
			r.AddCookie(c)
			r.Header.Set("X-Dokk", "1")
		}
		if bearerTok != "" {
			r.Header.Set("Authorization", "Bearer "+bearerTok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if c := do("POST", "/api/tokens", `{"name":""}`, cookie, "").Code; c != 400 {
		t.Fatalf("nome vazio = %d", c)
	}
	w := do("POST", "/api/tokens", `{"name":"ci"}`, cookie, "")
	var created struct{ ID, Name, Token string }
	json.Unmarshal(w.Body.Bytes(), &created)
	if w.Code != 200 || !strings.HasPrefix(created.Token, "dokk_") || created.ID == "" {
		t.Fatalf("criar = %d %s", w.Code, w.Body)
	}

	// O arquivo guarda só o hash, com permissão 0600.
	b, _ := os.ReadFile(filepath.Join(dir, "tokens.json"))
	if strings.Contains(string(b), created.Token) || !strings.Contains(string(b), hashToken(created.Token)) {
		t.Fatalf("tokens.json = %s", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, "tokens.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", st.Mode())
	}

	// Bearer vale em GET e em POST (sem X-Dokk); token errado não.
	if c := do("GET", "/api/apps", "", nil, created.Token).Code; c != 200 {
		t.Fatalf("bearer GET = %d", c)
	}
	if c := do("POST", "/api/apps", "", nil, created.Token).Code; c != 200 {
		t.Fatalf("bearer POST = %d", c)
	}
	if c := do("GET", "/api/apps", "", nil, created.Token+"x").Code; c != 401 {
		t.Fatalf("bearer errado = %d", c)
	}
	// Com Authorization inválido, o cookie não salva a requisição.
	if c := do("GET", "/api/apps", "", cookie, "dokk_nope").Code; c != 401 {
		t.Fatalf("bearer inválido + cookie = %d", c)
	}
	// Um token não cria nem apaga tokens.
	if c := do("POST", "/api/tokens", `{"name":"x"}`, nil, created.Token).Code; c != 403 {
		t.Fatalf("token criando token = %d", c)
	}
	if c := do("DELETE", "/api/tokens/"+created.ID, "", nil, created.Token).Code; c != 403 {
		t.Fatalf("token apagando token = %d", c)
	}

	w = do("GET", "/api/tokens", "", nil, created.Token)
	var list []map[string]any
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["lastUsedAt"] == nil || list[0]["token"] != nil || list[0]["hash"] != nil {
		t.Fatalf("listar = %s", w.Body)
	}

	// Sobrevive a restart; depois de apagado, para de valer.
	re, _ := loadTokens(filepath.Join(dir, "tokens.json"))
	if !re.check(created.Token) {
		t.Fatal("recarregar do disco")
	}
	if c := do("DELETE", "/api/tokens/nope", "", cookie, "").Code; c != 404 {
		t.Fatalf("apagar inexistente = %d", c)
	}
	if c := do("DELETE", "/api/tokens/"+created.ID, "", cookie, "").Code; c != 200 {
		t.Fatalf("apagar = %d", c)
	}
	if c := do("GET", "/api/apps", "", nil, created.Token).Code; c != 401 {
		t.Fatalf("token apagado = %d", c)
	}
}
