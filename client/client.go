// Package client fala com a API HTTP de um painel dokk (DOKK_URL +
// DOKK_TOKEN) e implementa o fluxo de release usado por `dokk mcp` e
// `dokk release`.
package client

import (
	"bytes"
	"context"
	"encoding/json"
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

// GithubRelease é uma release do GitHub vinculada à app.
type GithubRelease struct {
	Tag        string `json:"tag"`
	Name       string `json:"name"`
	SHA        string `json:"sha"`
	URL        string `json:"url"`
	CreatedAt  string `json:"createdAt"`
	Prerelease bool   `json:"prerelease"`
}

// NewRelease é o corpo de POST /api/apps/{name}/github/releases.
type NewRelease struct {
	Tag        string `json:"tag"`
	SHA        string `json:"sha"`
	Image      string `json:"image"`
	Notes      string `json:"notes,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
}

// CreatedRelease é a resposta da criação.
type CreatedRelease struct {
	Tag string `json:"tag"`
	URL string `json:"url"`
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

// GithubReleases lista as releases do GitHub da app, mais recente primeiro.
func (c *Client) GithubReleases(ctx context.Context, name string) ([]GithubRelease, error) {
	var l []GithubRelease
	err := c.getJSON(ctx, appPath(name, "github", "releases"), &l)
	return l, err
}

// CreateRelease cria a release (tag no sha) no GitHub.
func (c *Client) CreateRelease(ctx context.Context, name string, r NewRelease) (CreatedRelease, error) {
	data, err := c.Do(ctx, http.MethodPost, appPath(name, "github", "releases"), r)
	if err != nil {
		return CreatedRelease{}, err
	}
	var out CreatedRelease
	err = json.Unmarshal(data, &out)
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
