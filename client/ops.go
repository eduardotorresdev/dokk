package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ExecRequest é o corpo de POST /api/apps/{name}/exec. O comando roda
// dentro do container da app, nunca no host.
type ExecRequest struct {
	Cmd     []string `json:"cmd"`
	Mode    string   `json:"mode,omitempty"` // enter (padrão) | run
	Process string   `json:"process,omitempty"`
}

// ExecResult é a resposta do exec.
type ExecResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
}

// Exec roda um comando no container da app.
func (c *Client) Exec(ctx context.Context, name string, r ExecRequest) (ExecResult, error) {
	if len(r.Cmd) == 0 {
		return ExecResult{}, fmt.Errorf("comando vazio")
	}
	if r.Mode == "" {
		r.Mode = "enter"
	}
	if r.Mode != "enter" && r.Mode != "run" {
		return ExecResult{}, fmt.Errorf("mode inválido %q (enter ou run)", r.Mode)
	}
	data, err := c.Do(ctx, http.MethodPost, appPath(name, "exec"), r)
	if err != nil {
		return ExecResult{}, err
	}
	var out ExecResult
	err = json.Unmarshal(data, &out)
	return out, err
}

// Logs devolve as últimas linhas do log da app (modo não-streaming).
// Aceita resposta em texto, lista JSON de strings ou {"lines":[...]}.
func (c *Client) Logs(ctx context.Context, name string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	data, err := c.Do(ctx, http.MethodGet, appPath(name, "logs")+"?lines="+strconv.Itoa(lines), nil)
	if err != nil {
		return "", err
	}
	var list []string
	if json.Unmarshal(data, &list) == nil {
		return strings.Join(list, "\n"), nil
	}
	var obj struct {
		Lines []string `json:"lines"`
	}
	if json.Unmarshal(data, &obj) == nil && obj.Lines != nil {
		return strings.Join(obj.Lines, "\n"), nil
	}
	return string(data), nil
}

// AppAction dispara start, stop ou restart e acompanha até terminar
// (start/restart: até healthy).
func (c *Client) AppAction(ctx context.Context, name, action string, every time.Duration, progress func(string)) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("ação inválida %q", action)
	}
	if _, err := c.Do(ctx, http.MethodPost, appPath(name, action), nil); err != nil {
		return err
	}
	return c.WaitAction(ctx, name, every, action != "stop", progress)
}
