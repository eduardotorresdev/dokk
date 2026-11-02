package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitLab imita a parte do GitLab que o dokk usa: OAuth (token, refresh,
// revoke) e a API v4 (user, projects, tags, releases, commits).
type fakeGitLab struct {
	mu        sync.Mutex
	challenge string // code_challenge do authorize
	access    string
	refresh   string
	n         int // geração do token
	refreshes int
	revoked   []string
	tags      map[string]string
	created   map[string]any
}

const glSHA = "abcdef0123456789abcdef0123456789abcdef01"

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	issue := func() {
		f.n++
		f.access, f.refresh = fmt.Sprintf("at%d", f.n), fmt.Sprintf("rt%d", f.n)
		reply(200, map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": 7200, "token_type": "Bearer"})
	}
	p := r.URL.EscapedPath()
	if r.Method == "POST" && strings.HasPrefix(p, "/oauth/") {
		r.ParseForm()
		v := r.PostForm
		if v.Get("client_id") != "app-id" || v.Get("client_secret") != "app-secret" {
			reply(401, map[string]string{"error": "invalid_client"})
			return
		}
		switch {
		case p == "/oauth/revoke":
			f.revoked = append(f.revoked, v.Get("token"))
			reply(200, map[string]any{})
		case v.Get("grant_type") == "authorization_code":
			sum := sha256.Sum256([]byte(v.Get("code_verifier")))
			if v.Get("code") != "good" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || v.Get("redirect_uri") != "https://ops.example.com/gitlab/callback" {
				reply(400, map[string]string{"error": "invalid_grant", "error_description": "bad code"})
				return
			}
			issue()
		case v.Get("grant_type") == "refresh_token":
			if v.Get("refresh_token") != f.refresh || v.Get("redirect_uri") != "https://ops.example.com/gitlab/callback" {
				reply(400, map[string]string{"error": "invalid_grant"})
				return
			}
			f.refreshes++
			issue()
		default:
			reply(400, map[string]string{"error": "unsupported_grant_type"})
		}
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.access || f.access == "" {
		reply(401, map[string]string{"message": "401 Unauthorized"})
		return
	}
	q := r.URL.Query()
	page := q.Get("page")
	proj := "/api/v4/projects/grp%2Fsub%2Fweb"
	switch {
	case p == "/api/v4/user":
		reply(200, map[string]string{"username": "edu"})
	case p == "/api/v4/projects":
		if q.Get("membership") != "true" || q.Get("min_access_level") != "30" {
			reply(400, map[string]string{"message": "filtro"})
			return
		}
		out := []map[string]string{}
		from, to := 0, 100
		if page == "2" {
			from, to = 100, 150
		} else if page != "1" {
			to = 0
		}
		for i := from; i < to; i++ {
			vis := "private"
			if i == 0 {
				vis = "public"
			}
			out = append(out, map[string]string{"path_with_namespace": fmt.Sprintf("grp/p%03d", i), "visibility": vis})
		}
		reply(200, out)
	case p == proj:
		reply(200, map[string]any{"id": 7})
	case p == proj+"/repository/tags":
		if q.Get("search") != "^back@" {
			reply(200, []any{})
			return
		}
		out := []map[string]any{}
		if page == "1" { // página cheia de ruído, para forçar a segunda
			for i := range 100 {
				out = append(out, map[string]any{"name": fmt.Sprintf("back@0.0.%d", i), "commit": map[string]any{"id": "0000000"}})
			}
		} else if page == "2" {
			for n, s := range f.tags {
				out = append(out, map[string]any{"name": n, "commit": map[string]any{"id": s, "created_at": "2026-10-01T00:00:00Z"}})
			}
		}
		reply(200, out)
	case p == proj+"/releases" && r.Method == "GET":
		if page != "1" {
			reply(200, []any{})
			return
		}
		reply(200, []map[string]any{{"tag_name": "back@1.17.0", "name": "Back 1.17", "created_at": "2026-10-02T00:00:00Z", "_links": map[string]string{"self": "https://gl/grp/sub/web/-/releases/back@1.17.0"}}})
	case p == proj+"/releases" && r.Method == "POST":
		json.NewDecoder(r.Body).Decode(&f.created)
		tag := f.created["tag_name"].(string)
		if _, ok := f.tags[tag]; ok {
			reply(409, map[string]string{"message": "Release already exists"})
			return
		}
		f.tags[tag] = f.created["ref"].(string)
		reply(201, map[string]any{"tag_name": tag, "_links": map[string]string{"self": "https://gl/grp/sub/web/-/releases/" + tag}})
	case strings.HasPrefix(p, proj+"/repository/commits/"):
		sha := strings.TrimPrefix(p, proj+"/repository/commits/")
		if !strings.HasPrefix(glSHA, sha) {
			reply(404, map[string]string{"message": "404 Commit Not Found"})
			return
		}
		reply(200, map[string]string{"id": glSHA})
	case strings.HasPrefix(p, proj+"/repository/tags/"):
		tag, _ := url.PathUnescape(strings.TrimPrefix(p, proj+"/repository/tags/"))
		if _, ok := f.tags[tag]; ok {
			reply(200, map[string]string{"name": tag})
			return
		}
		reply(404, map[string]string{"message": "404 Tag Not Found"})
	default:
		reply(404, map[string]string{"message": "404 Project Not Found"})
	}
}

func TestGitLabFlow(t *testing.T) {
	fake := &fakeGitLab{tags: map[string]string{"back@1.17.0": "1717171", "back@2.0.0-rc.1": "2020202", "front@9.0.0": "9999999"}}
	gls := httptest.NewServer(fake)
	defer gls.Close()

	dir := t.TempDir()
	a, _ := newAuth(filepath.Join(dir, "auth.json"))
	if _, err := a.createSuperuser("", "a@b.co", "0123456789"); err != nil {
		t.Fatal(err)
	}
	a.tokens, _ = loadTokens(filepath.Join(dir, "tokens.json"))
	gh, _ := newGitHub(dir, "http://127.0.0.1:1") // GitHub não configurado
	gl, err := newGitLab(dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	githubRoutes(mux, gh)
	gitlabRoutes(mux, gl, a)
	repoRoutes(mux, providers{links: gh, gh: gh, gl: gl}, func(_ context.Context, name string) (bool, error) { return name == "web", nil })
	h := sameOrigin(a.middleware(mux))
	cookie := &http.Cookie{Name: sessionCookie, Value: a.sign(time.Now().Add(time.Hour))}
	other := &http.Cookie{Name: sessionCookie, Value: a.sign(time.Now().Add(2 * time.Hour))}
	do := func(method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Dokk", "1")
		r.Header.Set("X-Dokk-Lang", "en")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "ops.example.com")
		if c != nil {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	jsonOf := func(w *httptest.ResponseRecorder) map[string]any {
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	loc := func(w *httptest.ResponseRecorder) string { return w.Header().Get("Location") }

	// Antes de configurar: o passo a passo já sai com os valores a copiar.
	st := jsonOf(do("GET", "/api/gitlab", "", cookie))
	if st["url"] != "https://gitlab.com" || st["hasApp"] != false || st["connected"] != false || st["redirectUri"] != "https://ops.example.com/gitlab/callback" ||
		st["applicationsUrl"] != "https://gitlab.com/-/user_settings/applications" || st["appName"] != "dokk-ops-example-com" || st["scopes"] != "api" {
		t.Fatalf("estado inicial = %v", st)
	}
	if c := do("POST", "/api/gitlab/connect", "", cookie).Code; c != 400 {
		t.Errorf("connect sem Application = %d", c)
	}
	for _, body := range []string{`{"url":"ftp://x","clientId":"a","clientSecret":"b"}`, `{"url":"http://gitlab.evil.com","clientId":"a","clientSecret":"b"}`, `{"clientId":"","clientSecret":"b"}`, `{"clientId":"a"}`} {
		if c := do("PUT", "/api/gitlab", body, cookie).Code; c != 400 {
			t.Errorf("PUT %s = %d", body, c)
		}
	}
	w := do("PUT", "/api/gitlab", `{"url":"`+gls.URL+`/","clientId":"app-id","clientSecret":"app-secret"}`, cookie)
	if st := jsonOf(w); w.Code != 200 || st["hasApp"] != true || st["url"] != gls.URL || strings.Contains(w.Body.String(), "app-secret") {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	if c := do("POST", "/api/gitlab/connect", "", nil).Code; c != 401 {
		t.Errorf("connect sem cookie = %d", c)
	}

	connect := func() url.Values {
		w := do("POST", "/api/gitlab/connect", "", cookie)
		u, err := url.Parse(jsonOf(w)["url"].(string))
		if err != nil || w.Code != 200 || !strings.HasPrefix(u.String(), gls.URL+"/oauth/authorize?") {
			t.Fatalf("connect = %d %s", w.Code, w.Body)
		}
		q := u.Query()
		if q.Get("client_id") != "app-id" || q.Get("redirect_uri") != "https://ops.example.com/gitlab/callback" || q.Get("scope") != "api" ||
			q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
			t.Fatalf("authorize = %v", q)
		}
		fake.mu.Lock()
		fake.challenge = q.Get("code_challenge")
		fake.mu.Unlock()
		return q
	}
	cb := func(code, state string, c *http.Cookie) string {
		return loc(do("GET", "/gitlab/callback?code="+code+"&state="+state, "", c))
	}
	q := connect()
	if l := cb("good", q.Get("state"), nil); l != "/login" {
		t.Errorf("callback sem cookie = %s", l)
	}
	if l := cb("good", "nope", cookie); l != "/settings?gitlab=state" {
		t.Errorf("state desconhecido = %s", l)
	}
	if l := cb("good", q.Get("state"), other); l != "/settings?gitlab=state" {
		t.Errorf("state de outra sessão = %s", l)
	}
	q = connect()
	now := time.Now()
	gl.now = func() time.Time { return now.Add(11 * time.Minute) }
	if l := cb("good", q.Get("state"), cookie); l != "/settings?gitlab=state" {
		t.Errorf("state expirado = %s", l)
	}
	gl.now = time.Now
	q = connect()
	if l := cb("bad", q.Get("state"), cookie); l != "/settings?gitlab=error" || gl.configured() {
		t.Errorf("code inválido = %s", l)
	}
	q = connect()
	if l := loc(do("GET", "/gitlab/callback?error=access_denied&state="+q.Get("state"), "", cookie)); l != "/settings?gitlab=error" {
		t.Errorf("negado = %s", l)
	}

	// Caminho feliz; reusar o state falha.
	q = connect()
	if l := cb("good", q.Get("state"), cookie); l != "/settings?gitlab=connected" {
		t.Fatalf("callback = %s", l)
	}
	if l := cb("good", q.Get("state"), cookie); l != "/settings?gitlab=state" {
		t.Errorf("state reusado = %s", l)
	}
	if st := jsonOf(do("GET", "/api/gitlab", "", cookie)); st["connected"] != true || st["username"] != "edu" || strings.Contains(fmt.Sprint(st), "at1") {
		t.Fatalf("conectado = %v", st)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "gitlab.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("perm gitlab.json = %v", fi.Mode())
	}

	// Lista de repos paginada, com o provedor.
	w = do("GET", "/api/repos", "", cookie)
	var repos []repoJSON
	json.Unmarshal(w.Body.Bytes(), &repos)
	if w.Code != 200 || len(repos) != 150 || repos[0] != (repoJSON{"gitlab", "grp/p000", false}) || !repos[1].Private || repos[149].FullName != "grp/p149" {
		t.Fatalf("repos = %d %d %s", w.Code, len(repos), w.Body.String()[:min(200, w.Body.Len())])
	}
	if w := do("GET", "/api/repos?provider=github", "", cookie); w.Body.String() != "[]\n" {
		t.Errorf("repos github = %s", w.Body)
	}

	// Vínculo com o GitLab: aceita a URL do projeto e grupos aninhados.
	if c := do("PUT", "/api/apps/web/repo", `{"provider":"bitbucket","repo":"a/b"}`, cookie).Code; c != 400 {
		t.Errorf("provedor inválido = %d", c)
	}
	if c := do("PUT", "/api/apps/web/repo", `{"provider":"gitlab","repo":"semgrupo"}`, cookie).Code; c != 400 {
		t.Errorf("repo sem grupo = %d", c)
	}
	w = do("PUT", "/api/apps/web/repo", `{"provider":"gitlab","repo":"`+gls.URL+`/grp/sub/web.git","tagFormat":"back@{version}"}`, cookie)
	if w.Body.String() != `{"installed":true,"provider":"gitlab","repo":"grp/sub/web","tagFormat":"back@{version}"}`+"\n" {
		t.Fatalf("vincular = %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "github-apps.json")); !strings.Contains(string(b), `"provider": "gitlab"`) {
		t.Errorf("github-apps.json = %s", b)
	}
	// A rota antiga lê o mesmo vínculo.
	if w := do("GET", "/api/apps/web/github", "", cookie); !strings.Contains(w.Body.String(), `"provider":"gitlab"`) {
		t.Errorf("alias = %s", w.Body)
	}

	w = do("GET", "/api/apps/web/repo/releases", "", cookie)
	var rels []releaseJSON
	json.Unmarshal(w.Body.Bytes(), &rels)
	if w.Code != 200 || len(rels) != 102 || rels[0].Tag != "back@2.0.0-rc.1" || !rels[0].Prerelease || rels[0].SHA != "2020202" ||
		rels[1].Tag != "back@1.17.0" || rels[1].URL != "https://gl/grp/sub/web/-/releases/back@1.17.0" || rels[1].Name != "Back 1.17" {
		t.Fatalf("releases = %d %d %+v", w.Code, len(rels), rels[:min(2, len(rels))])
	}

	post := func(body string) *httptest.ResponseRecorder {
		return do("POST", "/api/apps/web/repo/releases", body, cookie)
	}
	if c := post(`{"tag":"back@1.17.0","sha":"abcdef0","image":"registry.gitlab.com/grp/sub/web:1.17.0"}`).Code; c != 409 {
		t.Errorf("tag existente = %d", c)
	}
	if c := post(`{"tag":"back@3.0.0","sha":"9999999","image":"x:3"}`).Code; c != 422 {
		t.Errorf("sha desconhecido = %d", c)
	}
	if c := post(`{"tag":"v3.0.0","sha":"abcdef0","image":"x:3"}`).Code; c != 400 {
		t.Errorf("fora do formato = %d", c)
	}
	w = post(`{"tag":"back@2.0.0","sha":"abcdef0","image":"registry.gitlab.com/grp/sub/web:2.0.0","notes":"Hotfix"}`)
	if w.Body.String() != `{"provider":"gitlab","tag":"back@2.0.0","url":"https://gl/grp/sub/web/-/releases/back@2.0.0"}`+"\n" {
		t.Fatalf("criar = %d %s", w.Code, w.Body)
	}
	if c := fake.created; c["ref"] != glSHA || c["name"] != "back@2.0.0" || !strings.Contains(c["description"].(string), "Hotfix") || !strings.Contains(c["description"].(string), "registry.gitlab.com/grp/sub/web:2.0.0") {
		t.Errorf("corpo = %v", c)
	}
	// Token vencido: um refresh só, mesmo com pedidos simultâneos.
	gl.now = func() time.Time { return now.Add(3 * time.Hour) }
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = do("GET", "/api/apps/web/repo/releases", "", cookie).Code
		}()
	}
	wg.Wait()
	if fake.refreshes != 1 || fmt.Sprint(codes) != "[200 200 200 200 200]" {
		t.Fatalf("refresh = %d %v", fake.refreshes, codes)
	}
	gl.now = time.Now
	// Token revogado do lado do GitLab (401 com token "válido"): renova e repete.
	fake.mu.Lock()
	fake.access = "rotated"
	fake.mu.Unlock()
	if c := do("GET", "/api/apps/web/repo", "", cookie); !strings.Contains(c.Body.String(), `"installed":true`) || fake.refreshes != 2 {
		t.Errorf("401 → refresh = %s %d", c.Body, fake.refreshes)
	}
	// Recarregar do disco mantém a conexão.
	if g2, _ := newGitLab(dir); !g2.configured() || g2.cfg.Username != "edu" || g2.cfg.RefreshToken != fake.refresh {
		t.Errorf("recarregar = %+v", g2.cfg)
	}

	// Desconectar revoga os dois tokens e esquece.
	at, rt := fake.access, fake.refresh
	if st := jsonOf(do("DELETE", "/api/gitlab/connection", "", cookie)); st["connected"] != false || st["hasApp"] != true {
		t.Fatalf("desconectar = %v", st)
	}
	if fmt.Sprint(fake.revoked) != fmt.Sprint([]string{at, rt}) {
		t.Errorf("revogados = %v", fake.revoked)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "gitlab.json")); strings.Contains(string(b), at) || strings.Contains(string(b), rt) {
		t.Errorf("gitlab.json = %s", b)
	}
	if w := do("GET", "/api/apps/web/repo/releases", "", cookie); w.Code != 400 || !strings.Contains(w.Body.String(), "GitLab is not connected") {
		t.Errorf("releases desconectado = %d %s", w.Code, w.Body)
	}
	// Trocar a Application zera tudo; o secret vazio só vale para a mesma.
	if c := do("PUT", "/api/gitlab", `{"url":"`+gls.URL+`","clientId":"outra"}`, cookie).Code; c != 400 {
		t.Errorf("trocar Application sem secret = %d", c)
	}
	if c := do("PUT", "/api/gitlab", `{"url":"`+gls.URL+`","clientId":"app-id"}`, cookie).Code; c != 200 {
		t.Errorf("manter secret = %d", c)
	}
}

// A tag criada por outro entre a checagem e o POST vira 409.
func TestGitLabCreateRace(t *testing.T) {
	fake := &fakeGitLab{tags: map[string]string{}, access: "at", refresh: "rt"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/releases") {
			w.WriteHeader(409)
			w.Write([]byte(`{"message":"Release already exists"}`))
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	gl, _ := newGitLab(t.TempDir())
	gl.cfg = gitlabConfig{URL: srv.URL, ClientID: "app-id", ClientSecret: "app-secret", AccessToken: "at", RefreshToken: "rt"}
	if _, err := gl.createRelease(context.Background(), "grp/sub/web", "v1.0.0", "abcdef0", "x:1", "", false); err != errTagExists {
		t.Errorf("corrida = %v", err)
	}
}

func TestNormalizeGitLabURL(t *testing.T) {
	for in, want := range map[string]string{"": "https://gitlab.com", "gitlab.acme.io": "https://gitlab.acme.io", "https://git.acme.io/gitlab/": "https://git.acme.io/gitlab", "http://127.0.0.1:8929": "http://127.0.0.1:8929"} {
		if got, ok := normalizeGitLabURL(in); !ok || got != want {
			t.Errorf("%q = %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"http://gitlab.acme.io", "ftp://x", "https://u:p@gitlab.com", "https://gitlab.com/?a=1"} {
		if _, ok := normalizeGitLabURL(in); ok {
			t.Errorf("%q aceito", in)
		}
	}
}
