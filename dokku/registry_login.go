package dokku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Registry é um login global do dokku num registry (docker login do
// usuário dokku). A senha nunca sai do servidor.
type Registry struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Helper   bool   `json:"helper,omitempty"`
}

// NormalizeRegistry tira esquema, barra final e caixa alta; os nomes do
// Docker Hub viram "docker.io".
func NormalizeRegistry(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s, _, _ = strings.Cut(s, "/")
	switch s {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return s
}

var registryRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:\d{1,5})?$`)

func ValidRegistry(s string) bool { return registryRe.MatchString(s) }

// ValidRegistryUser e ValidRegistryPassword barram o que viraria flag ou
// quebraria o stdin do registry:login.
func ValidRegistryUser(u string) bool {
	return u != "" && len(u) <= 256 && !strings.ContainsAny(u, " \t\r\n") && !strings.HasPrefix(u, "-")
}

func ValidRegistryPassword(p string) bool {
	return p != "" && len(p) <= 4096 && !strings.ContainsAny(p, "\r\n")
}

// Registries lista os logins de HomeRoot/.docker/config.json (auths),
// ordenados por servidor. Sem arquivo: lista vazia, sem erro.
func (c *Client) Registries() ([]Registry, error) {
	out := []Registry{}
	b, err := os.ReadFile(c.HomeRoot + "/.docker/config.json")
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, &DockerConfigError{Err: err}
	}
	for k, v := range cfg.Auths {
		r := Registry{Server: NormalizeRegistry(k)}
		dec, err := base64.StdEncoding.DecodeString(v.Auth)
		user, _, ok := strings.Cut(string(dec), ":")
		if err != nil || !ok || v.Auth == "" {
			r.Helper = true
		} else {
			r.Username = user
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out, nil
}

// RegistryLogin roda `registry:login --global --password-stdin <server> <user>`
// com a senha no stdin. O erro nunca carrega a senha.
func (c *Client) RegistryLogin(ctx context.Context, server, username, password string) error {
	server = NormalizeRegistry(server)
	switch {
	case !ValidRegistry(server):
		return errors.New("registry inválido")
	case !ValidRegistryUser(username):
		return errors.New("usuário inválido")
	case !ValidRegistryPassword(password):
		return errors.New("senha ou token inválido")
	}
	_, err := c.runInput(ctx, password, "dokku", "registry:login", "--global", "--password-stdin", server, username)
	if err != nil {
		return &RegistryLoginError{Server: server, Stderr: strings.ReplaceAll(err.Error(), password, "***")}
	}
	return nil
}

// RegistryLogout roda `registry:logout --global <server>`.
func (c *Client) RegistryLogout(ctx context.Context, server string) error {
	server = NormalizeRegistry(server)
	if !ValidRegistry(server) {
		return errors.New("registry inválido")
	}
	_, err := c.Runner.Run(ctx, "dokku", "registry:logout", "--global", server)
	return err
}
