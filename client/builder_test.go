package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// builderRunner registra os comandos; ok diz quais binários "existem"
// (respondem), fail faz um comando falhar com a saída dada.
type builderRunner struct {
	calls []string
	ok    map[string]bool
	fail  map[string]string // prefixo do comando → saída
}

func (r *builderRunner) Run(_ context.Context, _ string, out io.Writer, name string, args ...string) error {
	line := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, line)
	for p, o := range r.fail {
		if strings.HasPrefix(line, p) {
			io.WriteString(out, o)
			return errors.New("exit status 1")
		}
	}
	if r.ok != nil && !r.ok[name] {
		return errors.New("not found")
	}
	return nil
}

func TestBuilderDockerAndPodmanCommands(t *testing.T) {
	r := &builderRunner{}
	d := Builder{Kind: "docker", Bin: "docker"}
	if err := d.BuildPush(context.Background(), r, "/repo", io.Discard, "linux/amd64", "ghcr.io/e/back:2.9.1", "/repo/api/Dockerfile", "/repo"); err != nil {
		t.Fatal(err)
	}
	p := Builder{Kind: "podman", Bin: "/opt/podman/bin/podman"}
	if err := p.BuildPush(context.Background(), r, "/repo", io.Discard, "linux/amd64", "ghcr.io/e/back:2.9.1", "", ""); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker buildx build --platform linux/amd64 -t ghcr.io/e/back:2.9.1 --push -f /repo/api/Dockerfile /repo",
		"/opt/podman/bin/podman build --platform linux/amd64 -t ghcr.io/e/back:2.9.1 .",
		"/opt/podman/bin/podman push ghcr.io/e/back:2.9.1",
	}
	if strings.Join(r.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(r.calls, "\n"))
	}
	// Build do podman falhou: não tenta o push.
	r = &builderRunner{fail: map[string]string{"podman build": "boom\n"}}
	p.Bin = "podman"
	if err := p.BuildPush(context.Background(), r, ".", io.Discard, "linux/amd64", "x/y:1", "", ""); err == nil || len(r.calls) != 1 {
		t.Fatalf("err %v calls %v", err, r.calls)
	}
}

func TestBuilderAuthHint(t *testing.T) {
	for _, b := range []Builder{{Kind: "podman", Bin: "podman"}, {Kind: "docker", Bin: "docker"}} {
		r := &builderRunner{fail: map[string]string{b.Bin + " push": "Error: denied: requested access to the resource is denied\n", "docker buildx": "ERROR: failed to push: unauthorized: authentication required\n"}}
		err := b.BuildPush(context.Background(), r, ".", io.Discard, "linux/amd64", "ghcr.io/e/back:2.9.1", "", "")
		if !errors.Is(err, ErrRegistryAuth) || !strings.Contains(err.Error(), "`"+b.Bin+" login ghcr.io`") {
			t.Errorf("%s: %v", b.Kind, err)
		}
	}
	// Erro que não é de login não ganha a dica.
	r := &builderRunner{fail: map[string]string{"docker": "no space left on device\n"}}
	err := Builder{Kind: "docker", Bin: "docker"}.BuildPush(context.Background(), r, ".", io.Discard, "p", "ghcr.io/e/b:1", "", "")
	if err == nil || errors.Is(err, ErrRegistryAuth) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuilderPrecedence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	run := &builderRunner{}
	check := func(flag, wantKind, wantBin, wantSource string) {
		t.Helper()
		b, err := ResolveBuilder(context.Background(), run, flag)
		if err != nil || b.Kind != wantKind || b.Bin != wantBin || b.Source != wantSource {
			t.Fatalf("flag %q: %+v %v; want %s %s %s", flag, b, err, wantKind, wantBin, wantSource)
		}
	}
	check("", "docker", "docker", "auto")
	if err := SaveUserConfig(UserConfig{Builder: "podman"}); err != nil {
		t.Fatal(err)
	}
	p, _ := UserConfigPath()
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 || filepath.Base(filepath.Dir(p)) != "dokk" {
		t.Fatalf("config %s: %v %v", p, fi, err)
	}
	check("", "podman", "podman", "config")
	t.Setenv("DOKK_BUILDER", "/opt/podman/bin/podman")
	check("", "podman", "/opt/podman/bin/podman", "env")
	check("docker", "docker", "docker", "flag")
	t.Setenv("DOKK_BUILDER", "/usr/local/bin/docker")
	check("", "docker", "/usr/local/bin/docker", "env")
	t.Setenv("DOKK_BUILDER", "buildah")
	if _, err := ResolveBuilder(context.Background(), run, ""); err == nil || !strings.Contains(err.Error(), "de env") {
		t.Fatalf("valor inválido aceito: %v", err)
	}
	var uc UserConfig
	if uc.Set("builder", "kaniko") == nil || uc.Set("cor", "x") == nil {
		t.Fatal("aceitou valor/chave inválidos")
	}
}

func TestBuilderAutoDetect(t *testing.T) {
	ctx := context.Background()
	// Docker com buildx.
	if b, err := ResolveBuilder(ctx, &builderRunner{ok: map[string]bool{"docker": true, "podman": true}}, "auto"); err != nil || b.Kind != "docker" {
		t.Fatalf("%+v %v", b, err)
	}
	// Docker sem buildx: cai no podman do PATH.
	r := &builderRunner{ok: map[string]bool{"docker": true, "podman": true}, fail: map[string]string{"docker buildx version": "unknown command\n"}}
	if b, err := ResolveBuilder(ctx, r, "auto"); err != nil || b.Kind != "podman" || b.Bin != "podman" {
		t.Fatalf("%+v %v", b, err)
	}
	// Só o podman do instalador do macOS.
	if b, err := ResolveBuilder(ctx, &builderRunner{ok: map[string]bool{"/opt/podman/bin/podman": true}}, "auto"); err != nil || b.Bin != "/opt/podman/bin/podman" {
		t.Fatalf("%+v %v", b, err)
	}
	// Nada: erro claro.
	if _, err := ResolveBuilder(ctx, &builderRunner{ok: map[string]bool{}}, "auto"); !errors.Is(err, ErrNoBuilder) || !strings.Contains(err.Error(), "dokk config set builder") {
		t.Fatalf("err = %v", err)
	}
}

func TestReleaseWithPodman(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	run := &fakeRunner{remote: "origin/main"}
	c := &Client{URL: srv.URL, Token: "t"}
	var log strings.Builder
	res, err := c.Release(context.Background(), ReleaseOptions{App: "web", Builder: "podman", Runner: run, Log: &log})
	if err != nil || res.Builder != "podman" {
		t.Fatalf("%+v %v", res, err)
	}
	var got []string
	for _, cl := range run.calls {
		if strings.HasPrefix(cl.args, "podman") {
			got = append(got, cl.args)
		}
	}
	if strings.Join(got, "|") != "podman build --platform linux/amd64 -t ghcr.io/acme/web:v1.0.1 .|podman push ghcr.io/acme/web:v1.0.1" {
		t.Fatalf("podman = %v", got)
	}
	if !strings.Contains(log.String(), "==> Build e push (podman)") {
		t.Fatalf("passo sem o builder: %s", log.String())
	}
}
