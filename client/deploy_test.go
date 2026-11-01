package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeDokk é um painel com uma app por imagem, as tags do registry e os
// installs pedidos.
type fakeDokk struct {
	mu       sync.Mutex
	image    string   // imagem atual
	registry []string // tags do registry, mais nova primeiro
	ghTags   []string // releases do GitHub
	format   string
	installs []string
	created  *NewRelease
	digestTo string   // tag que o servidor resolve para o digest
	events   []string // config e install, na ordem
}

func (f *fakeDokk) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps/pbs-back", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"app":    map[string]any{"name": "pbs-back", "deploy-source": "docker-image", "git-sha": f.image, "checks": map[string]any{"status": "healthy"}},
			"action": map[string]any{"name": "install", "running": false},
		})
	})
	mux.HandleFunc("GET /api/apps/pbs-back/releases", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		rels := []map[string]string{}
		for _, t := range f.registry {
			rels = append(rels, map[string]string{"tag": t, "image": "ghcr.io/e/pbs/back:" + t})
		}
		body := map[string]any{"image": "ghcr.io/e/pbs/back", "current": f.image, "releases": rels}
		if f.digestTo != "" {
			body["currentTag"] = f.digestTo
		}
		json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("GET /api/apps/pbs-back/github", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"repo": "e/pbs", "tagFormat": f.format})
	})
	mux.HandleFunc("GET /api/apps/pbs-back/github/releases", func(w http.ResponseWriter, r *http.Request) {
		rels := []map[string]string{}
		for _, t := range f.ghTags {
			rels = append(rels, map[string]string{"tag": t, "sha": "sha-" + t})
		}
		json.NewEncoder(w).Encode(rels)
	})
	mux.HandleFunc("POST /api/apps/pbs-back/github/releases", func(w http.ResponseWriter, r *http.Request) {
		var nr NewRelease
		json.NewDecoder(r.Body).Decode(&nr)
		f.mu.Lock()
		f.created = &nr
		f.mu.Unlock()
		json.NewEncoder(w).Encode(CreatedRelease{Tag: nr.Tag, URL: "u"})
	})
	mux.HandleFunc("PUT /api/apps/pbs-back/config/{key}", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Value   string
			Restart *bool
		}
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.events = append(f.events, fmt.Sprintf("config %s=%s restart=%v", r.PathValue("key"), b.Value, b.Restart != nil && *b.Restart))
		f.mu.Unlock()
		io.WriteString(w, "{}")
	})
	mux.HandleFunc("POST /api/apps/pbs-back/install", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Image string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.events = append(f.events, "install "+b.Image)
		f.installs = append(f.installs, b.Image)
		f.image = b.Image
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type hookCall struct {
	dir, command string
	env          map[string]string
}

type fakeHooks struct {
	calls []hookCall
	fail  map[string]error // por comando
}

func (h *fakeHooks) run(_ context.Context, dir string, env []string, out io.Writer, command string) error {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	h.calls = append(h.calls, hookCall{dir, command, m})
	fmt.Fprintf(out, "hook %s rodou\n", command)
	return h.fail[command]
}

func pbsConfig() *Config {
	cfg, _ := ParseConfig([]byte(pbsYAML), false)
	cfg.Root = "/repo"
	return cfg
}

func installOpts(h *fakeHooks, log io.Writer) InstallOptions {
	return InstallOptions{App: "pbs-back", Config: pbsConfig(), Hooks: h.run, Reporter: PlainReporter{log}, PollInterval: time.Millisecond}
}

func TestInstallRunsHooksWithEnv(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back@" + digest, digestTo: "2.9.0", registry: []string{"2.9.1", "2.9.0"}}
	srv := f.server(t)
	t.Setenv("FROM_PARENT", "kept")
	h := &fakeHooks{}
	var log strings.Builder
	c := &Client{URL: srv.URL, Token: "t"}
	r, err := c.InstallTag(context.Background(), "back@2.9.1", installOpts(h, &log))
	if err != nil {
		t.Fatal(err, log.String())
	}
	if !slices.Equal(f.installs, []string{"ghcr.io/e/pbs/back:2.9.1"}) || !r.Installed || !r.HooksRun {
		t.Fatalf("installs %v res %+v", f.installs, r)
	}
	if len(h.calls) != 1 {
		t.Fatalf("hooks = %+v (post-install vazio não roda)", h.calls)
	}
	call := h.calls[0]
	if call.dir != "/repo" || call.command != "ops/dokk/backup-pb.sh" {
		t.Fatalf("call = %+v", call)
	}
	want := map[string]string{
		"ACTION": "install", "APP": "pbs-back", "DOKK_URL": srv.URL,
		"FROM_IMAGE": "ghcr.io/e/pbs/back@" + digest, "TO_IMAGE": "ghcr.io/e/pbs/back:2.9.1",
		"FROM_VERSION": "2.9.0", "TO_VERSION": "2.9.1", "FROM_DIGEST": digest, "TO_DIGEST": "",
		"FROM_PARENT": "kept",
	}
	for k, v := range want {
		if call.env[k] != v {
			t.Errorf("%s = %q, want %q", k, call.env[k], v)
		}
	}
	if !strings.Contains(log.String(), "hook ops/dokk/backup-pb.sh rodou") {
		t.Errorf("saída do hook fora do progresso: %q", log.String())
	}
}

func TestPreInstallFailureAbortsWithoutTouchingTheApp(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back:2.9.0"}
	srv := f.server(t)
	h := &fakeHooks{fail: map[string]error{"ops/dokk/backup-pb.sh": errors.New("exit status 3")}}
	c := &Client{URL: srv.URL, Token: "t"}
	_, err := c.InstallTag(context.Background(), "2.9.1", installOpts(h, io.Discard))
	var he *HookError
	if !errors.As(err, &he) || he.Hook != "pre-install" || !strings.Contains(err.Error(), "pre-install (ops/dokk/backup-pb.sh)") {
		t.Fatalf("err = %v", err)
	}
	if len(f.installs) != 0 {
		t.Fatalf("instalou mesmo com o pre-install falhando: %v", f.installs)
	}
}

func TestPostInstallFailureMarksFailedAfterInstall(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back:2.9.0"}
	srv := f.server(t)
	cfg := pbsConfig()
	app := cfg.Apps["pbs-back"]
	app.Hooks.PostInstall = "./smoke.sh"
	cfg.Apps["pbs-back"] = app
	h := &fakeHooks{fail: map[string]error{"./smoke.sh": errors.New("exit status 1")}}
	c := &Client{URL: srv.URL, Token: "t"}
	o := installOpts(h, io.Discard)
	o.Config = cfg
	r, err := c.InstallTag(context.Background(), "2.9.1", o)
	var he *HookError
	if !errors.As(err, &he) || he.Hook != "post-install" {
		t.Fatalf("err = %v", err)
	}
	if !r.Installed || len(f.installs) != 1 {
		t.Fatalf("o install deveria ter acontecido: %+v %v", r, f.installs)
	}
	if len(h.calls) != 2 || h.calls[1].env["ACTION"] != "install" {
		t.Fatalf("calls = %+v", h.calls)
	}
}

func TestSkipHooks(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back:2.9.0"}
	srv := f.server(t)
	h := &fakeHooks{}
	c := &Client{URL: srv.URL, Token: "t"}
	o := installOpts(h, io.Discard)
	o.SkipHooks = true
	r, err := c.InstallTag(context.Background(), "2.9.1", o)
	if err != nil || len(h.calls) != 0 || r.HooksRun || len(f.installs) != 1 {
		t.Fatalf("err %v calls %v res %+v", err, h.calls, r)
	}
	// Sem dokk.yml, nenhum hook.
	o = installOpts(h, io.Discard)
	o.Config = nil
	if _, err := c.InstallTag(context.Background(), "2.9.0", o); err != nil || len(h.calls) != 0 {
		t.Fatalf("sem config: %v %v", err, h.calls)
	}
}

func TestRollback(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back:2.9.1", registry: []string{"2.10.0-sha.20261030120000.gabc1234", "2.9.1", "2.9.0", "2.8.5", "latest"}}
	srv := f.server(t)
	h := &fakeHooks{}
	c := &Client{URL: srv.URL, Token: "t"}
	r, err := c.Rollback(context.Background(), "", installOpts(h, io.Discard))
	if err != nil || r.Image != "ghcr.io/e/pbs/back:2.9.0" || r.Action != "rollback" {
		t.Fatalf("res %+v err %v", r, err)
	}
	if len(h.calls) != 1 || h.calls[0].env["ACTION"] != "rollback" || h.calls[0].env["FROM_VERSION"] != "2.9.1" || h.calls[0].env["TO_VERSION"] != "2.9.0" {
		t.Fatalf("hook = %+v", h.calls)
	}

	// Versão explícita.
	if r, err := c.Rollback(context.Background(), "2.8.5", installOpts(h, io.Discard)); err != nil || r.Image != "ghcr.io/e/pbs/back:2.8.5" {
		t.Fatalf("explícita: %+v %v", r, err)
	}

	// Por digest: a versão atual vem do registry.
	f.image, f.digestTo = "ghcr.io/e/pbs/back@"+digest, "2.9.0"
	if r, err := c.Rollback(context.Background(), "", installOpts(h, io.Discard)); err != nil || r.Image != "ghcr.io/e/pbs/back:2.8.5" {
		t.Fatalf("digest: %+v %v", r, err)
	}

	// Na menor versão não há para onde voltar.
	f.image, f.digestTo = "ghcr.io/e/pbs/back:2.8.5", ""
	if _, err := c.Rollback(context.Background(), "", installOpts(h, io.Discard)); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("err = %v", err)
	}
}

func TestPreviousAndCompareVersion(t *testing.T) {
	tags := []string{"2.9.0", "2.10.0", "2.9.10", "2.9.1-sha.1.gabc", "x"}
	if p, _ := PreviousVersion(tags, "2.10.0"); p != "2.9.10" {
		t.Errorf("prev 2.10.0 = %s", p)
	}
	if p, _ := PreviousVersion(tags, "2.9.11-sha.20261030.gabc"); p != "2.9.10" {
		t.Errorf("prev de prerelease = %s", p)
	}
	a, _ := parseVersion("1.0.0-sha.2")
	b, _ := parseVersion("1.0.0-sha.10")
	s, _ := parseVersion("1.0.0")
	if compareVersion(a, b) >= 0 || compareVersion(b, s) >= 0 || compareVersion(s, a) <= 0 {
		t.Error("ordem semver errada")
	}
}

func TestHashVersion(t *testing.T) {
	got := HashVersion([]string{"2.9.0", "2.8.1", "3.0.0-rc.1"}, time.Date(2026, 10, 30, 9, 5, 7, 0, time.FixedZone("BRT", -3*3600)), "abcdef1234567890")
	if got != "2.9.1-sha.20261030120507.gabcdef1" {
		t.Fatalf("got %s", got)
	}
}

// Bug real: back@1.17.0 no git, registry em 2.9.0 → o próximo parte de 2.9.0.
func TestReleaseVersionIsMaxOfGitAndRegistry(t *testing.T) {
	for _, tc := range []struct{ bump, want string }{{"", "back@2.9.1"}, {"minor", "back@2.10.0"}} {
		f := &fakeDokk{image: "ghcr.io/e/pbs/back@" + digest, format: "back@{version}", ghTags: []string{"back@1.17.0"}, registry: []string{"2.9.0", "2.8.0", "1.17.0"}}
		srv := f.server(t)
		run := &fakeRunner{remote: "origin/main"}
		c := &Client{URL: srv.URL, Token: "t"}
		res, err := c.Release(context.Background(), ReleaseOptions{App: "pbs-back", Bump: tc.bump, Runner: run, Log: io.Discard})
		if err != nil || res.Tag != tc.want || res.Image != "ghcr.io/e/pbs/back:"+strings.TrimPrefix(tc.want, "back@") || res.Prerelease {
			t.Fatalf("bump %q: %+v %v", tc.bump, res, err)
		}
	}
}

func TestReleaseHashVersionOutsideMain(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back:2.9.0", format: "back@{version}", ghTags: []string{"back@1.17.0"}, registry: []string{"2.9.0"}}
	srv := f.server(t)
	run := &fakeRunner{remote: "origin/feat-x", branch: "feat-x"}
	c := &Client{URL: srv.URL, Token: "t"}
	res, err := c.Release(context.Background(), ReleaseOptions{App: "pbs-back", Runner: run, Log: io.Discard})
	want := "back@2.9.1-sha.20261030120000.gabcdef1"
	if err != nil || res.Tag != want || !res.Prerelease || res.Branch != "feat-x" {
		t.Fatalf("%+v %v", res, err)
	}
	if f.created == nil || !f.created.Prerelease || f.created.Image != "ghcr.io/e/pbs/back:2.9.1-sha.20261030120000.gabcdef1" {
		t.Fatalf("created = %+v", f.created)
	}
	// --bump fora da main é recusado.
	if _, err := c.Release(context.Background(), ReleaseOptions{App: "pbs-back", Bump: "minor", Runner: run, Log: io.Discard}); err == nil || !strings.Contains(err.Error(), "só vale na branch padrão") {
		t.Fatalf("bump fora da main: %v", err)
	}
}

func TestReleaseBuildFromConfigAndInstallHooks(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back:2.9.0", format: "back@{version}", registry: []string{"2.9.0"}}
	srv := f.server(t)
	run := &fakeRunner{remote: "origin/main"}
	h := &fakeHooks{}
	c := &Client{URL: srv.URL, Token: "t"}
	res, err := c.Release(context.Background(), ReleaseOptions{
		App: "pbs-back", Dir: "/repo/api", Install: true, Config: pbsConfig(), Hooks: h.run,
		Runner: run, Log: io.Discard, PollInterval: time.Millisecond,
	})
	if err != nil || !res.Installed {
		t.Fatalf("%+v %v", res, err)
	}
	var build string
	for _, cl := range run.calls {
		if strings.HasPrefix(cl.args, "docker") {
			build = cl.args
		}
	}
	if build != "docker buildx build --platform linux/amd64 -t ghcr.io/e/pbs/back:2.9.1 --push -f /repo/api/Dockerfile /repo" {
		t.Fatalf("build = %q", build)
	}
	if len(h.calls) != 1 || h.calls[0].env["TO_VERSION"] != "2.9.1" || h.calls[0].env["FROM_VERSION"] != "2.9.0" {
		t.Fatalf("hooks = %+v", h.calls)
	}
}

func TestCurrentVersionByDigest(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back@" + digest, digestTo: "2.9.0"}
	srv := f.server(t)
	c := &Client{URL: srv.URL, Token: "t"}
	st, err := c.Status(context.Background(), "pbs-back")
	if err != nil || st.Version != "2.9.0" {
		t.Fatalf("%+v %v", st, err)
	}
	f.digestTo = ""
	if st, _ := c.Status(context.Background(), "pbs-back"); st.Version != "@sha256:0123456789ab" {
		t.Fatalf("digest curto = %q", st.Version)
	}
}

func TestInstallAppliesConfigBeforeInstall(t *testing.T) {
	f := &fakeDokk{image: "ghcr.io/e/pbs/back:2.9.0", ghTags: []string{"back@2.9.1"}}
	srv := f.server(t)
	cfg, err := ParseConfig([]byte(`apps:
  pbs-back:
    config: { PBS_BUILD_VERSION: "{version}", PBS_REF: "{tag} {sha} {image}" }
`), false)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{URL: srv.URL, Token: "t"}
	o := InstallOptions{App: "pbs-back", Config: cfg, Hooks: (&fakeHooks{}).run, Reporter: PlainReporter{io.Discard}, PollInterval: time.Millisecond}
	if _, err := c.InstallTag(context.Background(), "back@2.9.1", o); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"config PBS_BUILD_VERSION=2.9.1 restart=false",
		"config PBS_REF=back@2.9.1 sha-back@2.9.1 ghcr.io/e/pbs/back:2.9.1 restart=false",
		"install ghcr.io/e/pbs/back:2.9.1",
	}
	if !slices.Equal(f.events, want) {
		t.Fatalf("events = %q", f.events)
	}
}
