package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Integração com uma GitHub App: o dokk assina um JWT (RS256) com a chave
// privada da App, troca por um token de instalação do repositório vinculado à
// app do Dokku e cria tags + GitHub Releases. Config em <data>/github.json e
// vínculos app→repo em <data>/github-apps.json (ambos 0600).

const defaultGitHubAPI = "https://api.github.com"

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	slugRe   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62})$`)
	shaRe    = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
	tagRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,127}$`)
	imageMax = 512
)

type githubConfig struct {
	AppID      int64  `json:"appId"`
	Slug       string `json:"slug"`
	PrivateKey string `json:"privateKey"`
	// Vêm do manifest flow (não são usados hoje, mas o GitHub só mostra uma vez).
	ClientID      string `json:"clientId,omitempty"`
	ClientSecret  string `json:"clientSecret,omitempty"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

type cachedToken struct {
	token string
	exp   time.Time
}

type github struct {
	dir     string
	apiBase string
	http    *http.Client
	now     func() time.Time

	mu     sync.Mutex
	cfg    githubConfig
	key    *rsa.PrivateKey
	links  map[string]string // app → owner/repo
	tokens map[string]cachedToken
}

// ghError é uma resposta de erro da API do GitHub.
type ghError struct {
	Status  int
	Message string
}

func (e *ghError) Error() string { return fmt.Sprintf("GitHub %d: %s", e.Status, e.Message) }

func ghStatus(err error) int {
	var e *ghError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func newGitHub(dir, apiBase string) (*github, error) {
	if apiBase == "" {
		apiBase = defaultGitHubAPI
	}
	g := &github{dir: dir, apiBase: strings.TrimRight(apiBase, "/"), http: &http.Client{Timeout: 20 * time.Second}, now: time.Now, links: map[string]string{}, tokens: map[string]cachedToken{}}
	if err := readJSON(filepath.Join(dir, "github.json"), &g.cfg); err != nil {
		return nil, err
	}
	if g.cfg.PrivateKey != "" {
		k, err := parseRSAKey(g.cfg.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("github.json: %w", err)
		}
		g.key = k
	}
	if err := readJSON(filepath.Join(dir, "github-apps.json"), &g.links); err != nil {
		return nil, err
	}
	if g.links == nil {
		g.links = map[string]string{}
	}
	return g, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSONFile grava de forma atômica (temporário + rename), 0600.
func writeJSONFile(path string, v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseRSAKey(p string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(p)))
	if block == nil {
		return nil, errors.New("PEM inválido")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("a chave não é RSA")
	}
	return rk, nil
}

func (g *github) configured() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.key != nil && g.cfg.AppID > 0
}

func (g *github) publicJSON() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	install := ""
	if g.cfg.Slug != "" {
		install = "https://github.com/apps/" + g.cfg.Slug + "/installations/new"
	}
	return map[string]any{"configured": g.key != nil && g.cfg.AppID > 0, "appId": g.cfg.AppID, "slug": g.cfg.Slug, "installUrl": install}
}

// setConfig troca a config; privateKey vazio mantém a chave atual.
func (g *github) setConfig(appID int64, slug, privateKey string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg := githubConfig{AppID: appID, Slug: slug, PrivateKey: g.cfg.PrivateKey}
	key := g.key
	if privateKey != "" {
		k, err := parseRSAKey(privateKey)
		if err != nil {
			return err
		}
		cfg.PrivateKey, key = strings.TrimSpace(privateKey)+"\n", k
	}
	if key == nil {
		return errors.New("sem chave")
	}
	if err := writeJSONFile(filepath.Join(g.dir, "github.json"), cfg); err != nil {
		return err
	}
	g.cfg, g.key = cfg, key
	g.tokens = map[string]cachedToken{}
	return nil
}

func (g *github) link(app string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.links[app]
}

func (g *github) setLink(app, repo string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := map[string]string{}
	for k, v := range g.links {
		next[k] = v
	}
	if repo == "" {
		delete(next, app)
	} else {
		next[app] = repo
	}
	if err := writeJSONFile(filepath.Join(g.dir, "github-apps.json"), next); err != nil {
		return err
	}
	g.links = next
	return nil
}

// appJWT assina o JWT da App (vale 9 min; iat 60 s no passado por relógio torto).
func (g *github) appJWT() (string, error) {
	g.mu.Lock()
	key, id := g.key, g.cfg.AppID
	g.mu.Unlock()
	if key == nil || id <= 0 {
		return "", errors.New("GitHub App não configurada")
	}
	now := g.now()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(id, 10)})
	signing := header + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// call faz uma requisição à API do GitHub; out pode ser nil.
func (g *github) call(ctx context.Context, method, path, auth string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.apiBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", auth)
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
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &e)
		if e.Message == "" {
			e.Message = http.StatusText(res.StatusCode)
		}
		return &ghError{res.StatusCode, e.Message}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// installationToken devolve um token de instalação para o repo (com cache
// até 1 min antes de expirar). 404 do GitHub = App não instalada no repo.
func (g *github) installationToken(ctx context.Context, repo string) (string, error) {
	g.mu.Lock()
	c, ok := g.tokens[repo]
	g.mu.Unlock()
	if ok && g.now().Before(c.exp.Add(-time.Minute)) {
		return c.token, nil
	}
	jwt, err := g.appJWT()
	if err != nil {
		return "", err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := g.call(ctx, "GET", "/repos/"+repo+"/installation", "Bearer "+jwt, nil, &inst); err != nil {
		return "", err
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := g.call(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), "Bearer "+jwt, nil, &tok); err != nil {
		return "", err
	}
	g.mu.Lock()
	g.tokens[repo] = cachedToken{tok.Token, tok.ExpiresAt}
	g.mu.Unlock()
	return tok.Token, nil
}

type releaseJSON struct {
	Tag        string    `json:"tag"`
	Name       string    `json:"name"`
	SHA        string    `json:"sha"`
	URL        string    `json:"url"`
	CreatedAt  time.Time `json:"createdAt"`
	Prerelease bool      `json:"prerelease"`
}

// releases lista as releases (mais recente primeiro), com o sha da tag.
func (g *github) releases(ctx context.Context, repo string) ([]releaseJSON, error) {
	tok, err := g.installationToken(ctx, repo)
	if err != nil {
		return nil, err
	}
	var rels []struct {
		TagName    string    `json:"tag_name"`
		Name       string    `json:"name"`
		HTMLURL    string    `json:"html_url"`
		CreatedAt  time.Time `json:"created_at"`
		Prerelease bool      `json:"prerelease"`
		Draft      bool      `json:"draft"`
	}
	if err := g.call(ctx, "GET", "/repos/"+repo+"/releases?per_page=50", "Bearer "+tok, nil, &rels); err != nil {
		return nil, err
	}
	var tags []struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.call(ctx, "GET", "/repos/"+repo+"/tags?per_page=100", "Bearer "+tok, nil, &tags); err != nil {
		return nil, err
	}
	shas := map[string]string{}
	for _, t := range tags {
		shas[t.Name] = t.Commit.SHA
	}
	out := []releaseJSON{}
	for _, r := range rels {
		if r.Draft {
			continue
		}
		out = append(out, releaseJSON{Tag: r.TagName, Name: r.Name, SHA: shas[r.TagName], URL: r.HTMLURL, CreatedAt: r.CreatedAt, Prerelease: r.Prerelease})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

var (
	errTagExists  = errors.New("tag exists")
	errSHAUnknown = errors.New("sha unknown")
)

// createRelease cria a tag (ref leve) no sha e a GitHub Release com notas
// geradas pelo GitHub; o corpo começa pelas notas manuais e pela imagem.
func (g *github) createRelease(ctx context.Context, repo, tag, sha, image, notes string, prerelease bool) (string, error) {
	tok, err := g.installationToken(ctx, repo)
	if err != nil {
		return "", err
	}
	auth := "Bearer " + tok
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := g.call(ctx, "GET", "/repos/"+repo+"/commits/"+sha, auth, nil, &commit); err != nil {
		if s := ghStatus(err); s == 404 || s == 422 {
			return "", errSHAUnknown
		}
		return "", err
	}
	err = g.call(ctx, "GET", "/repos/"+repo+"/git/ref/tags/"+url.PathEscape(tag), auth, nil, nil)
	if err == nil {
		return "", errTagExists
	}
	if ghStatus(err) != 404 {
		return "", err
	}
	if err := g.call(ctx, "POST", "/repos/"+repo+"/git/refs", auth, map[string]string{"ref": "refs/tags/" + tag, "sha": commit.SHA}, nil); err != nil {
		if ghStatus(err) == 422 {
			return "", errTagExists // corrida: alguém criou a tag entre as chamadas
		}
		return "", err
	}
	body := "Image: `" + image + "`"
	if notes = strings.TrimSpace(notes); notes != "" {
		body = notes + "\n\n" + body
	}
	var rel struct {
		HTMLURL string `json:"html_url"`
	}
	in := map[string]any{"tag_name": tag, "name": tag, "body": body, "generate_release_notes": true, "prerelease": prerelease}
	if err := g.call(ctx, "POST", "/repos/"+repo+"/releases", auth, in, &rel); err != nil {
		return "", err
	}
	return rel.HTMLURL, nil
}

func githubRoutes(mux *http.ServeMux, g *github, appExists func(context.Context, string) (bool, error)) {
	upstream := func(w http.ResponseWriter, r *http.Request, err error) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}

	mux.HandleFunc("GET /api/github", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, g.publicJSON())
	})
	mux.HandleFunc("PUT /api/github", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AppID      json.Number `json:"appId"`
			Slug       string      `json:"slug"`
			PrivateKey string      `json:"privateKey"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		id, err := strconv.ParseInt(strings.TrimSpace(body.AppID.String()), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, r, http.StatusBadRequest, "err.invalidGitHubAppID")
			return
		}
		slug := strings.ToLower(strings.TrimSpace(body.Slug))
		if !slugRe.MatchString(slug) {
			writeError(w, r, http.StatusBadRequest, "err.invalidGitHubSlug")
			return
		}
		if body.PrivateKey == "" && !g.configured() {
			writeError(w, r, http.StatusBadRequest, "err.invalidGitHubKey")
			return
		}
		if body.PrivateKey != "" {
			if _, err := parseRSAKey(body.PrivateKey); err != nil {
				writeError(w, r, http.StatusBadRequest, "err.invalidGitHubKey")
				return
			}
		}
		if err := g.setConfig(id, slug, body.PrivateKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, g.publicJSON())
	})

	// app resolve {name} contra as apps reais do Dokku.
	app := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		name := r.PathValue("name")
		ok, err := appExists(r.Context(), name)
		switch {
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
		case !ok:
			writeError(w, r, http.StatusNotFound, "err.appNotFound")
		}
		return name, err == nil && ok
	}
	// ready: repo vinculado + App configurada; 400 caso contrário.
	ready := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		name, ok := app(w, r)
		if !ok {
			return "", false
		}
		repo := g.link(name)
		switch {
		case repo == "":
			writeError(w, r, http.StatusBadRequest, "err.githubNoRepo")
			return "", false
		case !g.configured():
			writeError(w, r, http.StatusBadRequest, "err.githubNotConfigured")
			return "", false
		}
		return repo, true
	}
	// installErr: 404 na instalação vira 400 "não instalada"; resto é 502.
	installErr := func(w http.ResponseWriter, r *http.Request, err error) {
		if ghStatus(err) == 404 {
			writeError(w, r, http.StatusBadRequest, "err.githubNotInstalled")
			return
		}
		upstream(w, r, err)
	}

	linkJSON := func(ctx context.Context, name string) map[string]any {
		repo := g.link(name)
		if repo == "" {
			return map[string]any{"repo": nil, "installed": false}
		}
		installed := false
		if g.configured() {
			_, err := g.installationToken(ctx, repo)
			installed = err == nil
		}
		return map[string]any{"repo": repo, "installed": installed}
	}
	mux.HandleFunc("GET /api/apps/{name}/github", func(w http.ResponseWriter, r *http.Request) {
		name, ok := app(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, linkJSON(r.Context(), name))
	})
	mux.HandleFunc("PUT /api/apps/{name}/github", func(w http.ResponseWriter, r *http.Request) {
		name, ok := app(w, r)
		if !ok {
			return
		}
		var body struct {
			Repo string `json:"repo"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		repo := strings.TrimSuffix(strings.TrimSpace(body.Repo), ".git")
		repo = strings.TrimPrefix(repo, "https://github.com/")
		if !repoRe.MatchString(repo) || strings.Contains(repo, "..") {
			writeError(w, r, http.StatusBadRequest, "err.invalidRepo")
			return
		}
		if err := g.setLink(name, repo); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, linkJSON(r.Context(), name))
	})
	mux.HandleFunc("DELETE /api/apps/{name}/github", func(w http.ResponseWriter, r *http.Request) {
		name, ok := app(w, r)
		if !ok {
			return
		}
		if err := g.setLink(name, ""); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"repo": nil, "installed": false})
	})

	mux.HandleFunc("GET /api/apps/{name}/github/releases", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := ready(w, r)
		if !ok {
			return
		}
		rels, err := g.releases(r.Context(), repo)
		if err != nil {
			installErr(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, rels)
	})
	mux.HandleFunc("POST /api/apps/{name}/github/releases", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := ready(w, r)
		if !ok {
			return
		}
		var body struct {
			Tag        string `json:"tag"`
			SHA        string `json:"sha"`
			Image      string `json:"image"`
			Notes      string `json:"notes"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		tag, sha, image := strings.TrimSpace(body.Tag), strings.TrimSpace(body.SHA), strings.TrimSpace(body.Image)
		switch {
		case !tagRe.MatchString(tag) || strings.Contains(tag, "..") || strings.HasSuffix(tag, ".lock") || strings.HasSuffix(tag, "/"):
			writeError(w, r, http.StatusBadRequest, "err.invalidTag")
			return
		case !shaRe.MatchString(sha):
			writeError(w, r, http.StatusUnprocessableEntity, "err.shaNotFound")
			return
		case image == "" || len(image) > imageMax || strings.ContainsAny(image, " \t\n`"):
			writeError(w, r, http.StatusBadRequest, "err.invalidImage")
			return
		}
		u, err := g.createRelease(r.Context(), repo, tag, sha, image, body.Notes, body.Prerelease)
		switch {
		case errors.Is(err, errTagExists):
			writeError(w, r, http.StatusConflict, "err.tagExists", tag)
		case errors.Is(err, errSHAUnknown):
			writeError(w, r, http.StatusUnprocessableEntity, "err.shaNotFound")
		case err != nil:
			installErr(w, r, err)
		default:
			writeJSON(w, http.StatusOK, map[string]string{"tag": tag, "url": u})
		}
	})
}
