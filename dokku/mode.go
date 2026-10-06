package dokku

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Mode diz onde o Dokku roda: no host (binário dokku, instalado pelo
// bootstrap.sh) ou num container (imagem dokku/dokku, instalação via Docker
// para as distros que o bootstrap não cobre).
type Mode string

const (
	ModeAuto   Mode = ""
	ModeHost   Mode = "host"
	ModeDocker Mode = "docker"
)

// ParseMode aceita "", "auto", "host" e "docker".
func ParseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ModeAuto, true
	case "host":
		return ModeHost, true
	case "docker":
		return ModeDocker, true
	}
	return ModeAuto, false
}

// DokkuContainer é o nome do container do Dokku no modo docker (o mesmo da
// documentação oficial).
const DokkuContainer = "dokku"

// DockerDataRoot é o diretório do host montado em /mnt/dokku no container.
// Lá dentro o Dokku liga /home/dokku e /var/lib/dokku a subpastas dele, então
// o DOKKU_ROOT fica em DockerDataRoot/home/dokku e o DOKKU_LIB_ROOT em
// DockerDataRoot/var/lib/dokku.
const DockerDataRoot = "/var/lib/dokku"

// DokkuRunner troca `dokku ...` por `docker exec dokku dokku ...` quando o
// Dokku roda num container. Os outros comandos (docker, git) passam direto
// para Base.
type DokkuRunner struct {
	Base Runner
	// Force fixa o modo (flag -dokku-mode); ModeAuto detecta.
	Force Mode
	// LookPath troca o exec.LookPath nos testes.
	LookPath func(string) (string, error)

	mu     sync.Mutex
	cached Mode
}

// Mode devolve o modo do Dokku: Force, ou o detectado. Na detecção, o binário
// dokku no PATH vence; sem ele, vale o container "dokku" rodando. Sem nenhum
// dos dois devolve ModeAuto (Dokku ausente) e tenta de novo na próxima
// chamada, já que o onboarding pode instalar com o dokk no ar.
func (r *DokkuRunner) Mode(ctx context.Context) Mode {
	if r.Force != ModeAuto {
		return r.Force
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached != ModeAuto {
		return r.cached
	}
	look := r.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look("dokku"); err == nil {
		r.cached = ModeHost
		return r.cached
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := r.Base.Run(ctx, "docker", "inspect", "--format", "{{.State.Running}}", DokkuContainer)
	if err == nil && strings.TrimSpace(out) == "true" {
		r.cached = ModeDocker
	}
	return r.cached
}

// Reset esquece o modo detectado (depois de uma instalação).
func (r *DokkuRunner) Reset() {
	r.mu.Lock()
	r.cached = ModeAuto
	r.mu.Unlock()
}

// command monta a linha de comando real. stdin pede o -i do docker exec.
func (r *DokkuRunner) command(ctx context.Context, stdin bool, name string, args []string) (string, []string) {
	if name != "dokku" || r.Mode(ctx) != ModeDocker {
		return name, args
	}
	return dockerExec(stdin, append([]string{"dokku"}, args...))
}

func dockerExec(stdin bool, argv []string) (string, []string) {
	pre := []string{"exec"}
	if stdin {
		pre = append(pre, "-i")
	}
	return "docker", append(append(pre, DokkuContainer), argv...)
}

func (r *DokkuRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	name, args = r.command(ctx, false, name, args)
	return r.Base.Run(ctx, name, args...)
}

func (r *DokkuRunner) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	in, ok := r.Base.(InputRunner)
	if !ok {
		return "", errRunnerNoStdin
	}
	name, args = r.command(ctx, true, name, args)
	return in.RunInput(ctx, stdin, name, args...)
}

// moder é o Runner que sabe o modo do Dokku (o DokkuRunner).
type moder interface {
	Mode(ctx context.Context) Mode
}

// Mode devolve o modo do Dokku do cliente; Runner sem detecção é host.
func (c *Client) Mode(ctx context.Context) Mode {
	if m, ok := c.Runner.(moder); ok {
		if mode := m.Mode(ctx); mode != ModeAuto {
			return mode
		}
	}
	return ModeHost
}

// ResetMode refaz a detecção do modo na próxima chamada.
func (c *Client) ResetMode() {
	if r, ok := c.Runner.(*DokkuRunner); ok {
		r.Reset()
	}
}

// lib e home são o LibRoot e o HomeRoot vistos do host: no modo docker os
// dois ficam dentro do volume DockerDataRoot.
func (c *Client) lib() string { return c.hostPath(c.LibRoot) }

func (c *Client) home() string { return c.hostPath(c.HomeRoot) }

func (c *Client) hostPath(p string) string {
	if c.Mode(context.Background()) == ModeDocker {
		return filepath.Join(DockerDataRoot, p)
	}
	return p
}

// command devolve a linha de comando para rodar argv onde o Dokku está: no
// host, ou dentro do container no modo docker.
func (c *Client) command(ctx context.Context, argv ...string) (string, []string) {
	if c.Mode(ctx) == ModeDocker {
		return dockerExec(false, argv)
	}
	return argv[0], argv[1:]
}
