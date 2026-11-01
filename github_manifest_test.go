package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitHubManifestFlow(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	conversions := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		p := r.URL.Path
		switch {
		case r.Method == "POST" && p == "/app-manifests/good/conversions":
			conversions++
			json.NewEncoder(w).Encode(map[string]any{"id": 77, "slug": "dokk-ops-ab12", "pem": pemKey, "client_id": "Iv1.x", "client_secret": "sec", "webhook_secret": "wh"})
		case p == "/app-manifests/bad/conversions":
			w.WriteHeader(404)
		case p == "/app/installations" && strings.HasPrefix(auth, "Bearer ey"):
			json.NewEncoder(w).Encode([]map[string]any{{"id": 1}, {"id": 2}})
		case r.Method == "POST" && strings.HasPrefix(p, "/app/installations/"):
			json.NewEncoder(w).Encode(map[string]any{"token": "ghs_" + strings.Split(p, "/")[3], "expires_at": time.Now().Add(time.Hour)})
		case p == "/installation/repositories" && auth == "Bearer ghs_1":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"full_name": "acme/web", "private": true}, {"full_name": "Acme/api", "private": false}}})
		case p == "/installation/repositories" && auth == "Bearer ghs_2":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"full_name": "me/blog", "private": false}, {"full_name": "acme/web", "private": true}}})
		default:
			w.WriteHeader(401)
		}
	}))
	defer fake.Close()

	dir := t.TempDir()
	a, _ := newAuth(filepath.Join(dir, "auth.json"))
	if _, err := a.createSuperuser("", "a@b.co", "0123456789"); err != nil {
		t.Fatal(err)
	}
	a.tokens, _ = loadTokens(filepath.Join(dir, "tokens.json"))
	g, _ := newGitHub(dir, fake.URL)
	mux := http.NewServeMux()
	manifestRoutes(mux, g, a, "https://gh.test")
	h := sameOrigin(a.middleware(mux))
	cookie := &http.Cookie{Name: sessionCookie, Value: a.sign(time.Now().Add(time.Hour))}
	other := &http.Cookie{Name: sessionCookie, Value: a.sign(time.Now().Add(2 * time.Hour))}
	do := func(method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Dokk", "1")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "ops.example.com")
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	start := func(org string) (action string, manifest map[string]any) {
		w := do("POST", "/api/github/manifest", `{"org":"`+org+`"}`, cookie)
		var out struct{ Action, Manifest string }
		json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 {
			t.Fatalf("manifest = %d %s", w.Code, w.Body)
		}
		json.Unmarshal([]byte(out.Manifest), &manifest)
		return out.Action, manifest
	}
	stateOf := func(action string) string { return action[strings.Index(action, "state=")+6:] }

	action, man := start("")
	if !strings.HasPrefix(action, "https://gh.test/settings/apps/new?state=") || man["redirect_url"] != "https://ops.example.com/github/callback" ||
		man["setup_url"] != "https://ops.example.com/github/installed" || man["public"] != false || !strings.HasPrefix(man["name"].(string), "dokk-ops-example-com-") {
		t.Fatalf("manifest = %s %v", action, man)
	}
	if perms := man["default_permissions"].(map[string]any); perms["contents"] != "write" || perms["metadata"] != "read" {
		t.Errorf("permissões = %v", perms)
	}
	if a, _ := start("acme"); !strings.HasPrefix(a, "https://gh.test/organizations/acme/settings/apps/new?state=") {
		t.Errorf("org = %s", a)
	}
	if c := do("POST", "/api/github/manifest", `{"org":"../x"}`, cookie).Code; c != 400 {
		t.Errorf("org inválida = %d", c)
	}
	if c := do("POST", "/api/github/manifest", `{}`, nil).Code; c != 401 {
		t.Errorf("sem cookie = %d", c)
	}

	loc := func(w *httptest.ResponseRecorder) string { return w.Header().Get("Location") }
	// Sem cookie: login. State desconhecido, de outra sessão ou expirado: erro.
	if l := loc(do("GET", "/github/callback?code=good&state="+stateOf(action), "", nil)); l != "/login" {
		t.Errorf("callback sem cookie = %s", l)
	}
	if l := loc(do("GET", "/github/callback?code=good&state=nope", "", cookie)); l != "/settings?github=state" {
		t.Errorf("state inválido = %s", l)
	}
	if l := loc(do("GET", "/github/callback?code=good&state="+stateOf(action), "", other)); l != "/settings?github=state" {
		t.Errorf("state de outra sessão = %s", l)
	}
	action, _ = start("")
	now := time.Now()
	g.now = func() time.Time { return now.Add(11 * time.Minute) }
	if l := loc(do("GET", "/github/callback?code=good&state="+stateOf(action), "", cookie)); l != "/settings?github=state" {
		t.Errorf("state expirado = %s", l)
	}
	g.now = time.Now
	action, _ = start("")
	if l := loc(do("GET", "/github/callback?code=bad&state="+stateOf(action), "", cookie)); l != "/settings?github=error" || g.configured() {
		t.Errorf("conversão falhou = %s", l)
	}

	// Caminho feliz; reusar o state falha.
	action, _ = start("")
	if l := loc(do("GET", "/github/callback?code=good&state="+stateOf(action), "", cookie)); l != "https://gh.test/apps/dokk-ops-ab12/installations/new" {
		t.Fatalf("callback = %s", l)
	}
	if l := loc(do("GET", "/github/callback?code=good&state="+stateOf(action), "", cookie)); l != "/settings?github=state" || conversions != 1 {
		t.Errorf("state reusado = %s (%d conversões)", l, conversions)
	}
	if !g.configured() || g.publicJSON()["slug"] != "dokk-ops-ab12" {
		t.Fatalf("config = %v", g.publicJSON())
	}
	st, _ := os.Stat(filepath.Join(dir, "github.json"))
	b, _ := os.ReadFile(filepath.Join(dir, "github.json"))
	if st.Mode().Perm() != 0o600 || !strings.Contains(string(b), `"clientSecret": "sec"`) {
		t.Errorf("github.json = %v %s", st.Mode(), b)
	}
	if l := loc(do("GET", "/github/installed?installation_id=1&setup_action=install", "", cookie)); l != "/settings?github=installed" {
		t.Errorf("installed = %s", l)
	}

	// Repos de todas as instalações, sem repetição, em ordem.
	w := do("GET", "/api/github/repos", "", cookie)
	if w.Body.String() != `[{"fullName":"Acme/api","private":false},{"fullName":"acme/web","private":true},{"fullName":"me/blog","private":false}]`+"\n" {
		t.Fatalf("repos = %d %s", w.Code, w.Body)
	}
}

func TestAppName(t *testing.T) {
	for _, o := range []string{"https://ops.portile.com.br", "http://127.0.0.1:8080", "https://a-very-long-subdomain.of-some-company.example.com"} {
		n := appName(o)
		if len(n) > 34 || !strings.HasPrefix(n, "dokk-") || strings.Contains(n, "--") {
			t.Errorf("%s → %s", o, n)
		}
	}
}
