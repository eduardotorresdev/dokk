package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNextVersion(t *testing.T) {
	tags := []string{"v1.2.3", "v1.10.0", "v2.0.0-rc.1", "latest", "v1.9.9"}
	cases := []struct{ bump, explicit, want string }{
		{"", "", "v1.10.1"},
		{"patch", "", "v1.10.1"},
		{"minor", "", "v1.11.0"},
		{"major", "", "v2.0.0"},
		{"major", "3.0.0-beta.1", "v3.0.0-beta.1"},
		{"", "v4.5.6", "v4.5.6"},
	}
	for _, c := range cases {
		got, err := NextVersion(tags, c.bump, c.explicit)
		if err != nil || got != c.want {
			t.Errorf("NextVersion(%q,%q) = %q, %v; want %q", c.bump, c.explicit, got, err, c.want)
		}
	}
	if got, _ := NextVersion(nil, "minor", ""); got != "v0.1.0" {
		t.Errorf("sem tags: %q", got)
	}
	if _, err := NextVersion(nil, "huge", ""); err == nil {
		t.Error("bump inválido aceito")
	}
	if _, err := NextVersion(nil, "", "1.2"); err == nil {
		t.Error("versão inválida aceita")
	}
}

type call struct {
	dir  string
	args string
}

type fakeRunner struct {
	mu       sync.Mutex
	calls    []call
	remote   string // saída de git branch -r --contains
	buildErr error
}

func (f *fakeRunner) Run(_ context.Context, dir string, out io.Writer, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call{dir, line})
	switch {
	case strings.HasPrefix(line, "git rev-parse"):
		io.WriteString(out, "abcdef1234567890\n")
	case strings.HasPrefix(line, "git branch -r"):
		io.WriteString(out, f.remote)
	case strings.HasPrefix(line, "docker"):
		io.WriteString(out, "#1 building\n")
		return f.buildErr
	}
	return nil
}

type fakeAPI struct {
	mu       sync.Mutex
	created  *NewRelease
	installs []string
	polls    int
	auth     string
}

func (f *fakeAPI) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps/web", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = r.Header.Get("Authorization")
		app := map[string]any{"name": "web", "deploy-source": "docker-image", "git-sha": "ghcr.io/acme/web:v1.0.0", "checks": map[string]any{"status": "healthy"}}
		var action any
		if len(f.installs) > 0 {
			f.polls++
			if f.polls < 3 {
				action = map[string]any{"name": "install", "running": true}
				app["checks"] = map[string]any{"status": "restarting"}
			} else {
				action = map[string]any{"name": "install", "running": false}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"app": app, "action": action})
	})
	mux.HandleFunc("GET /api/apps/web/github/releases", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"tag":"v1.0.0","sha":"x"}]`)
	})
	mux.HandleFunc("POST /api/apps/web/github/releases", func(w http.ResponseWriter, r *http.Request) {
		var nr NewRelease
		json.NewDecoder(r.Body).Decode(&nr)
		f.mu.Lock()
		f.created = &nr
		f.mu.Unlock()
		json.NewEncoder(w).Encode(CreatedRelease{Tag: nr.Tag, URL: "https://github.com/acme/web/releases/tag/" + nr.Tag})
	})
	mux.HandleFunc("POST /api/apps/web/install", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Image string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.installs = append(f.installs, b.Image)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestReleaseFullFlow(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	run := &fakeRunner{remote: "  origin/main\n"}
	var log strings.Builder
	c := &Client{URL: srv.URL, Token: "dokk_x"}
	res, err := c.Release(context.Background(), ReleaseOptions{
		App: "web", Bump: "minor", Dir: "/src", Notes: "n", Install: true,
		Runner: run, Log: &log, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err, log.String())
	}
	if res.Tag != "v1.1.0" || res.Image != "ghcr.io/acme/web:v1.1.0" || !res.Installed || res.SHA != "abcdef1234567890" {
		t.Fatalf("res = %+v", res)
	}
	if api.auth != "Bearer dokk_x" {
		t.Errorf("auth = %q", api.auth)
	}
	if api.created == nil || api.created.Tag != "v1.1.0" || api.created.SHA != res.SHA || api.created.Image != res.Image || api.created.Notes != "n" {
		t.Errorf("created = %+v", api.created)
	}
	if len(api.installs) != 1 || api.installs[0] != res.Image {
		t.Errorf("installs = %v", api.installs)
	}
	var build call
	for _, cl := range run.calls {
		if strings.HasPrefix(cl.args, "docker") {
			build = cl
		}
	}
	if build.dir != "/src" || build.args != "docker buildx build --platform linux/amd64 -t ghcr.io/acme/web:v1.1.0 --push ." {
		t.Errorf("build = %+v", build)
	}
	if !strings.Contains(log.String(), "#1 building") {
		t.Errorf("build output não foi para o log: %q", log.String())
	}
}

func TestReleaseRequiresPushedCommit(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	run := &fakeRunner{remote: ""}
	c := &Client{URL: srv.URL, Token: "t"}
	_, err := c.Release(context.Background(), ReleaseOptions{App: "web", Runner: run, Log: io.Discard})
	if !errors.Is(err, ErrNotPushed) || Advise(err) != AdvicePush {
		t.Fatalf("err = %v", err)
	}
	for _, cl := range run.calls {
		if strings.HasPrefix(cl.args, "docker") {
			t.Fatal("buildou sem o commit no remoto")
		}
	}
	if api.created != nil {
		t.Fatal("criou release")
	}
}

func TestReleaseBuildFailureStops(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	run := &fakeRunner{remote: "origin/main", buildErr: errors.New("boom")}
	c := &Client{URL: srv.URL, Token: "t"}
	if _, err := c.Release(context.Background(), ReleaseOptions{App: "web", Runner: run, Log: io.Discard}); err == nil {
		t.Fatal("esperava erro")
	}
	if api.created != nil {
		t.Fatal("criou release com build quebrado")
	}
}

func TestReleaseExistingTag(t *testing.T) {
	srv := (&fakeAPI{}).server(t)
	c := &Client{URL: srv.URL, Token: "t"}
	_, err := c.Release(context.Background(), ReleaseOptions{App: "web", Version: "1.0.0", Runner: &fakeRunner{}, Log: io.Discard})
	if !errors.Is(err, ErrTagExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"tag existe"}`)
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "t"}
	_, err := c.CreateRelease(context.Background(), "web", NewRelease{Tag: "v1"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 409 || ae.Message != "tag existe" {
		t.Fatalf("err = %#v", err)
	}
}

func TestWaitHealthyActionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"app":{"name":"web","checks":{"status":"unknown"}},"action":{"name":"install","running":false,"error":"pull falhou"}}`)
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "t"}
	err := c.WaitHealthy(context.Background(), "web", time.Millisecond, nil)
	if err == nil || !strings.Contains(err.Error(), "pull falhou") {
		t.Fatalf("err = %v", err)
	}
}
