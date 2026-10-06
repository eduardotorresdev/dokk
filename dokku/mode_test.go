package dokku

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// callRunner grava cada comando e responde por prefixo da linha completa.
type callRunner struct {
	calls []string
	stdin []string
	out   map[string]string
}

func (r *callRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, line)
	for k, v := range r.out {
		if strings.HasPrefix(line, k) {
			return v, nil
		}
	}
	return "", errors.New("falhou: " + line)
}

func (r *callRunner) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	r.stdin = append(r.stdin, stdin)
	return r.Run(ctx, name, args...)
}

func noDokku(string) (string, error) { return "", errors.New("não achou") }

func hasDokku(string) (string, error) { return "/usr/bin/dokku", nil }

const inspectRunning = "docker inspect --format {{.State.Running}} dokku"

func TestModeDetection(t *testing.T) {
	cases := []struct {
		name  string
		force Mode
		look  func(string) (string, error)
		out   map[string]string
		want  Mode
	}{
		{"binário no host", ModeAuto, hasDokku, nil, ModeHost},
		{"container rodando", ModeAuto, noDokku, map[string]string{inspectRunning: "true\n"}, ModeDocker},
		{"container parado", ModeAuto, noDokku, map[string]string{inspectRunning: "false\n"}, ModeAuto},
		{"nada instalado", ModeAuto, noDokku, nil, ModeAuto},
		{"forçado docker", ModeDocker, hasDokku, nil, ModeDocker},
		{"forçado host", ModeHost, noDokku, map[string]string{inspectRunning: "true\n"}, ModeHost},
	}
	for _, c := range cases {
		r := &DokkuRunner{Base: &callRunner{out: c.out}, Force: c.force, LookPath: c.look}
		if got := r.Mode(context.Background()); got != c.want {
			t.Errorf("%s: modo %q, esperado %q", c.name, got, c.want)
		}
	}
}

func TestModeCacheAndReset(t *testing.T) {
	base := &callRunner{}
	r := &DokkuRunner{Base: base, LookPath: noDokku}
	// Ausente: não guarda, tenta de novo (o onboarding pode instalar depois).
	r.Mode(context.Background())
	base.out = map[string]string{inspectRunning: "true"}
	if got := r.Mode(context.Background()); got != ModeDocker {
		t.Fatalf("depois de subir o container: %q", got)
	}
	n := len(base.calls)
	r.Mode(context.Background())
	if len(base.calls) != n {
		t.Fatal("modo achado deveria ficar em cache")
	}
	r.Reset()
	r.LookPath = hasDokku
	if got := r.Mode(context.Background()); got != ModeHost {
		t.Fatalf("depois do Reset: %q", got)
	}
}

func TestDokkuRunnerWrapsCommands(t *testing.T) {
	ctx := context.Background()
	base := &callRunner{out: map[string]string{"": "ok"}}
	docker := &DokkuRunner{Base: base, Force: ModeDocker}
	docker.Run(ctx, "dokku", "apps:list")
	docker.Run(ctx, "docker", "ps", "--all")
	docker.RunInput(ctx, "segredo", "dokku", "registry:login", "--global", "--password-stdin", "ghcr.io", "eu")
	host := &DokkuRunner{Base: base, Force: ModeHost}
	host.Run(ctx, "dokku", "apps:list")
	host.RunInput(ctx, "chave\n", "dokku", "ssh-keys:add", "x")
	want := []string{
		"docker exec dokku dokku apps:list",
		"docker ps --all",
		"docker exec -i dokku dokku registry:login --global --password-stdin ghcr.io eu",
		"dokku apps:list",
		"dokku ssh-keys:add x",
	}
	if !reflect.DeepEqual(base.calls, want) {
		t.Fatalf("comandos:\n%s", strings.Join(base.calls, "\n"))
	}
	if !reflect.DeepEqual(base.stdin, []string{"segredo", "chave\n"}) {
		t.Fatalf("stdin: %q", base.stdin)
	}
}

func TestClientDockerMode(t *testing.T) {
	ctx := context.Background()
	base := &callRunner{out: map[string]string{"docker exec dokku dokku version": "dokku version 0.38.31\n"}}
	c := NewClient(&DokkuRunner{Base: base, Force: ModeDocker})
	if v, err := c.Version(ctx); err != nil || v != "0.38.31" {
		t.Fatalf("versão %q, %v", v, err)
	}
	if c.lib() != "/var/lib/dokku/var/lib/dokku" || c.home() != "/var/lib/dokku/home/dokku" {
		t.Fatalf("caminhos no host: %s %s", c.lib(), c.home())
	}
	// git e logs rodam dentro do container, com o caminho de lá.
	c.Versions(ctx, "api", "")
	if got := base.calls[len(base.calls)-1]; !strings.HasPrefix(got, "docker exec dokku git -c safe.directory=* -C /home/dokku/api log") {
		t.Fatalf("git: %s", got)
	}
	if name, args := c.command(ctx, "dokku", "logs", "api"); name != "docker" || !reflect.DeepEqual(args, []string{"exec", "dokku", "dokku", "logs", "api"}) {
		t.Fatalf("logs: %s %v", name, args)
	}

	// Runner sem detecção (testes, fakes) é sempre host.
	h := NewClient(fakeRunner{})
	if h.Mode(ctx) != ModeHost || h.lib() != "/var/lib/dokku" || h.home() != "/home/dokku" {
		t.Fatal("cliente host com caminhos trocados")
	}
}

func TestAddPluginList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plugin-list")
	must(t, os.WriteFile(p, []byte("postgres: https://github.com/dokku/dokku-postgres.git"), 0o644))
	must(t, addPluginList(p, "letsencrypt", letsencryptRepo))
	must(t, addPluginList(p, "letsencrypt", letsencryptRepo))
	b, _ := os.ReadFile(p)
	want := "postgres: https://github.com/dokku/dokku-postgres.git\nletsencrypt: " + letsencryptRepo + "\n"
	if string(b) != want {
		t.Fatalf("plugin-list:\n%s", b)
	}
}

func TestDockerPkgScript(t *testing.T) {
	cases := map[[2]string]string{
		{"ubuntu", "debian"}:                "get.docker.com",
		{"fedora", ""}:                      "get.docker.com",
		{"rocky", "rhel centos fedora"}:     "docker-ce.repo",
		{"almalinux", "rhel centos fedora"}: "docker-ce.repo",
		{"amzn", "centos rhel fedora"}:      "install docker",
		{"arch", ""}:                        "pacman",
		{"manjaro", "arch"}:                 "pacman",
		{"opensuse-leap", "suse opensuse"}:  "zypper",
		{"opensuse-tumbleweed", ""}:         "zypper",
		{"alpine", ""}:                      "apk add",
		{"linuxmint", "ubuntu debian"}:      "docker.io",
		{"void", ""}:                        "get.docker.com",
	}
	for in, want := range cases {
		if got := dockerPkgScript(in[0], in[1]); !strings.Contains(got, want) {
			t.Errorf("%v: %q sem %q", in, got, want)
		}
	}
}

func TestDokkuRunArgs(t *testing.T) {
	got := strings.Join(dokkuRunArgs("v0.38.31", "srv"), " ")
	for _, want := range []string{
		"--name dokku", "--restart unless-stopped", "DOKKU_HOSTNAME=srv",
		"DOKKU_HOST_ROOT=/var/lib/dokku/home/dokku", "DOKKU_LIB_HOST_ROOT=/var/lib/dokku/var/lib/dokku",
		"3022:22", "80:80", "443:443", "/var/lib/dokku:/mnt/dokku", "/var/run/docker.sock:/var/run/docker.sock",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("faltou %q em %s", want, got)
		}
	}
	if !strings.HasSuffix(got, " dokku/dokku:0.38.31") {
		t.Errorf("imagem: %s", got)
	}
}

func TestWaitHealthy(t *testing.T) {
	r := &seqRunner{outs: []string{"running starting", "running starting", "running healthy"}}
	var lines []string
	if err := waitHealthy(context.Background(), r, func(s string) { lines = append(lines, s) }, 0); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("linhas: %q", lines)
	}
	r = &seqRunner{outs: []string{"exited ", ""}}
	if err := waitHealthy(context.Background(), r, func(string) {}, 0); err == nil {
		t.Fatal("container parado deveria dar erro")
	}
}

type seqRunner struct{ outs []string }

func (r *seqRunner) Run(context.Context, string, ...string) (string, error) {
	out := r.outs[0]
	if len(r.outs) > 1 {
		r.outs = r.outs[1:]
	}
	return out, nil
}
