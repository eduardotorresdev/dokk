package dokku

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetDetail(t *testing.T) {
	r := fakeRunner{
		"dokku ports:report": "=====> api ports information\n       Ports map:                     http:80:5000 https:443:5000\n",
		"dokku storage:report": `=====> api storage information
       Storage attachment 1 container path: /data
       Storage attachment 1 host path:      /var/lib/dokku/data/storage/api/data
       Storage attachment 1 readonly:       true`,
		"dokku certs:report":   "=====> api ssl information\n       Ssl enabled:                   true\n       Ssl hostnames:                 api.dokk.dev\n",
		"dokku config:export":  "",
		"dokku network:report": "=====> api network information\n       Network computed attach post create:  apps\n",
		"docker ps": `{"Names":"api.web.1.1791145321","State":"exited","Status":"Exited (0)","Labels":"com.dokku.process-type=web"}
{"Names":"api.web.1","State":"running","Status":"Up 2 hours","Image":"dokku/api:latest","Labels":"com.dokku.app-name=api,com.dokku.process-type=web"}`,
		"docker stats": `{"Name":"api.web.1","CPUPerc":"0.15%","MemUsage":"10MiB / 1GiB","MemPerc":"1%"}`,
	}
	c := newClient(t, r)
	dir := filepath.Join(c.LibRoot, "data", "builds", "api")
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "old.json"), []byte(`{"id":"old","status":"failed","started_at":"2026-01-01T00:00:00Z"}`), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "new.json"), []byte(`{"id":"new","status":"succeeded","started_at":"2026-02-01T00:00:00Z"}`), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "new.log"), []byte("\x1b[32m-----> ok\x1b[0m\n"), 0o644))

	d, err := c.GetDetail(context.Background(), "api", []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Ports) != 2 || len(d.Storage) != 1 || !d.Storage[0].Readonly || d.Storage[0].Container != "/data" {
		t.Errorf("ports/storage = %v %+v", d.Ports, d.Storage)
	}
	if !d.SSL.Enabled || d.Networks[0] != "apps" {
		t.Errorf("config/ssl = %v %+v", d.ConfigKeys, d.SSL)
	}
	if len(d.Containers) != 2 || d.Containers[0].Name != "api.web.1" || d.Containers[0].CPU != "0.15%" || !d.Containers[1].Retiring {
		t.Errorf("containers = %+v", d.Containers)
	}
	if len(d.Builds) != 2 || d.Builds[0].ID != "new" {
		t.Errorf("builds = %+v", d.Builds)
	}

	log, err := c.BuildLog("api", "new")
	if err != nil || log != "-----> ok\n" {
		t.Errorf("log = %q, %v", log, err)
	}
	if _, err := c.BuildLog("api", "../../etc/passwd"); err == nil {
		t.Error("BuildLog aceitou id com path traversal")
	}
}

// configRunner devolve o config:export de cada app pelo nome.
type configRunner struct {
	fakeRunner
	configs map[string]string
}

func (r configRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == "dokku" && args[0] == "config:export" {
		return r.configs[args[len(args)-1]], nil
	}
	return r.fakeRunner.Run(ctx, name, args...)
}

func TestDetailLinks(t *testing.T) {
	r := configRunner{
		fakeRunner: fakeRunner{
			"dokku ports:report": "", "dokku storage:report": "", "dokku certs:report": "", "dokku network:report": "",
			"docker ps": "", "docker stats": "",
		},
		configs: map[string]string{
			"app":    `{"DB_HOST":"db","DATABASE_URL":"mysql://u:p@db.web:3306/x","SECRET":"dbz"}`,
			"db":     `{"MYSQL_ROOT_PASSWORD":"x"}`,
			"worker": `{"API":"http://app:5000"}`,
		},
	}
	known := []string{"app", "db", "worker"}

	d, err := newClient(t, r).GetDetail(context.Background(), "app", known)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.ConfigKeys, ",") != "DATABASE_URL,DB_HOST,SECRET" {
		t.Errorf("config keys = %v", d.ConfigKeys)
	}
	if len(d.Services) != 1 || d.Services[0].App != "db" || strings.Join(d.Services[0].Vars, ",") != "DATABASE_URL,DB_HOST" {
		t.Errorf("services = %+v", d.Services)
	}
	if len(d.UsedBy) != 1 || d.UsedBy[0].App != "worker" {
		t.Errorf("used by = %+v", d.UsedBy)
	}
}
