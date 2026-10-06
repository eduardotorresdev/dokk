package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dokk/dokku"
)

// stdinRunner grava os comandos (e o stdin) e devolve sucesso.
type stdinRunner struct {
	cmds  [][]string
	stdin []string
}

func (r *stdinRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.stdin = append(r.stdin, "")
	return "", nil
}

func (r *stdinRunner) RunInput(_ context.Context, stdin, name string, args ...string) (string, error) {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.stdin = append(r.stdin, stdin)
	return "", nil
}

func registryMux(t *testing.T) (*http.ServeMux, *stdinRunner) {
	r := &stdinRunner{}
	client := dokku.NewClient(r)
	client.HomeRoot = t.TempDir() // sem .docker/config.json: lista vazia
	mux := http.NewServeMux()
	registryRoutes(mux, client, dokku.NewMonitor(client, time.Hour))
	return mux, r
}

func serve(mux *http.ServeMux, method, url, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
	return rec
}

func TestRegistryLoginValidation(t *testing.T) {
	for _, body := range []string{
		`{"server":"-H","username":"u","password":"p"}`,
		`{"server":"ghcr.io","username":"-u","password":"p"}`,
		`{"server":"ghcr.io","username":"a b","password":"p"}`,
		`{"server":"ghcr.io","username":"u","password":"p\nx"}`,
		`{"server":"ghcr.io","username":"u","password":""}`,
		`não é json`,
	} {
		mux, r := registryMux(t)
		rec := serve(mux, "POST", "/api/registries", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
		if len(r.cmds) != 0 {
			t.Errorf("%s: rodou %v", body, r.cmds)
		}
	}
}

func TestRegistryLoginPasswordOnStdin(t *testing.T) {
	mux, r := registryMux(t)
	rec := serve(mux, "POST", "/api/registries", `{"server":"ghcr.io","username":"eduardo","password":"s3cr3t"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Fatal("a resposta ecoou a senha")
	}
	if len(r.cmds) != 1 || r.cmds[0][1] != "registry:login" || r.stdin[0] != "s3cr3t" {
		t.Fatalf("chamadas: %v stdin %q", r.cmds, r.stdin)
	}
	for _, a := range r.cmds[0] {
		if strings.Contains(a, "s3cr3t") {
			t.Fatalf("senha nos argumentos: %v", r.cmds[0])
		}
	}
}

func TestRegistryLogoutUnknown(t *testing.T) {
	mux, r := registryMux(t)
	if rec := serve(mux, "DELETE", "/api/registries?server=ghcr.io", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(r.cmds) != 0 {
		t.Fatalf("rodou %v", r.cmds)
	}
}
