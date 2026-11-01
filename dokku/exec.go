package dokku

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// ExecMaxOutput limita a saída guardada de um comando (stdout+stderr).
const ExecMaxOutput = 1 << 20

// capWriter guarda até max bytes e descarta o resto, marcando o corte.
type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

// ExecArgs monta o argv do dokku: "enter" roda no container em execução do
// processo; "run" sobe um container novo.
func ExecArgs(mode, app, process string, cmd []string) []string {
	if mode == "run" {
		return append([]string{"dokku", "run", app}, cmd...)
	}
	return append([]string{"dokku", "enter", app, process}, cmd...)
}

// ExecResult é a saída combinada e o código de saída do comando.
type ExecResult struct {
	Output    string
	ExitCode  int
	Truncated bool
}

// Exec roda o comando dentro do container da app, sem TTY, com a saída
// limitada a ExecMaxOutput. Cancelar o ctx mata o grupo de processos inteiro
// (dokku passa por sudo/bash/docker). Código de saída diferente de zero não
// é erro: volta em ExitCode.
func (c *Client) Exec(ctx context.Context, mode, app, process string, cmd []string) (ExecResult, error) {
	argv := ExecArgs(mode, app, process, cmd)
	name, args := c.command(ctx, argv...)
	return runCapped(ctx, name, args)
}

func runCapped(ctx context.Context, name string, args []string) (ExecResult, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	out := &capWriter{max: ExecMaxOutput}
	c.Stdout, c.Stderr = out, out
	err := c.Run()
	res := ExecResult{Output: StripANSI(out.buf.String()), Truncated: out.truncated}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}

// Logs devolve as últimas n linhas do log da app, sem acompanhar.
func (c *Client) Logs(ctx context.Context, app string, n int) ([]string, error) {
	out, err := c.Runner.Run(ctx, "dokku", "logs", app, "-n", strconv.Itoa(n))
	if err != nil {
		return nil, err
	}
	lines := []string{}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l != "" {
			lines = append(lines, StripANSI(l))
		}
	}
	return lines, nil
}
