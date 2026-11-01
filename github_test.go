package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub imita o pedaço da API do GitHub que o dokk usa.
type fakeGitHub struct {
	t        *testing.T
	key      *rsa.PublicKey
	mu       sync.Mutex
	tokens   int // quantos tokens de instalação foram emitidos
	tags     map[string]string
	releases []map[string]any
	lastBody map[string]any
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	reply := func(code int, v any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	notFound := func() { reply(404, map[string]string{"message": "Not Found"}) }
	p := r.URL.Path
	switch {
	case p == "/repos/acme/web/installation" || p == "/repos/acme/other/installation" || strings.HasPrefix(p, "/app/"):
		if !f.validJWT(auth) {
			reply(401, map[string]string{"message": "bad jwt"})
			return
		}
		if p == "/repos/acme/other/installation" {
			notFound()
			return
		}
		if p == "/repos/acme/web/installation" {
			reply(200, map[string]any{"id": 42})
			return
		}
		if p != "/app/installations/42/access_tokens" || r.Method != "POST" {
			notFound()
			return
		}
		f.tokens++
		reply(201, map[string]any{"token": "ghs_inst", "expires_at": time.Now().Add(time.Hour)})
		return
	}
	if auth != "ghs_inst" {
		reply(401, map[string]string{"message": "bad token"})
		return
	}
	switch {
	case r.Method == "GET" && p == "/repos/acme/web/releases":
		if r.URL.Query().Get("page") != "1" {
			reply(200, []any{})
			return
		}
		reply(200, f.releases)
	case r.Method == "GET" && strings.HasPrefix(p, "/repos/acme/web/git/matching-refs/tags/"):
		prefix := strings.TrimPrefix(p, "/repos/acme/web/git/matching-refs/tags/")
		out := []map[string]any{}
		for n, s := range f.tags {
			if strings.HasPrefix(n, prefix) && r.URL.Query().Get("page") == "1" {
				out = append(out, map[string]any{"ref": "refs/tags/" + n, "object": map[string]string{"sha": s, "type": "commit"}})
			}
		}
		reply(200, out)
	case r.Method == "GET" && p == "/repos/acme/web/tags":
		var out []map[string]any
		for n, s := range f.tags {
			out = append(out, map[string]any{"name": n, "commit": map[string]string{"sha": s}})
		}
		reply(200, out)
	case r.Method == "GET" && strings.HasPrefix(p, "/repos/acme/web/commits/"):
		sha := strings.TrimPrefix(p, "/repos/acme/web/commits/")
		if !strings.HasPrefix("abcdef0123456789abcdef0123456789abcdef01", sha) {
			reply(422, map[string]string{"message": "No commit found"})
			return
		}
		reply(200, map[string]string{"sha": "abcdef0123456789abcdef0123456789abcdef01"})
	case r.Method == "GET" && strings.HasPrefix(p, "/repos/acme/web/git/ref/tags/"):
		if _, ok := f.tags[strings.TrimPrefix(p, "/repos/acme/web/git/ref/tags/")]; ok {
			reply(200, map[string]string{})
			return
		}
		notFound()
	case r.Method == "POST" && p == "/repos/acme/web/git/refs":
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.tags[strings.TrimPrefix(b["ref"], "refs/tags/")] = b["sha"]
		reply(201, b)
	case r.Method == "POST" && p == "/repos/acme/web/releases":
		json.NewDecoder(r.Body).Decode(&f.lastBody)
		tag := f.lastBody["tag_name"].(string)
		reply(201, map[string]string{"html_url": "https://github.com/acme/web/releases/tag/" + tag})
	default:
		notFound()
	}
}

func (f *fakeGitHub) validJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(f.key, crypto.SHA256, d[:], sig) != nil {
		return false
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	json.Unmarshal(claims, &c)
	return c.Iss == "123" && c.Exp > time.Now().Unix()
}

func TestGitHubReleases(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	fake := &fakeGitHub{t: t, key: &key.PublicKey, tags: map[string]string{"v1.0.0": "1111111111111111111111111111111111111111", "v1.1.0-rc": "2222222222222222222222222222222222222222",
		"v0.9.0": "0909090909090909090909090909090909090909", "back@1.17.0": "1717171717171717171717171717171717171717", "back@1.9.0": "1919191919191919191919191919191919191919",
		"front@2.0.0": "2020202020202020202020202020202020202020", "draft": "dddddddddddddddddddddddddddddddddddddddd"},
		releases: []map[string]any{
			{"tag_name": "v1.0.0", "name": "v1.0.0", "html_url": "u1", "created_at": "2026-01-01T00:00:00Z"},
			{"tag_name": "v1.1.0-rc", "name": "rc", "html_url": "u2", "created_at": "2026-02-01T00:00:00Z", "prerelease": true},
			{"tag_name": "draft", "draft": true, "created_at": "2026-03-01T00:00:00Z"},
			{"tag_name": "back@1.17.0", "name": "back 1.17.0", "html_url": "u3", "created_at": "2026-03-02T00:00:00Z"},
		}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	dir := t.TempDir()
	g, err := newGitHub(dir, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	githubRoutes(mux, g, func(_ context.Context, name string) (bool, error) { return name == "web" || name == "api", nil })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Dokk-Lang", "en")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	if w := do("GET", "/api/github", ""); !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("antes de configurar = %s", w.Body)
	}
	if c := do("PUT", "/api/github", `{"appId":123,"slug":"dokk-test","privateKey":"lixo"}`).Code; c != 400 {
		t.Fatalf("chave inválida = %d", c)
	}
	if c := do("PUT", "/api/github", `{"appId":0,"slug":"dokk-test"}`).Code; c != 400 {
		t.Fatalf("appId 0 = %d", c)
	}
	// Vínculo e release antes de configurar a App.
	if c := do("GET", "/api/apps/web/github/releases", "").Code; c != 400 {
		t.Fatalf("releases sem repo = %d", c)
	}
	if c := do("PUT", "/api/apps/web/github", `{"repo":"https://github.com/acme/web.git"}`).Code; c != 200 {
		t.Fatalf("vincular = %d", c)
	}
	if w := do("GET", "/api/apps/web/github/releases", ""); w.Code != 400 || !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("releases sem App = %d %s", w.Code, w.Body)
	}

	body, _ := json.Marshal(map[string]any{"appId": 123, "slug": "dokk-test", "privateKey": pemKey})
	w := do("PUT", "/api/github", string(body))
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE") || !strings.Contains(w.Body.String(), "github.com/apps/dokk-test/installations/new") {
		t.Fatalf("configurar = %d %s", w.Code, w.Body)
	}
	if st, _ := os.Stat(filepath.Join(dir, "github.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("perm github.json = %v", st.Mode())
	}
	// Trocar só o slug mantém a chave.
	if w := do("PUT", "/api/github", `{"appId":123,"slug":"dokk-test"}`); !strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatalf("editar sem chave = %s", w.Body)
	}

	if w := do("GET", "/api/apps/web/github", ""); w.Body.String() != `{"installed":true,"repo":"acme/web","tagFormat":"v{version}"}`+"\n" {
		t.Fatalf("vínculo = %s", w.Body)
	}
	if c := do("PUT", "/api/apps/nope/github", `{"repo":"acme/web"}`).Code; c != 404 {
		t.Fatalf("app inexistente = %d", c)
	}
	if c := do("PUT", "/api/apps/api/github", `{"repo":"../etc"}`).Code; c != 400 {
		t.Fatalf("repo inválido = %d", c)
	}
	do("PUT", "/api/apps/api/github", `{"repo":"acme/other"}`)
	if w := do("GET", "/api/apps/api/github", ""); !strings.Contains(w.Body.String(), `"installed":false`) {
		t.Fatalf("não instalada = %s", w.Body)
	}
	if c := do("GET", "/api/apps/api/github/releases", "").Code; c != 400 {
		t.Fatalf("releases em repo sem a App = %d", c)
	}

	w = do("GET", "/api/apps/web/github/releases", "")
	var rels []releaseJSON
	json.Unmarshal(w.Body.Bytes(), &rels)
	// Só as tags do formato, com ou sem Release, maior versão primeiro.
	if w.Code != 200 || len(rels) != 3 || rels[0].Tag != "v1.1.0-rc" || !rels[0].Prerelease || rels[1].SHA != "1111111111111111111111111111111111111111" ||
		rels[1].Version != "1.0.0" || rels[1].URL != "u1" || rels[2].Tag != "v0.9.0" || rels[2].URL != "" {
		t.Fatalf("releases = %d %s", w.Code, w.Body)
	}
	if c := do("POST", "/api/apps/web/github/releases", `{"tag":"back@3.0.0","sha":"abcdef0","image":"x:3"}`).Code; c != 400 {
		t.Fatalf("tag fora do formato = %d", c)
	}

	if c := do("POST", "/api/apps/web/github/releases", `{"tag":"v1.0.0","sha":"abcdef0","image":"ghcr.io/acme/web:1"}`).Code; c != 409 {
		t.Fatalf("tag existente = %d", c)
	}
	if c := do("POST", "/api/apps/web/github/releases", `{"tag":"v2.0.0","sha":"9999999","image":"ghcr.io/acme/web:2"}`).Code; c != 422 {
		t.Fatalf("sha desconhecido = %d", c)
	}
	if c := do("POST", "/api/apps/web/github/releases", `{"tag":"v2.0.0","sha":"abcdef0","image":""}`).Code; c != 400 {
		t.Fatalf("sem imagem = %d", c)
	}
	w = do("POST", "/api/apps/web/github/releases", `{"tag":"v2.0.0","sha":"abcdef0","image":"ghcr.io/acme/web:2","notes":"Hotfix","prerelease":true}`)
	if w.Code != 200 || w.Body.String() != `{"tag":"v2.0.0","url":"https://github.com/acme/web/releases/tag/v2.0.0"}`+"\n" {
		t.Fatalf("criar release = %d %s", w.Code, w.Body)
	}
	if fake.tags["v2.0.0"] != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("tag no sha completo = %q", fake.tags["v2.0.0"])
	}
	lb := fake.lastBody
	if lb["generate_release_notes"] != true || lb["prerelease"] != true || !strings.Contains(lb["body"].(string), "Hotfix") || !strings.Contains(lb["body"].(string), "ghcr.io/acme/web:2") {
		t.Errorf("corpo da release = %v", lb)
	}
	// Token de instalação em cache: um só para todas as chamadas do repo.
	if fake.tokens != 1 {
		t.Errorf("tokens emitidos = %d", fake.tokens)
	}

	// Monorepo: formato back@{version}.
	for _, f := range []string{"back", "{version}{version}", "back@{version}..", "a b{version}"} {
		if c := do("PUT", "/api/apps/web/github", `{"repo":"acme/web","tagFormat":"`+f+`"}`).Code; c != 400 {
			t.Errorf("formato %q = %d", f, c)
		}
	}
	if w := do("PUT", "/api/apps/web/github", `{"repo":"acme/web","tagFormat":"back@{version}"}`); !strings.Contains(w.Body.String(), `"tagFormat":"back@{version}"`) {
		t.Fatalf("formato = %s", w.Body)
	}
	// PUT sem tagFormat mantém o atual.
	if w := do("PUT", "/api/apps/web/github", `{"repo":"acme/web"}`); !strings.Contains(w.Body.String(), `"tagFormat":"back@{version}"`) {
		t.Fatalf("manter formato = %s", w.Body)
	}
	w = do("GET", "/api/apps/web/github/releases", "")
	rels = nil
	json.Unmarshal(w.Body.Bytes(), &rels)
	if len(rels) != 2 || rels[0].Tag != "back@1.17.0" || rels[0].Version != "1.17.0" || rels[0].URL != "u3" || rels[1].Version != "1.9.0" || rels[1].URL != "" {
		t.Fatalf("releases back@ = %s", w.Body)
	}
	if c := do("POST", "/api/apps/web/github/releases", `{"tag":"v9.0.0","sha":"abcdef0","image":"x:9"}`).Code; c != 400 {
		t.Fatalf("v9.0.0 em app back@ = %d", c)
	}
	if c := do("POST", "/api/apps/web/github/releases", `{"tag":"back@1.17.1","sha":"abcdef0","image":"x:1.17.1"}`).Code; c != 200 {
		t.Fatalf("back@1.17.1 = %d", c)
	}

	// Vínculos e config sobrevivem a restart; DELETE desfaz o vínculo.
	g2, err := newGitHub(dir, srv.URL)
	if err != nil || !g2.configured() || g2.link("web") != "acme/web" || g2.tagFormat("web") != "back@{version}" {
		t.Fatalf("recarregar: %v", err)
	}
	do("DELETE", "/api/apps/web/github", "")
	if w := do("GET", "/api/apps/web/github", ""); !strings.Contains(w.Body.String(), `"repo":null`) {
		t.Fatalf("depois do DELETE = %s", w.Body)
	}
}

func TestTagFormat(t *testing.T) {
	for tag, want := range map[string]string{"back@1.17.0": "1.17.0", "back@1.2.3-rc.1": "1.2.3-rc.1", "back@1.2": "", "front@1.0.0": "", "back@": ""} {
		if v, _ := versionOf("back@{version}", tag); v != want && want != "" {
			t.Errorf("%s = %q", tag, v)
		}
		if matchesFormat("back@{version}", tag) != (want != "") {
			t.Errorf("matches %s", tag)
		}
	}
	if !matchesFormat("release-{version}-final", "release-2.0.0-final") {
		t.Error("sufixo")
	}
	// github-apps.json antigo (só a string do repo) continua valendo.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "github-apps.json"), []byte(`{"web":"acme/web","api":{"repo":"acme/pbs","tagFormat":"back@{version}"}}`), 0o600)
	g, err := newGitHub(dir, "")
	if err != nil || g.link("web") != "acme/web" || g.tagFormat("web") != "v{version}" || g.tagFormat("api") != "back@{version}" {
		t.Fatalf("legado: %v %+v", err, g.links)
	}
	if !versionLess("1.9.0", "1.17.0") || !versionLess("1.2.0-rc", "1.2.0") || versionLess("1.2.0", "1.2.0-rc") {
		t.Error("versionLess")
	}
}
