package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Credentials é o arquivo ~/.config/dokk/credentials.json gravado por
// `dokk login`.
type Credentials struct {
	Current string                `json:"current"`
	Servers map[string]ServerCred `json:"servers"`
}

// ServerCred é o token de um painel.
type ServerCred struct {
	Token string `json:"token"`
}

// NormalizeURL tira espaços e a barra final; sem esquema, assume https.
func NormalizeURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u != "" && !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return u
}

// CredentialsPath é $XDG_CONFIG_HOME/dokk/credentials.json ou
// ~/.config/dokk/credentials.json.
func CredentialsPath() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "dokk", "credentials.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "dokk", "credentials.json"), nil
}

// LoadCredentials lê o arquivo; inexistente vira credencial vazia.
func LoadCredentials(path string) (*Credentials, error) {
	c := &Credentials{Servers: map[string]ServerCred{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Servers == nil {
		c.Servers = map[string]ServerCred{}
	}
	return c, nil
}

// Save grava com permissão 0600 (diretório 0700), de forma atômica.
func (c *Credentials) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Set grava o token do servidor e o torna o atual.
func (c *Credentials) Set(url, token string) {
	url = NormalizeURL(url)
	c.Servers[url] = ServerCred{Token: token}
	c.Current = url
}

// Remove apaga o servidor (o atual, se url vazio). Devolve o removido.
func (c *Credentials) Remove(url string) string {
	if url == "" {
		url = c.Current
	}
	url = NormalizeURL(url)
	if _, ok := c.Servers[url]; !ok {
		return ""
	}
	delete(c.Servers, url)
	if c.Current == url {
		c.Current = ""
		for u := range c.Servers {
			c.Current = u
			break
		}
	}
	return url
}

// ErrNotLoggedIn indica que não há DOKK_TOKEN nem credencial gravada.
var ErrNotLoggedIn = errors.New("sem credencial do dokk: rode `dokk login <url>` ou defina DOKK_URL e DOKK_TOKEN")

// FromEnv monta o cliente: DOKK_URL + DOKK_TOKEN quando ambos existem; senão
// a credencial gravada (a de DOKK_URL, se definido, ou a atual).
func FromEnv() (*Client, error) {
	u := NormalizeURL(os.Getenv("DOKK_URL"))
	if t := os.Getenv("DOKK_TOKEN"); u != "" && t != "" {
		return &Client{URL: u, Token: t}, nil
	}
	path, err := CredentialsPath()
	if err != nil {
		return nil, ErrNotLoggedIn
	}
	creds, err := LoadCredentials(path)
	if err != nil {
		return nil, err
	}
	if u == "" {
		u = creds.Current
	}
	if s, ok := creds.Servers[u]; ok && s.Token != "" {
		return &Client{URL: u, Token: s.Token}, nil
	}
	return nil, ErrNotLoggedIn
}
