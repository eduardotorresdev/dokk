// Package dokku conversa com um servidor Dokku executando os comandos da CLI
// no próprio servidor e converte os relatórios em structs.
package dokku

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executa um comando (dokku, docker) e devolve o stdout.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// LocalRunner executa os binários na própria máquina.
type LocalRunner struct{}

func (LocalRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// InputRunner é um Runner que também manda dados pelo stdin (senhas e
// chaves não vão para a linha de comando, onde aparecem no ps).
type InputRunner interface {
	RunInput(ctx context.Context, stdin, name string, args ...string) (string, error)
}

func (LocalRunner) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runInput falha se o Runner não aceitar stdin.
func (c *Client) runInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	r, ok := c.Runner.(InputRunner)
	if !ok {
		return "", errors.New("runner sem suporte a stdin")
	}
	return r.RunInput(ctx, stdin, name, args...)
}
