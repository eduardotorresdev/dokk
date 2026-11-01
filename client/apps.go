package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// Erros com significado próprio: as camadas (CLI, MCP) traduzem em "o que
// fazer" via Advise.
var (
	ErrNotPushed      = errors.New("o commit não está em nenhum branch remoto")
	ErrNotImageDeploy = errors.New("a app não faz deploy por imagem")
	ErrTagExists      = errors.New("essa release já existe")
)

// Advice classifica um erro pelo que o usuário deve fazer a seguir.
type Advice int

const (
	AdviceNone        Advice = iota
	AdviceLogin              // sem credencial
	AdviceBadToken           // 401/403
	AdviceAppNotFound        // 404
	AdviceBusy               // 409 genérico (ação em andamento)
	AdviceTagExists          // tag já existe
	AdvicePush               // commit não está no remoto / 422
	AdviceLinkRepo           // 400 na release: repo/GitHub App não vinculado
	AdviceImage              // app não é deploy por imagem
	AdviceUnreachable        // rede
)

// Advise diz o que fazer diante de err.
func Advise(err error) Advice {
	var ae *APIError
	var ne net.Error
	var oe *net.OpError
	switch {
	case err == nil:
		return AdviceNone
	case errors.Is(err, ErrNotLoggedIn):
		return AdviceLogin
	case errors.Is(err, ErrNotPushed):
		return AdvicePush
	case errors.Is(err, ErrTagExists):
		return AdviceTagExists
	case errors.Is(err, ErrNotImageDeploy):
		return AdviceImage
	case errors.As(err, &ae):
		switch ae.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return AdviceBadToken
		case http.StatusNotFound:
			return AdviceAppNotFound
		case http.StatusConflict:
			if ae.Path != "" && strings.HasSuffix(ae.Path, "/github/releases") {
				return AdviceTagExists
			}
			return AdviceBusy
		case http.StatusUnprocessableEntity:
			return AdvicePush
		case http.StatusBadRequest:
			if strings.HasSuffix(ae.Path, "/github/releases") {
				return AdviceLinkRepo
			}
		}
	case errors.As(err, &oe), errors.As(err, &ne):
		return AdviceUnreachable
	}
	return AdviceNone
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// StripANSI remove sequências de escape ANSI (cores, cursor, OSC) e
// normaliza \r de barras de progresso.
func StripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	if strings.Contains(s, "\r") {
		lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
		for i, l := range lines {
			if j := strings.LastIndex(l, "\r"); j >= 0 {
				lines[i] = l[j+1:]
			}
		}
		s = strings.Join(lines, "\n")
	}
	return s
}

// Apps lista as apps.
func (c *Client) Apps(ctx context.Context) ([]App, error) {
	var l []App
	err := c.getJSON(ctx, "/api/apps", &l)
	return l, err
}

// Status é o detalhe de uma app para exibição.
type Status struct {
	App     App     `json:"app"`
	Action  *Action `json:"action,omitempty"`
	Version string  `json:"version,omitempty"` // tag da imagem atual (por digest: a tag resolvida ou o digest curto)
	Commit  string  `json:"commit,omitempty"`  // sha da release do GitHub dessa tag
	Release string  `json:"release,omitempty"` // URL da release
}

// Status junta o detalhe da app e, quando há, a release do GitHub da tag
// instalada (sem vínculo com GitHub, Commit fica vazio).
func (c *Client) Status(ctx context.Context, name string) (Status, error) {
	st, err := c.AppStatus(ctx, name)
	if err != nil {
		return Status{}, err
	}
	out := Status{App: st.App, Action: st.Action, Version: c.CurrentVersion(ctx, st.App)}
	if out.Version != "" {
		if rels, err := c.GithubReleases(ctx, name); err == nil {
			for _, r := range rels {
				if r.Tag == out.Version || ImageTag(r.Tag) == out.Version {
					out.Commit, out.Release = r.SHA, r.URL
					break
				}
			}
		}
	}
	return out, nil
}

// ReleaseList são as releases do GitHub e a tag instalada.
type ReleaseList struct {
	Current  string          `json:"current,omitempty"`
	Releases []GithubRelease `json:"releases"`
}

// Releases lista as releases do GitHub da app, marcando a instalada.
func (c *Client) Releases(ctx context.Context, name string) (ReleaseList, error) {
	rels, err := c.GithubReleases(ctx, name)
	if err != nil {
		return ReleaseList{}, err
	}
	out := ReleaseList{Releases: rels}
	if out.Releases == nil {
		out.Releases = []GithubRelease{}
	}
	if st, err := c.AppStatus(ctx, name); err == nil {
		out.Current = c.CurrentVersion(ctx, st.App)
	}
	return out, nil
}

// ImageForTag troca a tag da imagem atual da app pela pedida.
func (c *Client) ImageForTag(ctx context.Context, app, tag string) (string, error) {
	st, err := c.AppStatus(ctx, app)
	if err != nil {
		return "", err
	}
	img := st.App.Image()
	if img == "" {
		return "", fmt.Errorf("app %s: %w", app, ErrNotImageDeploy)
	}
	if i := strings.LastIndex(img, "@"); i >= 0 {
		img = img[:i]
	}
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		img = img[:i]
	}
	return img + ":" + ImageTag(tag), nil
}

// Session é quem está autenticado.
type Session struct {
	URL   string `json:"url"`
	Email string `json:"email"`
}

// Session consulta /api/session (só depende do token, não do Docker).
func (c *Client) Session(ctx context.Context) (Session, error) {
	data, err := c.Do(ctx, http.MethodGet, "/api/session", nil)
	if err != nil {
		return Session{}, err
	}
	var s Session
	json.Unmarshal(data, &s)
	s.URL = c.URL
	return s, nil
}

// LoginAndSave faz o Login e grava a credencial como a atual. Devolve o
// cliente já autenticado.
func LoginAndSave(ctx context.Context, o LoginOptions) (*Client, error) {
	o.URL = NormalizeURL(o.URL)
	tok, err := Login(ctx, o)
	if err != nil {
		return nil, err
	}
	path, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	creds, err := LoadCredentials(path)
	if err != nil {
		return nil, err
	}
	creds.Set(o.URL, tok)
	if err := creds.Save(path); err != nil {
		return nil, err
	}
	return &Client{URL: o.URL, Token: tok}, nil
}

// Logout remove a credencial de url (a atual se vazio). Devolve a removida
// ("" se não havia).
func Logout(url string) (string, error) {
	path, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	creds, err := LoadCredentials(path)
	if err != nil {
		return "", err
	}
	removed := creds.Remove(url)
	if removed == "" {
		return "", nil
	}
	return removed, creds.Save(path)
}
