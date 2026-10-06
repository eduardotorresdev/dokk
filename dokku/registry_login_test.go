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

// recRunner grava cada chamada (args e stdin) e devolve err.
type recRunner struct {
	calls []recCall
	err   error
}

type recCall struct {
	name  string
	args  []string
	stdin string
}

func (r *recRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, recCall{name, args, ""})
	return "", r.err
}

func (r *recRunner) RunInput(_ context.Context, stdin, name string, args ...string) (string, error) {
	r.calls = append(r.calls, recCall{name, args, stdin})
	return "", r.err
}

func TestNormalizeRegistry(t *testing.T) {
	for in, want := range map[string]string{
		"https://index.docker.io/v1/": "docker.io",
		"index.docker.io":             "docker.io",
		"registry-1.docker.io":        "docker.io",
		"DOCKER.IO":                   "docker.io",
		"https://ghcr.io/":            "ghcr.io",
		"host:5000":                   "host:5000",
	} {
		if got := NormalizeRegistry(in); got != want {
			t.Errorf("NormalizeRegistry(%q) = %q, esperado %q", in, got, want)
		}
	}
}

func TestRegistries(t *testing.T) {
	c := NewClient(&recRunner{})
	c.HomeRoot = t.TempDir()
	if list, err := c.Registries(); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("sem arquivo: %v %v", list, err)
	}
	must(t, os.MkdirAll(filepath.Join(c.HomeRoot, ".docker"), 0o700))
	must(t, os.WriteFile(filepath.Join(c.HomeRoot, ".docker", "config.json"), []byte(`{"auths":{
		"https://index.docker.io/v1/":{"auth":"`+b64("eduardo:x")+`"},
		"ghcr.io":{"auth":"`+b64("bot:y")+`"},
		"quay.io":{}}}`), 0o600))
	list, err := c.Registries()
	must(t, err)
	want := []Registry{{"docker.io", "eduardo", false}, {"ghcr.io", "bot", false}, {"quay.io", "", true}}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("registries: %+v", list)
	}
	if c.registryAuth("docker.io") != b64("eduardo:x") {
		t.Fatal("registryAuth não achou o login do Docker Hub")
	}
}

func TestRegistryLogin(t *testing.T) {
	r := &recRunner{}
	c := NewClient(r)
	must(t, c.RegistryLogin(context.Background(), "ghcr.io", "bot", "s3cr3t"))
	call := r.calls[0]
	if want := []string{"registry:login", "--global", "--password-stdin", "ghcr.io", "bot"}; !reflect.DeepEqual(call.args, want) {
		t.Fatalf("args: %v", call.args)
	}
	if call.stdin != "s3cr3t" || strings.Contains(strings.Join(call.args, " "), "s3cr3t") {
		t.Fatalf("senha fora do stdin: %+v", call)
	}
	r.err = errors.New("dokku registry:login: exit 1: senha s3cr3t recusada")
	err := c.RegistryLogin(context.Background(), "ghcr.io", "bot", "s3cr3t")
	if err == nil || strings.Contains(err.Error(), "s3cr3t") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("erro vazou a senha: %v", err)
	}
	if err := c.RegistryLogin(context.Background(), "ghcr.io", "-bot", "x"); err == nil {
		t.Fatal("usuário começando com - deveria falhar")
	}
	if _, err := NewClient(fakeRunner{}).runInput(context.Background(), "", "dokku"); err == nil {
		t.Fatal("runner sem stdin deveria falhar")
	}
}

func TestRegistryLogout(t *testing.T) {
	r := &recRunner{}
	must(t, NewClient(r).RegistryLogout(context.Background(), "https://ghcr.io/"))
	if want := []string{"registry:logout", "--global", "ghcr.io"}; !reflect.DeepEqual(r.calls[0].args, want) {
		t.Fatalf("args: %v", r.calls[0].args)
	}
}
