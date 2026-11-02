package dokku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Release é uma tag publicada no registry da imagem de uma app.
type Release struct {
	Tag     string     `json:"tag"`
	Image   string     `json:"image"`
	Updated *time.Time `json:"updated,omitempty"`
}

// ImageRef é uma referência de imagem separada em partes.
type ImageRef struct {
	Registry string // "docker.io", "ghcr.io"...
	Repo     string // "library/nginx", "eduardotorresdev/portile"
	Tag      string
	Digest   string
}

// Name é o repositório como se escreve no docker ("nginx", "ghcr.io/x/y").
func (r ImageRef) Name() string {
	if r.Registry == "docker.io" {
		return strings.TrimPrefix(r.Repo, "library/")
	}
	return r.Registry + "/" + r.Repo
}

var imageRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*(@sha256:[a-f0-9]{64})?$`)

// ParseImage lê "nginx:1.27", "ghcr.io/a/b@sha256:...". ok=false se não for
// uma imagem (ex.: deploy por git push, em que a origem é um sha).
func ParseImage(s string) (ImageRef, bool) {
	if !imageRef.MatchString(s) || !strings.ContainsAny(s, ":/@") {
		return ImageRef{}, false
	}
	var r ImageRef
	s, r.Digest, _ = strings.Cut(s, "@")
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		s, r.Tag = s[:i], s[i+1:]
	}
	first, rest, found := strings.Cut(s, "/")
	if found && strings.ContainsAny(first, ".:") {
		r.Registry, r.Repo = first, rest
	} else {
		r.Registry, r.Repo = "docker.io", s
		if !found {
			r.Repo = "library/" + s
		}
	}
	return r, true
}

const (
	maxReleases = 40
	tagPageSize = 1000 // tags por página da API v2
	maxTagPages = 20
)

// Tags de versão lançada começam com número ("1.15.4", "v2", "1.27-alpine").
// O resto (pr-32-abc, sha-..., latest) costuma ser tag de CI; só aparece se
// o repositório não tiver nenhuma tag de versão.
var releaseTag = regexp.MustCompile(`^v?\d+(\.\d+)*([-.+_].*)?$`)

func pickReleases(tags []string) []string {
	var out []string
	for _, t := range tags {
		if releaseTag.MatchString(t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		for _, t := range tags {
			if !strings.HasPrefix(t, "sha256-") && !strings.HasSuffix(t, ".sig") && !strings.HasSuffix(t, ".att") {
				out = append(out, t)
			}
		}
	}
	return out
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Releases lista as tags mais recentes do repositório da imagem.
func (c *Client) Releases(ctx context.Context, ref ImageRef) ([]Release, error) {
	if ref.Registry == "docker.io" {
		return dockerHubReleases(ctx, ref)
	}
	return c.registryReleases(ctx, ref)
}

// Docker Hub: a API do Hub já ordena pela data de publicação.
func dockerHubReleases(ctx context.Context, ref ImageRef) ([]Release, error) {
	u := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags?page_size=100&ordering=last_updated", ref.Repo)
	var body struct {
		Results []struct {
			Name        string    `json:"name"`
			LastUpdated time.Time `json:"last_updated"`
		} `json:"results"`
	}
	if err := getJSON(ctx, u, "", &body); err != nil {
		return nil, err
	}
	updated := map[string]time.Time{}
	names := make([]string, len(body.Results))
	for i, t := range body.Results {
		names[i] = t.Name
		updated[t.Name] = t.LastUpdated
	}
	out := []Release{}
	for _, t := range pickReleases(names) {
		u := updated[t]
		out = append(out, Release{Tag: t, Image: ref.Name() + ":" + t, Updated: &u})
		if len(out) == maxReleases {
			break
		}
	}
	return out, nil
}

// Outros registries (ghcr, GitLab...): API v2 com o token que o próprio
// registry indica (WWW-Authenticate), usando o login que o dokku já tem em
// ~dokku/.docker/config.json. A API não traz datas; as tags vão
// ordenadas por versão, mais nova primeiro.
func (c *Client) registryReleases(ctx context.Context, ref ImageRef) ([]Release, error) {
	auth, err := c.registryAuthHeader(ctx, ref)
	if err != nil {
		return nil, err
	}
	// A lista vem paginada (o ghcr devolve no máximo 1000 por página, em
	// ordem de criação): segue com `last` até acabar, senão as versões mais
	// novas de um repo com muitas tags ficam de fora.
	var all []string
	last := ""
	for page := 0; page < maxTagPages; page++ {
		var body struct {
			Tags []string `json:"tags"`
		}
		u := fmt.Sprintf("%s://%s/v2/%s/tags/list?n=%d", registryScheme, ref.Registry, ref.Repo, tagPageSize)
		if last != "" {
			u += "&last=" + url.QueryEscape(last)
		}
		if err := getJSON(ctx, u, auth, &body); err != nil {
			return nil, err
		}
		all = append(all, body.Tags...)
		if len(body.Tags) < tagPageSize || body.Tags[len(body.Tags)-1] == last {
			break
		}
		last = body.Tags[len(body.Tags)-1]
	}
	tags := pickReleases(all)
	sort.Slice(tags, func(i, j int) bool { return versionLess(tags[j], tags[i]) })
	out := []Release{}
	for _, t := range tags {
		out = append(out, Release{Tag: t, Image: ref.Name() + ":" + t})
		if len(out) == maxReleases {
			break
		}
	}
	return out, nil
}

// registryScheme é https; os testes trocam por http (httptest).
var registryScheme = "https"

// registryAuthHeader descobre como autenticar no registry pelo protocolo da
// API v2: GET /v2/ sem credencial → 401 com WWW-Authenticate. Bearer (ghcr,
// GitLab, Docker Hub, Harbor...): pede o token no realm com service e scope
// do repo, com o login do dokku em Basic quando há. Basic: manda o login
// direto. 200: registry aberto. Devolve o valor do cabeçalho Authorization
// ("" = sem auth).
func (c *Client) registryAuthHeader(ctx context.Context, ref ImageRef) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registryScheme+"://"+ref.Registry+"/v2/", nil)
	if err != nil {
		return "", err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	basic := c.registryAuth(ref.Registry)
	switch {
	case res.StatusCode == http.StatusOK:
		return "", nil
	case res.StatusCode != http.StatusUnauthorized:
		return "", &RegistryStatusError{Status: res.Status}
	}
	scheme, params := parseChallenge(res.Header.Get("WWW-Authenticate"))
	switch scheme {
	case "basic":
		if basic == "" {
			return "", &RegistryStatusError{Status: res.Status}
		}
		return "Basic " + basic, nil
	case "bearer":
	default:
		return "", &RegistryStatusError{Status: res.Status}
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" || (realm.Scheme != "https" && realm.Scheme != registryScheme) {
		return "", fmt.Errorf("registry %s: realm inválido no WWW-Authenticate", ref.Registry)
	}
	q := realm.Query()
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	q.Set("scope", "repository:"+ref.Repo+":pull")
	realm.RawQuery = q.Encode()
	auth := ""
	if basic != "" {
		auth = "Basic " + basic
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := getJSON(ctx, realm.String(), auth, &body); err != nil {
		return "", err
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	if body.Token == "" {
		return "", fmt.Errorf("registry %s: resposta de token vazia", ref.Registry)
	}
	return "Bearer " + body.Token, nil
}

// parseChallenge lê `Bearer realm="…",service="…",scope="…"`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest = strings.TrimSpace(rest); rest != ""; {
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				params[k] = v[1:]
				break
			}
			params[k], rest = v[1:end+1], v[end+2:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			params[k], rest = strings.TrimSpace(val), after
		}
		rest = strings.TrimLeft(strings.TrimSpace(rest), ",")
		rest = strings.TrimSpace(rest)
	}
	return strings.ToLower(scheme), params
}

// registryAuth devolve o "auth" (base64 de usuário:senha) do docker login
// do dokku para o registry, se houver.
func (c *Client) registryAuth(registry string) string {
	b, err := os.ReadFile(c.home() + "/.docker/config.json")
	if err != nil {
		return ""
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return ""
	}
	// O docker grava o Docker Hub como "https://index.docker.io/v1/".
	a := ""
	for k, v := range cfg.Auths {
		if NormalizeRegistry(k) == NormalizeRegistry(registry) {
			a = v.Auth
			break
		}
	}
	if a == "" {
		return ""
	}
	if _, err := base64.StdEncoding.DecodeString(a); err != nil {
		return ""
	}
	return a
}

func getJSON(ctx context.Context, u, auth string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &RegistryStatusError{Status: res.Status}
	}
	return json.NewDecoder(res.Body).Decode(v)
}

var numParts = regexp.MustCompile(`\d+|\D+`)

// versionLess compara tags "naturalmente" (1.10 > 1.9; números por valor).
func versionLess(a, b string) bool {
	pa, pb := numParts.FindAllString(a, -1), numParts.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		na, ea := atoi(pa[i])
		nb, eb := atoi(pb[i])
		if ea && eb {
			return na < nb
		}
		return pa[i] < pb[i]
	}
	return len(pa) < len(pb)
}

func atoi(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 1<<40 {
			return n, true
		}
	}
	return n, s != ""
}

// Install faz o deploy de uma imagem (git:from-image).
func (c *Client) Install(ctx context.Context, app, image string) error {
	_, err := c.Runner.Run(ctx, "dokku", "git:from-image", app, image)
	return err
}

// manifestAccept cobre manifest único e lista/índice (multi-arch), para o
// digest do HEAD ser o mesmo que o docker grava em repo@sha256:….
const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// TagForDigest procura, entre as max tags mais novas, a que aponta para
// ref.Digest (um HEAD de manifest por tag). "" quando não acha ou quando o
// registry é o Docker Hub (fora do escopo: o pbs usa ghcr).
func (c *Client) TagForDigest(ctx context.Context, ref ImageRef, tags []Release, max int) (string, error) {
	if ref.Digest == "" || ref.Registry == "docker.io" {
		return "", nil
	}
	auth, err := c.registryAuthHeader(ctx, ref)
	if err != nil {
		return "", err
	}
	for i, t := range tags {
		if i == max {
			break
		}
		u := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", registryScheme, ref.Registry, ref.Repo, url.PathEscape(t.Tag))
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err != nil {
			return "", err
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("Accept", manifestAccept)
		res, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK && res.Header.Get("Docker-Content-Digest") == ref.Digest {
			return t.Tag, nil
		}
	}
	return "", nil
}
