package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dokk/dokku"
)

// waitJob espera o job terminar (roda numa goroutine).
func waitJob(t *testing.T, j *jobRunner) *Job {
	t.Helper()
	for i := 0; i < 200; i++ {
		if job, _, _, _ := j.get(0); job != nil && !job.Running {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job não terminou")
	return nil
}

func TestJobRunner(t *testing.T) {
	j := &jobRunner{logTo: filepath.Join(t.TempDir(), "onboarding.log")}
	release := make(chan struct{})
	job, ok := j.start("install", func(ctx context.Context, log, step func(string)) error {
		step("Instalando")
		for i := 0; i < maxJobLines+10; i++ {
			log(fmt.Sprint("linha ", i))
		}
		<-release
		return errors.New("falhou")
	})
	if !ok || job.ID != 1 || !job.Running {
		t.Fatalf("start: %+v %v", job, ok)
	}
	if _, ok := j.start("configure", func(context.Context, func(string), func(string)) error { return nil }); ok {
		t.Fatal("segundo start deveria falhar com um job rodando")
	}
	close(release)
	done := waitJob(t, j)
	if done.Error != "falhou" || done.FinishedAt.IsZero() || done.Step != "Instalando" {
		t.Fatalf("job final: %+v", done)
	}
	// maxJobLines+12 linhas no total (cabeçalho, linhas, erro).
	_, lines, next, truncated := j.get(0)
	if !truncated || len(lines) != maxJobLines || next != maxJobLines+12 {
		t.Fatalf("truncamento: %d linhas, next %d, %v", len(lines), next, truncated)
	}
	_, lines, next2, truncated := j.get(next - 2)
	if truncated || len(lines) != 2 || next2 != next || !strings.HasPrefix(lines[1], "!") {
		t.Fatalf("paginação: %v %d %v", lines, next2, truncated)
	}
	if job, ok := j.start("configure", func(context.Context, func(string), func(string)) error { return nil }); !ok || job.ID != 2 {
		t.Fatalf("novo job: %+v %v", job, ok)
	}
	if done := waitJob(t, j); done.Error != "" {
		t.Fatalf("job sem erro: %+v", done)
	}
}

func TestOnboardingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onboarding.json")
	s := loadOnboardingState(path)
	if s.done() {
		t.Fatal("estado novo já veio concluído")
	}
	if err := s.markDone(); err != nil {
		t.Fatal(err)
	}
	if !loadOnboardingState(path).done() {
		t.Fatal("done não persistiu")
	}
	if s.dokkuSeen() {
		t.Fatal("dokku-seen veio marcado sem ninguém marcar")
	}
	if err := s.markDokkuSeen(); err != nil {
		t.Fatal(err)
	}
	if got := loadOnboardingState(path); !got.dokkuSeen() || !got.done() {
		t.Fatalf("dokku-seen não persistiu ou apagou o done: %+v", got)
	}
}

type fakeRunner map[string]string

func (f fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	if out, ok := f[name+" "+args[0]]; ok {
		return out, nil
	}
	return "", fmt.Errorf("comando inesperado: %s %v", name, args)
}

func TestOnboardingInstallConflict(t *testing.T) {
	client := dokku.NewClient(fakeRunner{"dokku version": "dokku version 0.38.31\n"})
	mux := http.NewServeMux()
	newOnboarding(t.TempDir(), "", client, dokku.NewMonitor(client, time.Hour)).routes(mux)
	req := httptest.NewRequest("POST", "/api/onboarding/install", strings.NewReader("{}"))
	req.Header.Set("X-Dokk-Lang", "pt-BR")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "já está instalado") {
		t.Fatalf("install com dokku presente: %d %s", rec.Code, rec.Body)
	}
	// Sem cabeçalho de idioma, a mensagem sai em inglês.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/onboarding/install", strings.NewReader("{}")))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Dokku is already installed") {
		t.Fatalf("install sem idioma: %d %s", rec.Code, rec.Body)
	}
}

func TestSuggestedDomain(t *testing.T) {
	for host, want := range map[string]string{
		"167.172.131.141:7070": "167.172.131.141.sslip.io",
		"dokk.example.com":     "a.com",
		"[::1]:80":             "a.com",
		"127.0.0.1:7070":       "a.com", // túnel SSH
		"192.168.0.10":         "a.com",
		"10.0.0.5:7070":        "a.com",
		"0.0.0.0:7070":         "a.com",
	} {
		if got := suggestedDomain(host, []string{"a.com"}); got != want {
			t.Errorf("suggestedDomain(%q) = %q", host, got)
		}
	}
}

func TestValidEmail(t *testing.T) {
	for in, want := range map[string]bool{
		"voce@empresa.com": true,
		"a.b+c@x.io":       true,
		"":                 false,
		"-x@y.com":         false,
		"Nome <a@b.com>":   false,
		"a@b.com ":         false,
		"sem-arroba":       false,
		"a@b.com, c@d.com": false,
	} {
		if got := validEmail(in); got != want {
			t.Errorf("validEmail(%q) = %v, quer %v", in, got, want)
		}
	}
}

func TestOnboardingConfigureValidation(t *testing.T) {
	post := func(r dokku.Runner, body string) *httptest.ResponseRecorder {
		client := dokku.NewClient(r)
		mux := http.NewServeMux()
		newOnboarding(t.TempDir(), "", client, dokku.NewMonitor(client, time.Hour)).routes(mux)
		req := httptest.NewRequest("POST", "/api/onboarding/configure", strings.NewReader(body))
		req.Header.Set("X-Dokk-Lang", "pt-BR")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	installed := fakeRunner{"dokku version": "dokku version 0.38.31\n"}
	for body, want := range map[string]string{
		`{"domain":"-x.com"}`:                     "domínio inválido",
		`{"domain":"a b.com"}`:                    "domínio inválido",
		`{"ssh-key":"não é chave"}`:               "chave SSH inválida",
		`{"letsencrypt-email":"-x@y.com"}`:        "e-mail inválido",
		`{"letsencrypt-email":"Nome <a@b.com>"}`:  "e-mail inválido",
		`{}`:                                      "nada para configurar",
		`{"domain":"  ","letsencrypt-email":" "}`: "nada para configurar",
	} {
		rec := post(installed, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s (quer 400 %q)", body, rec.Code, rec.Body, want)
		}
	}
	if rec := post(fakeRunner{}, `{"domain":"apps.example.com"}`); rec.Code != http.StatusConflict {
		t.Errorf("sem dokku: %d %s", rec.Code, rec.Body)
	}
}
