package dokku

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeState cria os arquivos que o dokku guarda em DOKKU_LIB_ROOT.
func writeState(t *testing.T, root, app, scale, restore string, locked bool) {
	t.Helper()
	ps := filepath.Join(root, "config", "ps", app)
	must(t, os.MkdirAll(ps, 0o755))
	must(t, os.WriteFile(filepath.Join(ps, "scale"), []byte(scale), 0o644))
	must(t, os.WriteFile(filepath.Join(ps, "restore"), []byte(restore), 0o644))
	if locked {
		dir := filepath.Join(root, "data", "apps", app)
		must(t, os.MkdirAll(dir, 0o755))
		must(t, os.WriteFile(filepath.Join(dir, ".deploy.lock"), nil, 0o644))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newClient(t *testing.T, r Runner) *Client {
	c := NewClient(r)
	c.LibRoot = t.TempDir()
	return c
}

type fakeRunner map[string]string

func (f fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	key := name + " " + args[0]
	out, ok := f[key]
	if !ok {
		return "", fmt.Errorf("comando inesperado: %s %v", name, args)
	}
	return out, nil
}

func newFake() fakeRunner {
	return fakeRunner{
		"dokku apps:list": "=====> My Apps\njobs\napi\nidle\n",
		"dokku apps:report": `=====> api app information
       App deploy source:             git
       App deploy source metadata:    a1b2c3d
       App locked:                    false
=====> jobs app information
       App deploy source:             docker-image
       App deploy source metadata:    ghcr.io/acme/jobs:1.2.3
       App locked:                    true`,
		"dokku git:report": `=====> api git information
       Git sha:                       a1b2c3d
       Git last updated at:           1759515720
=====> jobs git information
       Git sha:                       ffff`,
		"dokku domains:report": `=====> api domains information
       Domains app vhosts:            api.dokk.dev www.dokk.dev
=====> jobs domains information
       Domains app vhosts:`,
		"dokku proxy:report": "=====> api proxy information\n       Proxy computed type:           nginx\n",
		"dokku certs:report": "=====> api ssl information\n       Ssl enabled:                   true\n",
		"docker ps": strings.Join([]string{
			"api web running Up 2 hours (healthy)", "api web running Up 2 hours", "api worker running Up 2 hours",
			"jobs worker running", "jobs worker exited",
		}, "\n"),
	}
}

func TestListApps(t *testing.T) {
	c := newClient(t, newFake())
	writeState(t, c.LibRoot, "jobs", "worker=2", "true", false)
	apps, err := c.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 || apps[0].Name != "api" || apps[1].Name != "idle" || apps[2].Name != "jobs" {
		t.Fatalf("apps = %+v", apps)
	}

	api := apps[0]
	if !api.Deployed || api.Locked || !api.SSL || api.GitSHA != "a1b2c3d" || api.ProxyType != "nginx" {
		t.Errorf("api = %+v", api)
	}
	if strings.Join(api.Domains, ",") != "api.dokk.dev,www.dokk.dev" {
		t.Errorf("domains = %v", api.Domains)
	}
	if *api.Processes["web"] != (Process{2, 2}) || *api.Processes["worker"] != (Process{1, 1}) {
		t.Errorf("processes = %+v", api.Processes)
	}
	if api.Checks.Status != StatusHealthy || api.LastDeployAt == nil {
		t.Errorf("checks = %+v, last deploy = %v", api.Checks, api.LastDeployAt)
	}

	idle := apps[1]
	if idle.Deployed || idle.Checks.Status != StatusUnknown || idle.Checks.LastCheckAt != nil {
		t.Errorf("idle = %+v", idle)
	}

	jobs := apps[2]
	if !jobs.Locked || len(jobs.Domains) != 0 || jobs.Checks.Status != StatusDegraded {
		t.Errorf("jobs = %+v", jobs)
	}
	if jobs.GitSHA != "ghcr.io/acme/jobs:1.2.3" {
		t.Errorf("jobs image = %q", jobs.GitSHA)
	}
}

func TestHealthStatus(t *testing.T) {
	cases := map[string]App{
		StatusUnknown:   {Deployed: false},
		StatusUnhealthy: {Deployed: true, Processes: map[string]*Process{"web": {0, 2}}},
	}
	for want, app := range cases {
		if got := healthStatus(app); got != want {
			t.Errorf("healthStatus(%+v) = %s, want %s", app, got, want)
		}
	}
}

func TestMonitorHistory(t *testing.T) {
	m := NewMonitor(newClient(t, newFake()), 0)
	for range HistorySize + 5 {
		m.refresh(context.Background())
	}
	close(m.refreshed)

	apps, err := m.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h := apps[0].Checks.History; len(h) != HistorySize || h[len(h)-1] != StatusHealthy {
		t.Errorf("history = %v", h)
	}
}

func TestTransitionStatuses(t *testing.T) {
	cases := []struct {
		name       string
		containers string
		restore    string
		locked     bool
		want       string
	}{
		{"parada com ps:stop", "app web exited Exited (0) 1 minute ago", "false", false, StatusSuspended},
		{"subindo após ps:start", "app web exited Exited (0) 1 minute ago", "true", true, StatusStarting},
		{"healthcheck ainda rodando", "app web running Up 3 seconds (health: starting)", "true", false, StatusStarting},
		{"ps:restart com antigo no ar", "app web running Up 2 hours", "true", true, StatusRestarting},
		{"crash loop do docker", "app web restarting Restarting (1) 2 seconds ago", "true", false, StatusRestarting},
		{"sendo removida", "app web removing Removal In Progress", "true", false, StatusRemoving},
		{"restart concluído, antigo parado", "app web running Up 5 seconds\napp web exited Exited (0)", "true", false, StatusHealthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := fakeRunner{"dokku apps:list": "app\n", "docker ps": tc.containers}
			for _, p := range bulkReports {
				r["dokku "+p+":report"] = ""
			}
			c := newClient(t, r)
			writeState(t, c.LibRoot, "app", "web=1", tc.restore, tc.locked)
			apps, err := c.ListApps(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := apps[0].Checks.Status; got != tc.want {
				t.Errorf("status = %s, want %s (processes %+v)", got, tc.want, apps[0].Processes)
			}
		})
	}
}
