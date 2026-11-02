package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Integração com o GitLab (gitlab.com ou self-managed). O GitLab não tem App
// Manifest: o usuário cria uma OAuth Application na instância (único passo
// manual) e cola Application ID + Secret. "Conectar" faz o authorization
// code com PKCE + state; o callback grava access + refresh token em
// <data>/gitlab.json (0600), e o refresh é automático quando expira.

const defaultGitLabURL = "https://gitlab.com"

var gitlabRepoRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)+$`)

type gitlabConfig struct {
	URL          string    `json:"url"`
	ClientID     string    `json:"clientId,omitempty"`
	ClientSecret string    `json:"clientSecret,omitempty"`
	RedirectURI  string    `json:"redirectUri,omitempty"` // a usada no connect (o refresh pede a mesma)
	AccessToken  string    `json:"accessToken,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt,omitzero"`
	Username     string    `json:"username,omitempty"`
}

type gitlab struct {
	dir  string
	http *http.Client
	now  func() time.Time

	mu  sync.Mutex
	cfg gitlabConfig
	// refreshMu serializa o refresh: dois pedidos com o token vencido não
	// gastam o mesmo refresh token (o GitLab o invalida no primeiro uso).
	refreshMu sync.Mutex
}

// glError é uma resposta de erro do GitLab.
type glError struct {
	Status  int
	Message string
}

func (e *glError) Error() string { return fmt.Sprintf("GitLab %d: %s", e.Status, e.Message) }

func glStatus(err error) int {
	var e *glError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

var errGitLabNotConnected = errors.New("GitLab não conectado")

func newGitLab(dir string) (*gitlab, error) {
	g := &gitlab{dir: dir, http: &http.Client{Timeout: 20 * time.Second}, now: time.Now}
	if err := readJSON(filepath.Join(dir, "gitlab.json"), &g.cfg); err != nil {
		return nil, err
	}
	if g.cfg.URL == "" {
		g.cfg.URL = defaultGitLabURL
	}
	return g, nil
}

func (g *gitlab) save(cfg gitlabConfig) error {
	if err := writeJSONFile(filepath.Join(g.dir, "gitlab.json"), cfg); err != nil {
		return err
	}
	g.cfg = cfg
	return nil
}

func (g *gitlab) clientID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.ClientID
}

func (g *gitlab) baseURL() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.URL
}

// hasApp: a OAuth Application foi informada (dá para conectar).
func (g *gitlab) hasApp() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.ClientID != "" && g.cfg.ClientSecret != ""
}

// configured: conectado (há token). É o que libera repos e releases.
func (g *gitlab) configured() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.RefreshToken != "" || g.cfg.AccessToken != ""
}

// normalizeGitLabURL aceita "gitlab.exemplo.com" ou a URL completa; só https
// (http apenas em loopback, para testes e instâncias locais).
func normalizeGitLabURL(s string) (string, bool) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return defaultGitLabURL, true
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", false
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"):
	default:
		return "", false
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), true
}

func (g *gitlab) publicJSON(origin string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]any{
		"url":             g.cfg.URL,
		"clientId":        g.cfg.ClientID,
		"hasApp":          g.cfg.ClientID != "" && g.cfg.ClientSecret != "",
		"connected":       g.cfg.RefreshToken != "" || g.cfg.AccessToken != "",
		"username":        g.cfg.Username,
		"applicationsUrl": g.cfg.URL + "/-/user_settings/applications",
		"appName":         gitlabAppName(origin),
		"redirectUri":     origin + "/gitlab/callback",
		"scopes":          "api",
	}
}

// gitlabAppName: "dokk-<host>" (o nome é só um rótulo no GitLab).
func gitlabAppName(origin string) string {
	n := appName(origin)
	return n[:strings.LastIndex(n, "-")]
}

// setApp grava URL + Application; secret vazio mantém o atual. Trocar a
// URL ou a Application desconecta (o token é de outra Application).
func (g *gitlab) setApp(base, clientID, secret string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg := g.cfg
	if secret == "" {
		secret = cfg.ClientSecret
	}
	if base != cfg.URL || clientID != cfg.ClientID {
		cfg = gitlabConfig{}
	}
	cfg.URL, cfg.ClientID, cfg.ClientSecret = base, clientID, secret
	return g.save(cfg)
}

// form faz um POST de formulário num endpoint OAuth do GitLab.
func (g *gitlab) form(ctx context.Context, path string, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", g.baseURL()+path, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return &glError{res.StatusCode, glMessage(b, res.StatusCode)}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// glMessage tira a mensagem do corpo de erro ({"message": "..."|{...}} ou
// {"error": "...", "error_description": "..."}).
func glMessage(b []byte, status int) string {
	var e struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	json.Unmarshal(b, &e)
	switch m := e.Message.(type) {
	case string:
		if m != "" {
			return m
		}
	case nil:
	default:
		j, _ := json.Marshal(m)
		return string(j)
	}
	if e.Desc != "" {
		return e.Desc
	}
	if e.Error != "" {
		return e.Error
	}
	return http.StatusText(status)
}

type glTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (g *gitlab) applyToken(cfg *gitlabConfig, t glTokenResponse) {
	cfg.AccessToken, cfg.RefreshToken = t.AccessToken, t.RefreshToken
	cfg.ExpiresAt = time.Time{}
	if t.ExpiresIn > 0 {
		cfg.ExpiresAt = g.now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
}

// exchange troca o code (com o verifier do PKCE) pelos tokens e grava.
func (g *gitlab) exchange(ctx context.Context, code, verifier, redirect string) error {
	g.mu.Lock()
	id, secret := g.cfg.ClientID, g.cfg.ClientSecret
	g.mu.Unlock()
	var t glTokenResponse
	err := g.form(ctx, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {id}, "client_secret": {secret},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier},
	}, &t)
	if err != nil {
		return err
	}
	if t.AccessToken == "" {
		return errors.New("resposta de token sem access_token")
	}
	g.mu.Lock()
	cfg := g.cfg
	cfg.RedirectURI = redirect
	g.applyToken(&cfg, t)
	err = g.save(cfg)
	g.mu.Unlock()
	if err != nil {
		return err
	}
	var user struct {
		Username string `json:"username"`
	}
	if g.call(ctx, "GET", "/user", nil, &user) == nil && user.Username != "" {
		g.mu.Lock()
		cfg := g.cfg
		cfg.Username = user.Username
		g.save(cfg)
		g.mu.Unlock()
	}
	return nil
}

// token devolve um access token válido, renovando se vence em menos de 1
// min (ou se force, depois de um 401).
func (g *gitlab) token(ctx context.Context, force bool, stale string) (string, error) {
	g.mu.Lock()
	cfg := g.cfg
	g.mu.Unlock()
	if cfg.AccessToken == "" && cfg.RefreshToken == "" {
		return "", errGitLabNotConnected
	}
	fresh := cfg.ExpiresAt.IsZero() || g.now().Before(cfg.ExpiresAt.Add(-time.Minute))
	if fresh && !force {
		return cfg.AccessToken, nil
	}
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()
	// Outro pedido pode ter renovado enquanto este esperava o lock.
	g.mu.Lock()
	cfg = g.cfg
	g.mu.Unlock()
	if cfg.AccessToken != stale && cfg.AccessToken != "" && (cfg.ExpiresAt.IsZero() || g.now().Before(cfg.ExpiresAt.Add(-time.Minute))) {
		return cfg.AccessToken, nil
	}
	if cfg.RefreshToken == "" {
		return "", errGitLabNotConnected
	}
	var t glTokenResponse
	err := g.form(ctx, "/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {cfg.RefreshToken},
		"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "redirect_uri": {cfg.RedirectURI},
	}, &t)
	if err != nil {
		return "", err
	}
	if t.AccessToken == "" {
		return "", errors.New("resposta de refresh sem access_token")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	next := g.cfg
	if next.RefreshToken != cfg.RefreshToken { // desconectado/reconfigurado no meio
		return "", errGitLabNotConnected
	}
	if t.RefreshToken == "" {
		t.RefreshToken = cfg.RefreshToken
	}
	g.applyToken(&next, t)
	if err := g.save(next); err != nil {
		return "", err
	}
	return next.AccessToken, nil
}

// disconnect revoga os tokens no GitLab (melhor esforço) e esquece.
func (g *gitlab) disconnect(ctx context.Context) error {
	g.mu.Lock()
	cfg := g.cfg
	g.mu.Unlock()
	for _, tok := range []string{cfg.AccessToken, cfg.RefreshToken} {
		if tok != "" {
			g.form(ctx, "/oauth/revoke", url.Values{"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "token": {tok}}, nil)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	next := g.cfg
	next.AccessToken, next.RefreshToken, next.ExpiresAt, next.Username, next.RedirectURI = "", "", time.Time{}, "", ""
	return g.save(next)
}

// call chama a API v4 (path relativo a /api/v4). Num 401 renova o token e
// tenta de novo uma vez.
func (g *gitlab) call(ctx context.Context, method, path string, in, out any) error {
	tok, err := g.token(ctx, false, "")
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err := g.do(ctx, method, path, tok, in, out)
		if glStatus(err) != http.StatusUnauthorized || attempt > 0 {
			return err
		}
		if tok, err = g.token(ctx, true, tok); err != nil {
			return err
		}
	}
}

func (g *gitlab) do(ctx context.Context, method, path, tok string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL()+"/api/v4"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return &glError{res.StatusCode, glMessage(b, res.StatusCode)}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// project é o id do projeto na URL (o caminho com "/" codificada).
func project(repo string) string { return "/projects/" + url.PathEscape(repo) }

// access confere que o projeto existe e é visível com o token.
func (g *gitlab) access(ctx context.Context, repo string) error {
	return g.call(ctx, "GET", project(repo), nil, nil)
}

// repos lista os projetos em que o usuário é ao menos Developer (pode criar
// tags e releases).
func (g *gitlab) repos(ctx context.Context) ([]ghRepo, error) {
	out := []ghRepo{}
	for page := 1; page <= maxPages; page++ {
		var list []struct {
			Path       string `json:"path_with_namespace"`
			Visibility string `json:"visibility"`
		}
		if err := g.call(ctx, "GET", fmt.Sprintf("/projects?membership=true&min_access_level=30&simple=true&order_by=path&sort=asc&per_page=100&page=%d", page), nil, &list); err != nil {
			return nil, err
		}
		for _, p := range list {
			out = append(out, ghRepo{FullName: p.Path, Private: p.Visibility != "public"})
		}
		if len(list) < 100 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].FullName) < strings.ToLower(out[j].FullName) })
	return out, nil
}

// releases: as tags do formato (maior versão primeiro) com os dados da
// Release do GitLab quando existe.
func (g *gitlab) releases(ctx context.Context, repo, format string) ([]releaseJSON, error) {
	prefix, _, _ := strings.Cut(format, "{version}")
	byTag := map[string]*releaseJSON{}
	for page := 1; page <= maxPages; page++ {
		var tags []struct {
			Name   string `json:"name"`
			Commit struct {
				ID        string    `json:"id"`
				CreatedAt time.Time `json:"created_at"`
			} `json:"commit"`
		}
		q := fmt.Sprintf("?per_page=100&page=%d", page)
		if prefix != "" {
			q += "&search=" + url.QueryEscape("^"+prefix)
		}
		if err := g.call(ctx, "GET", project(repo)+"/repository/tags"+q, nil, &tags); err != nil {
			return nil, err
		}
		for _, t := range tags {
			v, ok := versionOf(format, t.Name)
			if !ok {
				continue
			}
			byTag[t.Name] = &releaseJSON{Tag: t.Name, Version: v, Name: t.Name, SHA: t.Commit.ID, CreatedAt: t.Commit.CreatedAt, Prerelease: strings.Contains(v, "-")}
		}
		if len(tags) < 100 {
			break
		}
	}
	for page := 1; page <= maxPages; page++ {
		var rels []struct {
			TagName   string    `json:"tag_name"`
			Name      string    `json:"name"`
			CreatedAt time.Time `json:"created_at"`
			Links     struct {
				Self string `json:"self"`
			} `json:"_links"`
		}
		if err := g.call(ctx, "GET", fmt.Sprintf("%s/releases?per_page=100&page=%d", project(repo), page), nil, &rels); err != nil {
			return nil, err
		}
		for _, r := range rels {
			if rel, ok := byTag[r.TagName]; ok {
				rel.Name, rel.URL, rel.CreatedAt = r.Name, r.Links.Self, r.CreatedAt
			}
		}
		if len(rels) < 100 {
			break
		}
	}
	out := make([]releaseJSON, 0, len(byTag))
	for _, r := range byTag {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out, nil
}

// createRelease cria tag + Release numa chamada (POST /releases com ref =
// sha). O GitLab não marca prerelease; a versão já diz (tem "-").
func (g *gitlab) createRelease(ctx context.Context, repo, tag, sha, image, notes string, _ bool) (string, error) {
	var commit struct {
		ID string `json:"id"`
	}
	if err := g.call(ctx, "GET", project(repo)+"/repository/commits/"+url.PathEscape(sha), nil, &commit); err != nil {
		if s := glStatus(err); s == 404 || s == 422 {
			return "", errSHAUnknown
		}
		return "", err
	}
	err := g.call(ctx, "GET", project(repo)+"/repository/tags/"+url.PathEscape(tag), nil, nil)
	if err == nil {
		return "", errTagExists
	}
	if glStatus(err) != 404 {
		return "", err
	}
	body := "Image: `" + image + "`"
	if notes = strings.TrimSpace(notes); notes != "" {
		body = notes + "\n\n" + body
	}
	var rel struct {
		Links struct {
			Self string `json:"self"`
		} `json:"_links"`
	}
	in := map[string]any{"tag_name": tag, "ref": commit.ID, "name": tag, "description": body}
	if err := g.call(ctx, "POST", project(repo)+"/releases", in, &rel); err != nil {
		if glStatus(err) == 409 {
			return "", errTagExists // corrida: alguém criou a tag/release entre as chamadas
		}
		return "", err
	}
	return rel.Links.Self, nil
}

// --- fluxo OAuth (connect / callback) ---

type gitlabState struct {
	session  string
	verifier string
	redirect string
	exp      time.Time
}

type gitlabFlow struct {
	g      *gitlab
	mu     sync.Mutex
	states map[string]gitlabState
}

func (f *gitlabFlow) newState(r *http.Request, verifier, redirect string) string {
	s := hex.EncodeToString(randomBytes(20))
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.g.now()
	for k, v := range f.states {
		if now.After(v.exp) {
			delete(f.states, k)
		}
	}
	f.states[s] = gitlabState{sessionHash(r), verifier, redirect, now.Add(manifestStateTTL)}
	return s
}

// takeState consome o state (uso único) e confere sessão e validade.
func (f *gitlabFlow) takeState(r *http.Request, s string) (gitlabState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[s]
	delete(f.states, s)
	return st, ok && st.session != "" && st.session == sessionHash(r) && !f.g.now().After(st.exp)
}

func gitlabRoutes(mux *http.ServeMux, g *gitlab, a *auth) {
	f := &gitlabFlow{g: g, states: map[string]gitlabState{}}

	mux.HandleFunc("GET /api/gitlab", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, g.publicJSON(publicOrigin(r)))
	})
	mux.HandleFunc("PUT /api/gitlab", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL          string `json:"url"`
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		base, ok := normalizeGitLabURL(body.URL)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "err.invalidGitLabURL")
			return
		}
		id, secret := strings.TrimSpace(body.ClientID), strings.TrimSpace(body.ClientSecret)
		if id == "" || len(id) > 200 || strings.ContainsAny(id, " \t\n") || len(secret) > 500 || strings.ContainsAny(secret, " \t\n") {
			writeError(w, r, http.StatusBadRequest, "err.invalidGitLabApp")
			return
		}
		if secret == "" && (!g.hasApp() || id != g.clientID()) { // secret vazio só mantém o da mesma Application
			writeError(w, r, http.StatusBadRequest, "err.invalidGitLabApp")
			return
		}
		if err := g.setApp(base, id, secret); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, g.publicJSON(publicOrigin(r)))
	})

	// Devolve a URL de autorização; o navegador vai para lá.
	mux.HandleFunc("POST /api/gitlab/connect", func(w http.ResponseWriter, r *http.Request) {
		if _, has := bearer(r); has || !a.cookieLoggedIn(r) {
			writeError(w, r, http.StatusForbidden, "err.cookieRequired")
			return
		}
		if !g.hasApp() {
			writeError(w, r, http.StatusBadRequest, "err.gitlabNoApp")
			return
		}
		verifier := base64.RawURLEncoding.EncodeToString(randomBytes(32))
		sum := sha256.Sum256([]byte(verifier))
		redirect := publicOrigin(r) + "/gitlab/callback"
		state := f.newState(r, verifier, redirect)
		g.mu.Lock()
		id, base := g.cfg.ClientID, g.cfg.URL
		g.mu.Unlock()
		q := url.Values{
			"client_id": {id}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {"api"},
			"state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": base + "/oauth/authorize?" + q.Encode()})
	})

	mux.HandleFunc("GET /gitlab/callback", func(w http.ResponseWriter, r *http.Request) {
		if !a.cookieLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		q := r.URL.Query()
		st, ok := f.takeState(r, q.Get("state"))
		switch {
		case !ok:
			http.Redirect(w, r, "/settings?gitlab=state", http.StatusFound)
			return
		case q.Get("code") == "": // negado no GitLab (error=access_denied)
			http.Redirect(w, r, "/settings?gitlab=error", http.StatusFound)
			return
		}
		if err := g.exchange(r.Context(), q.Get("code"), st.verifier, st.redirect); err != nil {
			http.Redirect(w, r, "/settings?gitlab=error", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/settings?gitlab=connected", http.StatusFound)
	})

	mux.HandleFunc("DELETE /api/gitlab/connection", func(w http.ResponseWriter, r *http.Request) {
		if err := g.disconnect(r.Context()); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, g.publicJSON(publicOrigin(r)))
	})

	mux.HandleFunc("GET /api/gitlab/repos", func(w http.ResponseWriter, r *http.Request) {
		if !g.configured() {
			writeError(w, r, http.StatusBadRequest, "err.gitlabNotConnected")
			return
		}
		list, err := g.repos(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}
