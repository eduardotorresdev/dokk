package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dokk/client"
)

var update = flag.Bool("update", false, "regrava os golden files de testdata/cli")

func init() { time.Local = time.UTC } // goldens independentes do fuso da máquina

var fixedNow = func() time.Time { return time.Date(2026, 10, 31, 14, 5, 9, 0, time.UTC) }

// fakePanel é um painel dokk falso com duas apps, releases e install.
type fakePanel struct {
	mu        sync.Mutex
	installed string
	polls     int
	execCmd   []string
}

func (f *fakePanel) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	last := time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC).Format(time.RFC3339)
	webApp := func() map[string]any {
		img := "ghcr.io/acme/web:v1.0.0"
		if f.installed != "" {
			img = f.installed
		}
		return map[string]any{
			"name": "web", "deployed": true, "deploy-source": "docker-image", "git-sha": img,
			"domains": []string{"web.acme.dev"}, "ssl": true,
			"processes": map[string]any{"web": map[string]int{"running": 2, "total": 2}, "worker": map[string]int{"running": 0, "total": 1}},
			"checks":    map[string]any{"status": "healthy", "last-check-at": last},
		}
	}
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"email":"edu@acme.dev","lang":"pt-BR"}`)
	})
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{
			webApp(),
			map[string]any{"name": "api-com-um-nome-bem-comprido", "deploy-source": "docker-image", "git-sha": "registry.example.com/acme/api-service-with-long-name:v2.3.4", "domains": []string{"api.acme.dev"}, "checks": map[string]any{"status": "degraded"}},
			map[string]any{"name": "日本語", "deploy-source": "git", "git-sha": "abc", "checks": map[string]any{"status": "suspended"}},
			map[string]any{"name": "jobs", "deploy-source": "docker-image", "git-sha": "acme/jobs:v1", "action": "restart", "checks": map[string]any{"status": "restarting"}},
		})
	})
	mux.HandleFunc("GET /api/apps/web", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var action any
		app := webApp()
		if f.installed != "" {
			f.polls++
			action = map[string]any{"name": "install", "running": f.polls < 2}
			if f.polls < 2 {
				app["checks"] = map[string]any{"status": "restarting"}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"app": app, "action": action})
	})
	mux.HandleFunc("GET /api/apps/web/github/releases", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"tag":"v1.0.0","sha":"0123456789abcdef","url":"https://github.com/acme/web/releases/tag/v1.0.0","createdAt":"2026-10-30T12:00:00Z"},{"tag":"v0.9.0","sha":"fedcba9876543210","url":"https://github.com/acme/web/releases/tag/v0.9.0","createdAt":"2026-10-01T12:00:00Z","prerelease":true}]`)
	})
	mux.HandleFunc("POST /api/apps/web/github/releases", func(w http.ResponseWriter, r *http.Request) {
		var nr client.NewRelease
		json.NewDecoder(r.Body).Decode(&nr)
		json.NewEncoder(w).Encode(client.CreatedRelease{Tag: nr.Tag, URL: "https://github.com/acme/web/releases/tag/" + nr.Tag})
	})
	mux.HandleFunc("POST /api/apps/web/install", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Image string }
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.installed = b.Image
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("POST /api/apps/web/exec", func(w http.ResponseWriter, r *http.Request) {
		var b client.ExecRequest
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.execCmd = b.Cmd
		f.mu.Unlock()
		io.WriteString(w, "{\"output\":\"\\u001b[32mok\\u001b[0m\\n\",\"exitCode\":3}")
	})
	mux.HandleFunc("GET /api/apps/web/logs", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "\x1b[36mweb.1\x1b[0m started\nweb.1 listening :5000\n")
	})
	mux.HandleFunc("GET /api/apps/nope", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"app não encontrada"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type cliRun struct {
	stdout, stderr string
	code           int
}

func runCmd(t *testing.T, srv *httptest.Server, color bool, args ...string) cliRun {
	t.Helper()
	tm := term{now: fixedNow}
	if color {
		tm = term{prof: profTrue, width: 80, now: fixedNow}
	}
	var out, errb bytes.Buffer
	c := &cli{
		stdout: &out, stderr: &errb, stdin: strings.NewReader(""), out: tm, err: tm,
		open: func(string) error { return nil }, runner: mcpFakeRunner{}, poll: time.Millisecond,
		newClient: func() (*client.Client, error) {
			if srv == nil {
				return nil, client.ErrNotLoggedIn
			}
			return &client.Client{URL: srv.URL, Token: "t"}, nil
		},
	}
	code := c.main(context.Background(), args)
	norm := func(s string) string {
		if srv != nil {
			s = strings.ReplaceAll(s, srv.URL, "http://dokk.test")
		}
		return s
	}
	return cliRun{norm(out.String()), norm(errb.String()), code}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "cli", name+".golden")
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (rode go test -run TestCLIGolden -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s difere do golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestCLIGolden(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"apps", []string{"apps"}, 0},
		{"status", []string{"status", "web"}, 0},
		{"releases", []string{"releases", "web"}, 0},
		{"install", []string{"install", "web", "v1.1.0"}, 0},
		{"release", []string{"release", "--app", "web", "--bump", "minor", "--install"}, 0},
		{"whoami", []string{"whoami"}, 0},
		{"not-found", []string{"status", "nope"}, 1},
		{"usage", []string{"install", "web"}, 2},
	}
	for _, tc := range cases {
		for _, color := range []bool{false, true} {
			mode := "plain"
			if color {
				mode = "color"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				srv := (&fakePanel{}).server(t)
				r := runCmd(t, srv, color, tc.args...)
				if r.code != tc.code {
					t.Errorf("exit %d, want %d\nstderr: %s", r.code, tc.code, r.stderr)
				}
				if !color && strings.Contains(r.stdout+r.stderr, "\x1b") {
					t.Error("modo plain com ANSI")
				}
				golden(t, tc.name+"."+mode, "# stdout\n"+r.stdout+"# stderr\n"+r.stderr)
			})
		}
	}
	t.Run("not-logged-in", func(t *testing.T) {
		r := runCmd(t, nil, false, "apps")
		if r.code != 1 {
			t.Errorf("exit %d", r.code)
		}
		golden(t, "not-logged-in.plain", r.stderr)
	})
}

func TestCLIJSON(t *testing.T) {
	srv := (&fakePanel{}).server(t)
	for _, args := range [][]string{{"apps", "--json"}, {"status", "web", "--json"}, {"releases", "--json", "web"}, {"whoami", "--json"}, {"logs", "web", "--json"}, {"install", "web", "v2", "--json"}} {
		r := runCmd(t, srv, true, args...)
		if r.code != 0 || !json.Valid([]byte(r.stdout)) || strings.Contains(r.stdout, "\x1b") {
			t.Errorf("%v: code %d, stdout %q", args, r.code, r.stdout)
		}
	}
}

func TestCLIExecPropagatesExitCode(t *testing.T) {
	f := &fakePanel{}
	srv := f.server(t)
	r := runCmd(t, srv, false, "exec", "web", "--run", "--", "ls", "-la")
	if r.code != 3 {
		t.Errorf("exit %d", r.code)
	}
	if strings.Join(f.execCmd, " ") != "ls -la" {
		t.Errorf("cmd = %v", f.execCmd)
	}
	// sem "--": flags do comando remoto não são da CLI
	r = runCmd(t, srv, false, "exec", "web", "grep", "-v", "x")
	if r.code != 3 || strings.Join(f.execCmd, " ") != "grep -v x" {
		t.Errorf("exit %d cmd %v (%s)", r.code, f.execCmd, r.stderr)
	}
}

func TestCLILogsAndHelp(t *testing.T) {
	srv := (&fakePanel{}).server(t)
	r := runCmd(t, srv, false, "logs", "web", "-n", "5")
	if r.code != 0 || !strings.Contains(r.stdout, "web.1 listening") {
		t.Errorf("logs: %+v", r)
	}
	r = runCmd(t, srv, false, "help")
	for _, c := range []string{"apps", "status", "logs", "start", "stop", "restart", "exec", "releases", "install", "release", "build-log", "login", "logout", "whoami", "mcp"} {
		if !strings.Contains(r.stdout, " "+c+" ") {
			t.Errorf("help sem %s", c)
		}
	}
}

func TestParseInterleaved(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	n := fs.Int("n", 0, "")
	pos, rest, err := parse(fs, []string{"web", "-n", "5"}, 1)
	if err != nil || *n != 5 || len(pos) != 1 || pos[0] != "web" || len(rest) != 0 {
		t.Fatalf("pos %v rest %v n %d err %v", pos, rest, *n, err)
	}
}

func TestStripAndWidth(t *testing.T) {
	if got := client.StripANSI("\x1b[38;2;1;2;3mazul\x1b[0m \x1b]8;;http://x\x07link\x1b]8;;\x07\n\x1b[2K 50%\r100%"); got != "azul link\n100%" {
		t.Errorf("strip = %q", got)
	}
	if w := width("\x1b[1m日本語\x1b[0m ok"); w != 9 {
		t.Errorf("width = %d", w)
	}
	if got := truncate("日本語テキスト", 7); got != "日本語…" || width(got) != 7 {
		t.Errorf("truncate = %q (%d)", got, width(got))
	}
	if to256(0x4d, 0x84, 0xff) != 111 {
		t.Errorf("to256 = %d", to256(0x4d, 0x84, 0xff))
	}
}

func TestTableFitsWidth(t *testing.T) {
	var b bytes.Buffer
	tm := term{prof: profTrue, width: 40}
	tm.table(&b, []string{"a", "b"}, [][]cell{{plain("x"), {text: strings.Repeat("y", 100), style: tm.accent}}})
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if width(l) > 40 {
			t.Errorf("linha com %d colunas: %q", width(l), l)
		}
	}
	if !strings.Contains(b.String(), "…") {
		t.Error("sem reticências")
	}
}

func TestDetectTermFallbacks(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tm := detectTerm(&bytes.Buffer{}, env(map[string]string{"COLORTERM": "truecolor"}))
	if tm.prof != profNone || tm.spinner {
		t.Error("não-TTY com cor")
	}
	if tm.color(cAccent, false) != "" {
		t.Error("cor sem perfil")
	}
	if got := (term{prof: prof256}).color(cAccent, false); got != "\x1b[38;5;111m" {
		t.Errorf("256 = %q", got)
	}
	if got := (term{prof: profTrue}).color(cAccent, false); got != "\x1b[38;2;77;132;255m" {
		t.Errorf("true = %q", got)
	}
}

// fakeDokkOAuth: metadata + token, e uma API que exige o token emitido.
func fakeDokkOAuth(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	var srv *httptest.Server
	challenge := ""
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": srv.URL + "/oauth/authorize", "token_endpoint": srv.URL + "/oauth/token"})
	})
	mux.HandleFunc("GET /oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		challenge = r.URL.Query().Get("code_challenge")
		http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code=k&state="+url.QueryEscape(r.URL.Query().Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if client.PKCEChallenge(r.Form.Get("code_verifier")) != challenge || r.Form.Get("code") != "k" {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"access_token":"dokk_new","token_type":"Bearer"}`)
	})
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"email":"edu@acme.dev"}`)
	})
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dokk_new" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		io.WriteString(w, `[{"name":"web","deploy-source":"docker-image","git-sha":"acme/web:v1","domains":["web.acme.dev"],"checks":{"status":"healthy"}}]`)
	})
	mux.HandleFunc("POST /api/apps/web/exec", func(w http.ResponseWriter, r *http.Request) {
		var body client.ExecRequest
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(client.ExecResult{Output: strings.Join(body.Cmd, "|"), ExitCode: 0})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeBrowser segue o authorize como um navegador (com redirect).
func fakeBrowser(u string) error {
	go func() {
		if resp, err := http.Get(u); err == nil {
			resp.Body.Close()
		}
	}()
	return nil
}

func TestLoginCmdSavesCredential(t *testing.T) {
	srv := fakeDokkOAuth(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DOKK_URL", "")
	t.Setenv("DOKK_TOKEN", "")
	var errb bytes.Buffer
	c := &cli{stdout: io.Discard, stderr: &errb, stdin: strings.NewReader(srv.URL + "\n"), open: fakeBrowser, newClient: client.FromEnv, err: term{now: fixedNow}}
	if code := c.main(context.Background(), []string{"login"}); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	cl, err := client.FromEnv()
	if err != nil || cl.Token != "dokk_new" || cl.URL != srv.URL {
		t.Fatalf("FromEnv = %+v %v", cl, err)
	}
	if !strings.Contains(errb.String(), "/oauth/authorize?") || !strings.Contains(errb.String(), "edu@acme.dev") {
		t.Errorf("saída: %s", errb.String())
	}
	if code := c.main(context.Background(), []string{"logout"}); code != 0 {
		t.Fatal("logout")
	}
	if _, err := client.FromEnv(); err != client.ErrNotLoggedIn {
		t.Errorf("depois do logout: %v", err)
	}
}

func TestMCPLoginThenTools(t *testing.T) {
	srv := fakeDokkOAuth(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DOKK_URL", "")
	t.Setenv("DOKK_TOKEN", "")
	s := &mcpServer{clientErr: client.ErrNotLoggedIn, open: fakeBrowser}
	text := func(msgs []map[string]any, id float64) (string, bool) {
		r := byID(msgs, id)["result"].(map[string]any)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"] == true
	}
	msgs := mcpRun(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_apps","arguments":{}}}`)
	if txt, isErr := text(msgs, 1); !isErr || !strings.Contains(txt, "tool login") {
		t.Fatalf("sem login: %q", txt)
	}
	msgs = mcpRun(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"login","arguments":{"url":"`+srv.URL+`"}}}`)
	if txt, isErr := text(msgs, 2); isErr {
		t.Fatalf("login: %s", txt)
	}
	msgs = mcpRun(t, s,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_apps","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"exec","arguments":{"app":"web","command":"ls -la"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"exec","arguments":{"app":"web","command":["env"]}}}`)
	if txt, _ := text(msgs, 3); txt != " APP  STATUS     IMAGEM       URL\n web  ● healthy  acme/web:v1  http://web.acme.dev\n" {
		t.Errorf("list_apps = %q", txt)
	}
	if a, _ := text(msgs, 4); a != "exit code 0\nsh|-c|ls -la" {
		t.Errorf("exec = %q", a)
	}
	if b, _ := text(msgs, 5); b != "exit code 0\nenv" {
		t.Errorf("exec = %q", b)
	}
}

func TestMCPStripsANSIFromServer(t *testing.T) {
	srv := (&fakePanel{}).server(t)
	s := &mcpServer{client: &client.Client{URL: srv.URL, Token: "t"}}
	msgs := mcpRun(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"app_logs","arguments":{"app":"web"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"exec","arguments":{"app":"web","command":"x"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"app_status","arguments":{"app":"nope"}}}`)
	for _, id := range []float64{1, 2, 3} {
		raw, _ := json.Marshal(byID(msgs, id))
		if strings.Contains(string(raw), `\u001b`) {
			t.Errorf("id %v com ANSI: %s", id, raw)
		}
	}
	r := byID(msgs, 3)["result"].(map[string]any)
	txt := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if r["isError"] != true || !strings.Contains(txt, "list_apps") {
		t.Errorf("erro sem dica: %q", txt)
	}
}

func TestUILiveWindowAndFailureTail(t *testing.T) {
	var b bytes.Buffer
	u := newUIWith(&b, term{prof: profTrue, spinner: true, width: 60, now: fixedNow})
	u.Step("Build")
	w := u.Output()
	for i := 1; i <= 40; i++ {
		io.WriteString(w, "linha "+strconv.Itoa(i)+"\n")
	}
	u.mu.Lock()
	win := append([]string(nil), u.window...)
	u.mu.Unlock()
	if len(win) != windowLines || win[0] != "linha 35" || win[5] != "linha 40" {
		t.Errorf("janela = %v", win)
	}
	u.Done("", errors.New("boom"))
	out := b.String()
	i := strings.LastIndex(out, "✗")
	if i < 0 {
		t.Fatalf("sem ✗: %q", out)
	}
	after := client.StripANSI(out[i:])
	if !strings.Contains(after, "últimas 30 linhas") || !strings.Contains(after, "linha 11\n") || strings.Contains(after, "linha 10\n") || !strings.Contains(after, "linha 40") {
		t.Errorf("tail errado:\n%s", after)
	}
}
