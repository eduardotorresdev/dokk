package main

import (
	"context"
	"crypto/sha256"
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

// GitHub App Manifest flow: o dokk cria a própria App no GitHub sem colar
// id/chave. /settings pede um manifest (POST /api/github/manifest) e o
// navegador faz POST de form para o GitHub; o GitHub volta em
// /github/callback?code=&state=, onde o code vira id+slug+pem; depois da
// instalação o GitHub manda para /github/installed. As rotas /github/* ficam
// fora do middleware de /api e conferem o cookie aqui.

const manifestStateTTL = 10 * time.Minute

var orgRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

type manifestState struct {
	session string // sha256 do cookie de sessão
	exp     time.Time
}

type manifestFlow struct {
	g       *github
	a       *auth
	webBase string // https://github.com
	mu      sync.Mutex
	states  map[string]manifestState
}

func sessionHash(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(c.Value))
	return hex.EncodeToString(sum[:])
}

func (m *manifestFlow) newState(r *http.Request) string {
	s := hex.EncodeToString(randomBytes(20))
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.g.now()
	for k, v := range m.states {
		if now.After(v.exp) {
			delete(m.states, k)
		}
	}
	m.states[s] = manifestState{sessionHash(r), now.Add(manifestStateTTL)}
	return s
}

// takeState consome o state (uso único) e confere sessão e validade.
func (m *manifestFlow) takeState(r *http.Request, s string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[s]
	delete(m.states, s)
	return ok && st.session != "" && st.session == sessionHash(r) && !m.g.now().After(st.exp)
}

// appName: "dokk-<host>-<sufixo>", no máximo 34 caracteres (limite do GitHub).
func appName(origin string) string {
	host := origin
	if u, err := url.Parse(origin); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	var b strings.Builder
	for _, c := range strings.ToLower(host) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if len(h) > 23 {
		h = strings.Trim(h[:23], "-")
	}
	if h == "" {
		h = "panel"
	}
	return "dokk-" + h + "-" + hex.EncodeToString(randomBytes(2))
}

func manifestFor(origin string) map[string]any {
	return map[string]any{
		"name":                appName(origin),
		"url":                 origin,
		"redirect_url":        origin + "/github/callback",
		"setup_url":           origin + "/github/installed",
		"setup_on_update":     true,
		"public":              false,
		"default_permissions": map[string]string{"contents": "write", "metadata": "read"},
		"hook_attributes":     map[string]any{"url": origin + "/github/webhook", "active": false},
	}
}

// convert troca o code do manifest pela App criada e grava em github.json.
func (m *manifestFlow) convert(ctx context.Context, code string) (string, error) {
	g := m.g
	req, err := http.NewRequestWithContext(ctx, "POST", g.apiBase+"/app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", &ghError{res.StatusCode, strings.TrimSpace(string(b))}
	}
	var out struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		PEM           string `json:"pem"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	key, err := parseRSAKey(out.PEM)
	if err != nil || out.ID <= 0 || !slugRe.MatchString(out.Slug) {
		return "", errors.New("resposta de conversão inválida")
	}
	cfg := githubConfig{AppID: out.ID, Slug: out.Slug, PrivateKey: strings.TrimSpace(out.PEM) + "\n",
		ClientID: out.ClientID, ClientSecret: out.ClientSecret, WebhookSecret: out.WebhookSecret}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := writeJSONFile(filepath.Join(g.dir, "github.json"), cfg); err != nil {
		return "", err
	}
	g.cfg, g.key = cfg, key
	g.tokens = map[string]cachedToken{}
	return out.Slug, nil
}

type ghRepo struct {
	FullName string `json:"fullName"`
	Private  bool   `json:"private"`
}

// repos lista os repositórios acessíveis em todas as instalações da App.
func (g *github) repos(ctx context.Context) ([]ghRepo, error) {
	jwt, err := g.appJWT()
	if err != nil {
		return nil, err
	}
	var insts []struct {
		ID int64 `json:"id"`
	}
	if err := g.call(ctx, "GET", "/app/installations?per_page=100", "Bearer "+jwt, nil, &insts); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []ghRepo{}
	for _, in := range insts {
		var tok struct {
			Token string `json:"token"`
		}
		if err := g.call(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", in.ID), "Bearer "+jwt, nil, &tok); err != nil {
			return nil, err
		}
		for page := 1; page <= 10; page++ {
			var list struct {
				Repositories []struct {
					FullName string `json:"full_name"`
					Private  bool   `json:"private"`
				} `json:"repositories"`
			}
			if err := g.call(ctx, "GET", fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), "Bearer "+tok.Token, nil, &list); err != nil {
				return nil, err
			}
			for _, r := range list.Repositories {
				if !seen[r.FullName] {
					seen[r.FullName] = true
					out = append(out, ghRepo{r.FullName, r.Private})
				}
			}
			if len(list.Repositories) < 100 {
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].FullName) < strings.ToLower(out[j].FullName) })
	return out, nil
}

func manifestRoutes(mux *http.ServeMux, g *github, a *auth, webBase string) *manifestFlow {
	if webBase == "" {
		webBase = "https://github.com"
	}
	m := &manifestFlow{g: g, a: a, webBase: strings.TrimRight(webBase, "/"), states: map[string]manifestState{}}

	// Devolve para onde o navegador deve postar o form e o manifest.
	mux.HandleFunc("POST /api/github/manifest", func(w http.ResponseWriter, r *http.Request) {
		if _, has := bearer(r); has || !a.cookieLoggedIn(r) {
			writeError(w, r, http.StatusForbidden, "err.cookieRequired")
			return
		}
		var body struct {
			Org string `json:"org"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil && err != io.EOF {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		org := strings.TrimSpace(body.Org)
		action := m.webBase + "/settings/apps/new"
		if org != "" {
			if !orgRe.MatchString(org) {
				writeError(w, r, http.StatusBadRequest, "err.invalidGitHubOrg")
				return
			}
			action = m.webBase + "/organizations/" + org + "/settings/apps/new"
		}
		manifest, _ := json.Marshal(manifestFor(publicOrigin(r)))
		writeJSON(w, http.StatusOK, map[string]string{"action": action + "?state=" + m.newState(r), "manifest": string(manifest)})
	})

	mux.HandleFunc("GET /github/callback", func(w http.ResponseWriter, r *http.Request) {
		if !a.cookieLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		q := r.URL.Query()
		if !m.takeState(r, q.Get("state")) || q.Get("code") == "" {
			http.Redirect(w, r, "/settings?github=state", http.StatusFound)
			return
		}
		slug, err := m.convert(r.Context(), q.Get("code"))
		if err != nil {
			http.Redirect(w, r, "/settings?github=error", http.StatusFound)
			return
		}
		http.Redirect(w, r, m.webBase+"/apps/"+slug+"/installations/new", http.StatusFound)
	})

	mux.HandleFunc("GET /github/installed", func(w http.ResponseWriter, r *http.Request) {
		if !a.cookieLoggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/settings?github=installed", http.StatusFound)
	})

	mux.HandleFunc("GET /api/github/repos", func(w http.ResponseWriter, r *http.Request) {
		if !g.configured() {
			writeError(w, r, http.StatusBadRequest, "err.githubNotConfigured")
			return
		}
		list, err := g.repos(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
	return m
}
