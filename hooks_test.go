package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dokk/client"
)

// repoWithHooks cria um repo git com dokk.yml para a app web.
func repoWithHooks(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "dokk.yml"), []byte("apps:\n  web:\n    hooks:\n      pre-install: ./backup.sh\n"), 0o644)
	return dir
}

type recHooks struct{ cmds, actions []string }

func (h *recHooks) run(_ context.Context, _ string, env []string, out io.Writer, cmd string) error {
	h.cmds = append(h.cmds, cmd)
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "ACTION="); ok {
			h.actions = append(h.actions, v)
		}
	}
	io.WriteString(out, "backup ok\n")
	return nil
}

func hookCLI(srv string, stdin string, h *recHooks) (*cli, *bytes.Buffer) {
	var errb bytes.Buffer
	tm := term{now: fixedNow}
	return &cli{
		stdout: io.Discard, stderr: &errb, stdin: strings.NewReader(stdin), out: tm, err: tm,
		runner: mcpFakeRunner{}, poll: time.Millisecond, hooks: h.run,
		newClient: func() (*client.Client, error) { return &client.Client{URL: srv, Token: "t"}, nil },
	}, &errb
}

func TestCLIInstallRunsHooksAndNoHooksNeedsConfirmation(t *testing.T) {
	dir := repoWithHooks(t)
	f := &fakePanel{}
	srv := f.server(t)

	h := &recHooks{}
	c, errb := hookCLI(srv.URL, "", h)
	if code := c.main(context.Background(), []string{"install", "web", "v1.1.0", "--dir", dir}); code != 0 {
		t.Fatalf("code %d: %s", code, errb)
	}
	if strings.Join(h.cmds, ",") != "./backup.sh" || !strings.Contains(errb.String(), "backup ok") {
		t.Fatalf("hooks %v saída %s", h.cmds, errb)
	}

	// --no-hooks sem confirmação: não instala.
	f.installed = ""
	h = &recHooks{}
	c, errb = hookCLI(srv.URL, "", h)
	if code := c.main(context.Background(), []string{"install", "web", "v1.1.0", "--dir", dir, "--no-hooks"}); code != 1 || f.installed != "" {
		t.Fatalf("instalou sem confirmar: code %d %q %s", code, f.installed, errb)
	}
	// Digitando o nome da app: pula os hooks.
	c, errb = hookCLI(srv.URL, "web\n", h)
	if code := c.main(context.Background(), []string{"install", "web", "v1.1.0", "--dir", dir, "--no-hooks"}); code != 0 || len(h.cmds) != 0 {
		t.Fatalf("code %d hooks %v %s", code, h.cmds, errb)
	}
	// --yes também confirma.
	c, _ = hookCLI(srv.URL, "", h)
	if code := c.main(context.Background(), []string{"install", "web", "v1.1.0", "--dir", dir, "--no-hooks", "--yes"}); code != 0 || len(h.cmds) != 0 {
		t.Fatalf("--yes: code %d hooks %v", code, h.cmds)
	}
}

func TestCLIRollback(t *testing.T) {
	dir := repoWithHooks(t)
	f := &fakePanel{}
	srv := f.server(t)
	h := &recHooks{}
	c, errb := hookCLI(srv.URL, "", h)
	if code := c.main(context.Background(), []string{"rollback", "web", "--dir", dir}); code != 0 {
		t.Fatalf("code %d: %s", code, errb)
	}
	if f.installed != "ghcr.io/acme/web:v0.9.0" || strings.Join(h.actions, ",") != "rollback" {
		t.Fatalf("installed %q actions %v", f.installed, h.actions)
	}
}

func TestMCPRollbackAndSkipHooks(t *testing.T) {
	dir := repoWithHooks(t)
	f := &fakePanel{}
	srv := f.server(t)
	h := &recHooks{}
	s := &mcpServer{client: &client.Client{URL: srv.URL, Token: "t"}, hooks: h.run}
	args, _ := json.Marshal(map[string]any{"app": "web", "dir": dir})
	msgs := mcpRun(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rollback_release","arguments":`+string(args)+`}}`)
	r := byID(msgs, 1)["result"].(map[string]any)
	txt := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if r["isError"] == true || !strings.Contains(txt, "Rollback de web: v1.0.0 → ghcr.io/acme/web:v0.9.0") || f.installed != "ghcr.io/acme/web:v0.9.0" {
		t.Fatalf("txt %q installed %q", txt, f.installed)
	}
	if strings.Join(h.actions, ",") != "rollback" {
		t.Fatalf("actions %v", h.actions)
	}

	h2 := &recHooks{}
	s = &mcpServer{client: &client.Client{URL: srv.URL, Token: "t"}, hooks: h2.run}
	args, _ = json.Marshal(map[string]any{"app": "web", "tag": "v1.0.0", "dir": dir, "skip_hooks": true})
	msgs = mcpRun(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"install_release","arguments":`+string(args)+`}}`)
	if r := byID(msgs, 1)["result"].(map[string]any); r["isError"] == true || len(h2.cmds) != 0 {
		t.Fatalf("skip_hooks: %v %v", r, h2.cmds)
	}

	// A description manda nunca usar skip_hooks sem o usuário pedir.
	for _, tl := range mcpTools {
		if tl["name"] == "install_release" || tl["name"] == "rollback_release" || tl["name"] == "create_release" {
			raw, _ := json.Marshal(tl)
			if !strings.Contains(string(raw), "NUNCA use sem o usuário pedir") {
				t.Errorf("%s sem o aviso do skip_hooks", tl["name"])
			}
		}
	}
}
