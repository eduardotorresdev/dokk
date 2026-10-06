package dokku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// Nome de variável aceito pelo config:set (evita injetar flags ou "=").
var configKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidConfigKey(k string) bool { return configKey.MatchString(k) }

// Config devolve as variáveis da app com os valores (só sob demanda: a tela
// de secrets é a única que pede).
func (c *Client) Config(ctx context.Context, app string) (map[string]string, error) {
	out, err := c.Runner.Run(ctx, "dokku", "config:export", "--format", "json", app)
	if err != nil {
		return nil, err
	}
	vars := map[string]string{}
	if out == "" {
		return vars, nil
	}
	if err := json.Unmarshal([]byte(out), &vars); err != nil {
		return nil, fmt.Errorf("config:export: %w", err)
	}
	return vars, nil
}

// SetConfig grava uma variável. Com restart=false a app só pega o valor no
// próximo deploy/restart.
func (c *Client) SetConfig(ctx context.Context, app, key, value string, restart bool) error {
	if !ValidConfigKey(key) {
		return fmt.Errorf("nome inválido: %q", key)
	}
	args := []string{"config:set", "--encoded"}
	if !restart {
		args = append(args, "--no-restart")
	}
	args = append(args, app, key+"="+b64(value))
	// O erro do runner repete os argumentos, e eles carregam o valor:
	// devolve só o nome para não vazar o secret em log ou resposta.
	if _, err := c.Runner.Run(ctx, "dokku", args...); err != nil {
		return &ConfigSetError{Keys: []string{key}}
	}
	return nil
}

func (c *Client) UnsetConfig(ctx context.Context, app, key string, restart bool) error {
	if !ValidConfigKey(key) {
		return fmt.Errorf("nome inválido: %q", key)
	}
	args := []string{"config:unset"}
	if !restart {
		args = append(args, "--no-restart")
	}
	args = append(args, app, key)
	_, err := c.Runner.Run(ctx, "dokku", args...)
	return err
}

// PsAction roda ps:start, ps:stop ou ps:restart.
func (c *Client) PsAction(ctx context.Context, app, action string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("ação inválida: %q", action)
	}
	_, err := c.Runner.Run(ctx, "dokku", "ps:"+action, app)
	return err
}

// RenewSSL reemite o certificado Let's Encrypt da app (--force: sem ele o
// plugin pula a emissão enquanto o certificado atual for válido).
func (c *Client) RenewSSL(ctx context.Context, app string) error {
	_, err := c.Runner.Run(ctx, "dokku", "letsencrypt:enable", app, "--force")
	if err == nil {
		c.Invalidate()
	}
	return err
}

// SSLAutoRenew diz se o cron de auto-renovação do letsencrypt está ligado.
// O plugin só tem a opção global (vale para todas as apps), marcada por este
// arquivo.
func (c *Client) SSLAutoRenew() bool {
	_, err := os.Stat(filepath.Join(c.lib(), "data", "letsencrypt", "--global", "autorenew"))
	return err == nil
}

func (c *Client) SetSSLAutoRenew(ctx context.Context, on bool) error {
	flag := "--remove"
	if on {
		flag = "--add"
	}
	_, err := c.Runner.Run(ctx, "dokku", "letsencrypt:cron-job", flag)
	return err
}

func (c *Client) Destroy(ctx context.Context, app string) error {
	_, err := c.Runner.Run(ctx, "dokku", "--force", "apps:destroy", app)
	return err
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Nomes de app aceitos pelo dokku (minúsculas, números e hífen).
var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ValidAppName(n string) bool { return appName.MatchString(n) }

// Rename roda apps:rename, que recria a app com o novo nome e faz o deploy.
func (c *Client) Rename(ctx context.Context, app, to string) error {
	if !ValidAppName(to) {
		return fmt.Errorf("nome inválido: %q", to)
	}
	_, err := c.Runner.Run(ctx, "dokku", "apps:rename", app, to)
	return err
}

// CreateApp roda apps:create (rápido, só registra a app).
func (c *Client) CreateApp(ctx context.Context, name string) error {
	if !ValidAppName(name) {
		return fmt.Errorf("nome inválido: %q", name)
	}
	_, err := c.Runner.Run(ctx, "dokku", "apps:create", name)
	return err
}

// SetPort mapeia http:80 para a porta do container (imagem sem EXPOSE ou
// com a porta errada).
func (c *Client) SetPort(ctx context.Context, app string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("porta inválida: %d", port)
	}
	_, err := c.Runner.Run(ctx, "dokku", "ports:set", app, "http:80:"+strconv.Itoa(port))
	return err
}

// SetConfigs grava várias variáveis num config:set só. Como SetConfig,
// o erro só cita os nomes, nunca os valores.
func (c *Client) SetConfigs(ctx context.Context, app string, vars map[string]string, restart bool) error {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		if !ValidConfigKey(k) {
			return fmt.Errorf("nome inválido: %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := []string{"config:set", "--encoded"}
	if !restart {
		args = append(args, "--no-restart")
	}
	args = append(args, app)
	for _, k := range keys {
		args = append(args, k+"="+b64(vars[k]))
	}
	if _, err := c.Runner.Run(ctx, "dokku", args...); err != nil {
		return &ConfigSetError{Keys: keys}
	}
	return nil
}

// Rebuild roda ps:rebuild (refaz o deploy da imagem atual).
func (c *Client) Rebuild(ctx context.Context, app string) error {
	_, err := c.Runner.Run(ctx, "dokku", "ps:rebuild", app)
	return err
}

// EnableSSL emite o certificado da app (letsencrypt:enable, sem --force).
func (c *Client) EnableSSL(ctx context.Context, app string) error {
	_, err := c.Runner.Run(ctx, "dokku", "letsencrypt:enable", app)
	if err == nil {
		c.Invalidate()
	}
	return err
}
