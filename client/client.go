// Package client fala com a API HTTP de um painel dokk (DOKK_URL +
// DOKK_TOKEN) e implementa o fluxo de release usado por `dokk mcp` e
// `dokk release`.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client é um cliente da API do dokk.
type Client struct {
	URL   string // base, ex. https://dokk.example.com
	Token string // enviado como Authorization: Bearer
	HTTP  *http.Client
}

// APIError é uma resposta não-2xx da API.
type APIError struct {
	Status  int
	Message string
	Path    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("dokk: HTTP %d", e.Status)
	}
	return fmt.Sprintf("dokk: %s (HTTP %d)", e.Message, e.Status)
}

// Do faz a requisição e devolve o corpo cru. Respostas não-2xx viram *APIError.
func (c *Client) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return nil, &APIError{Status: res.StatusCode, Message: e.Error, Path: path}
	}
	return data, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	data, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func appPath(name string, rest ...string) string {
	var b strings.Builder
	b.WriteString("/api/apps/" + url.PathEscape(name))
	for _, r := range rest {
		b.WriteString("/" + url.PathEscape(r))
	}
	return b.String()
}

// App é o subconjunto de GET /api/apps/{name} que o cliente usa.
type App struct {
	Name         string              `json:"name"`
	Deployed     bool                `json:"deployed"`
	DeploySource string              `json:"deploy-source"`
	GitSHA       string              `json:"git-sha"` // imagem atual em deploys por imagem
	LastDeployAt *time.Time          `json:"last-deploy-at"`
	Domains      []string            `json:"domains"`
	SSL          bool                `json:"ssl"`
	Processes    map[string]*Process `json:"processes"`
	Checks       Checks              `json:"checks"`
	Action       string              `json:"action,omitempty"` // na lista: ação em andamento
}

// Process é a contagem de containers de um tipo de processo.
type Process struct {
	Running int `json:"running"`
	Total   int `json:"total"`
}

// Checks é o estado de saúde da app.
type Checks struct {
	Status      string     `json:"status"`
	LastCheckAt *time.Time `json:"last-check-at"`
}

// URL é o endereço público da app (primeiro domínio), ou "".
func (a App) URL() string {
	if len(a.Domains) == 0 {
		return ""
	}
	if a.SSL {
		return "https://" + a.Domains[0]
	}
	return "http://" + a.Domains[0]
}

// Image é a imagem atual quando o deploy é por imagem.
func (a App) Image() string {
	if a.DeploySource == "docker-image" {
		return a.GitSHA
	}
	return ""
}

// Tag é a tag da imagem atual ("" sem imagem ou sem tag).
func (a App) Tag() string {
	img := a.Image()
	if i := strings.LastIndex(img, "@"); i >= 0 {
		img = img[:i]
	}
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		return img[i+1:]
	}
	return ""
}

// Action é a ação em segundo plano de uma app (install, deploy...).
type Action struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

// AppStatus é a resposta de GET /api/apps/{name}.
type AppStatus struct {
	App    App     `json:"app"`
	Action *Action `json:"action"`
}

// RepoRelease é uma release (tag do formato da app, com a Release do
// provedor quando há) no repositório vinculado: GitHub ou GitLab.
type RepoRelease struct {
	Tag        string `json:"tag"`
	Version    string `json:"version"` // semver sem prefixo (ex. 1.17.0)
	Name       string `json:"name"`
	SHA        string `json:"sha"`
	URL        string `json:"url"`
	CreatedAt  string `json:"createdAt"`
	Prerelease bool   `json:"prerelease"`
}

// GithubRelease é o nome antigo de RepoRelease.
type GithubRelease = RepoRelease

// NewRelease é o corpo de POST /api/apps/{name}/repo/releases.
type NewRelease struct {
	Tag        string `json:"tag"`
	SHA        string `json:"sha"`
	Image      string `json:"image"`
	Notes      string `json:"notes,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

// CreatedRelease é a resposta da criação.
type CreatedRelease struct {
	Provider string `json:"provider,omitempty"` // vazio em servidor antigo (= github)
	Tag      string `json:"tag"`
	URL      string `json:"url"`
}

// AppsRaw devolve GET /api/apps como veio.
func (c *Client) AppsRaw(ctx context.Context) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, "/api/apps", nil)
}

// AppRaw devolve GET /api/apps/{name} como veio.
func (c *Client) AppRaw(ctx context.Context, name string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, appPath(name), nil)
}

// AppStatus lê GET /api/apps/{name}.
func (c *Client) AppStatus(ctx context.Context, name string) (AppStatus, error) {
	var s AppStatus
	err := c.getJSON(ctx, appPath(name), &s)
	return s, err
}

// routeMissing: o servidor não conhece a rota (dokk antigo, sem /repo).
// O 404 do mux vem em texto puro, diferente do {"error"} de app inexistente;
// um POST numa rota desconhecida cai no "GET /" da UI e vira 405.
func routeMissing(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusMethodNotAllowed || ae.Status == http.StatusNotFound && ae.Message == "404 page not found")
}

// repoDo chama a rota neutra /repo…; num servidor antigo, a /github….
func (c *Client) repoDo(ctx context.Context, method, name string, body any, rest ...string) ([]byte, error) {
	data, err := c.Do(ctx, method, appPath(name, append([]string{"repo"}, rest...)...), body)
	if routeMissing(err) {
		data, err = c.Do(ctx, method, appPath(name, append([]string{"github"}, rest...)...), body)
	}
	return data, err
}

// RepoReleases lista as releases do repo vinculado, maior versão primeiro.
func (c *Client) RepoReleases(ctx context.Context, name string) ([]RepoRelease, error) {
	data, err := c.repoDo(ctx, http.MethodGet, name, nil, "releases")
	if err != nil {
		return nil, err
	}
	var l []RepoRelease
	err = json.Unmarshal(data, &l)
	return l, err
}

// GithubReleases é o nome antigo de RepoReleases.
func (c *Client) GithubReleases(ctx context.Context, name string) ([]RepoRelease, error) {
	return c.RepoReleases(ctx, name)
}

// RepoLink é o vínculo app ↔ repositório.
type RepoLink struct {
	Provider  string `json:"provider"` // github ou gitlab (vazio em servidor antigo = github)
	Repo      string `json:"repo"`
	Installed bool   `json:"installed"`
	TagFormat string `json:"tagFormat"` // ex. v{version}, back@{version}
}

// GithubLink é o nome antigo de RepoLink.
type GithubLink = RepoLink

// ProviderName é o nome do provedor para mensagens ("GitHub", "GitLab").
func (l RepoLink) ProviderName() string { return ProviderName(l.Provider) }

// ProviderName traduz o id do provedor para exibição.
func ProviderName(p string) string {
	if p == "gitlab" {
		return "GitLab"
	}
	return "GitHub"
}

// RepoLink lê GET /api/apps/{name}/repo.
func (c *Client) RepoLink(ctx context.Context, name string) (RepoLink, error) {
	var l RepoLink
	data, err := c.repoDo(ctx, http.MethodGet, name, nil)
	if err == nil {
		err = json.Unmarshal(data, &l)
	}
	if l.TagFormat == "" {
		l.TagFormat = "v{version}"
	}
	if l.Provider == "" && l.Repo != "" {
		l.Provider = "github"
	}
	return l, err
}

// GithubLink é o nome antigo de RepoLink.
func (c *Client) GithubLink(ctx context.Context, name string) (RepoLink, error) {
	return c.RepoLink(ctx, name)
}

// CreateRelease cria a tag no sha e a Release no provedor do repo.
func (c *Client) CreateRelease(ctx context.Context, name string, r NewRelease) (CreatedRelease, error) {
	data, err := c.repoDo(ctx, http.MethodPost, name, r, "releases")
	if err != nil {
		return CreatedRelease{}, err
	}
	var out CreatedRelease
	err = json.Unmarshal(data, &out)
	if out.Provider == "" {
		out.Provider = "github"
	}
	return out, err
}

// Install pede a instalação de outra tag da mesma imagem.
func (c *Client) Install(ctx context.Context, name, image string) error {
	_, err := c.Do(ctx, http.MethodPost, appPath(name, "install"), map[string]string{"image": image})
	return err
}

// BuildLog devolve o log de um build.
func (c *Client) BuildLog(ctx context.Context, name, id string) (string, error) {
	data, err := c.Do(ctx, http.MethodGet, appPath(name, "builds", id, "log"), nil)
	return string(data), err
}

// WaitHealthy acompanha a app até a ação terminar e os checks ficarem
// healthy. Falha se a ação terminar com erro, a app ficar unhealthy depois
// da ação ou o contexto expirar.
func (c *Client) WaitHealthy(ctx context.Context, name string, every time.Duration, progress func(string)) error {
	return c.WaitAction(ctx, name, every, true, progress)
}

// WaitAction acompanha a ação em segundo plano da app até terminar. Com
// healthy, espera também os checks ficarem healthy.
func (c *Client) WaitAction(ctx context.Context, name string, every time.Duration, healthy bool, progress func(string)) error {
	if every <= 0 {
		every = 3 * time.Second
	}
	last := ""
	for {
		s, err := c.AppStatus(ctx, name)
		if err != nil {
			return err
		}
		running := s.Action != nil && s.Action.Running
		state := s.App.Checks.Status
		if running {
			state = s.Action.Name + "…"
		}
		if state != last && progress != nil && state != "" {
			progress(state)
		}
		last = state
		if !running {
			if s.Action != nil && s.Action.Error != "" {
				return fmt.Errorf("%s falhou: %s", s.Action.Name, s.Action.Error)
			}
			if !healthy {
				return nil
			}
			switch state {
			case "healthy":
				return nil
			case "unhealthy", "degraded":
				return fmt.Errorf("app %s ficou %s", name, state)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("esperando %s: %w", name, ctx.Err())
		case <-time.After(every):
		}
	}
}
