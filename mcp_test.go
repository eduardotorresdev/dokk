package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dokk/client"
)

func mcpRun(t *testing.T, s *mcpServer, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	s.out = &out
	if s.log == nil {
		s.log = io.Discard
	}
	if err := s.serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdout com linha fora do protocolo: %q", sc.Text())
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func byID(msgs []map[string]any, id float64) map[string]any {
	for _, m := range msgs {
		if m["id"] == id {
			return m
		}
	}
	return nil
}

func TestMCPHandshakeAndList(t *testing.T) {
	msgs := mcpRun(t, &mcpServer{},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nope"}`,
	)
	if len(msgs) != 3 {
		t.Fatalf("msgs = %v", msgs)
	}
	init := byID(msgs, 1)["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" {
		t.Errorf("init = %v", init)
	}
	tools := byID(msgs, 2)["result"].(map[string]any)["tools"].([]any)
	names := []string{}
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "list_apps,app_status,list_releases,create_release,install_release,build_log,app_logs,start_app,stop_app,restart_app,exec,login" {
		t.Errorf("tools = %v", names)
	}
	if byID(msgs, 3)["error"] == nil {
		t.Error("método desconhecido sem erro")
	}
}

type mcpFakeRunner struct{}

func (mcpFakeRunner) Run(_ context.Context, _ string, out io.Writer, name string, args ...string) error {
	switch {
	case name == "git" && args[0] == "rev-parse":
		io.WriteString(out, "abc1234\n")
	case name == "git":
		io.WriteString(out, "origin/main\n")
	default:
		io.WriteString(out, "#1 step one\n#2 step two\n")
	}
	return nil
}

func TestMCPCreateReleaseProgress(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps/web", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"app":{"name":"web","deploy-source":"docker-image","git-sha":"acme/web:v0.1.0","checks":{"status":"healthy"}}}`)
	})
	mux.HandleFunc("GET /api/apps/web/github/releases", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"tag":"v0.1.0"}]`)
	})
	mux.HandleFunc("POST /api/apps/web/github/releases", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tag":"v0.1.1","url":"https://gh/r"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var stderr bytes.Buffer
	s := &mcpServer{client: &client.Client{URL: srv.URL, Token: "t"}, runner: mcpFakeRunner{}, log: &stderr}
	msgs := mcpRun(t, s,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"create_release","arguments":{"app":"web","dir":"/x"},"_meta":{"progressToken":"p1"}}}`)
	res := byID(msgs, 7)["result"].(map[string]any)
	if res["isError"] != false {
		t.Fatalf("res = %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Release v0.1.1 criada") || !strings.Contains(text, "acme/web:v0.1.1") {
		t.Errorf("text = %s", text)
	}
	progress := 0
	for _, m := range msgs {
		if m["method"] == "notifications/progress" {
			progress++
		}
	}
	if progress == 0 {
		t.Error("sem notificações de progresso")
	}
	if !strings.Contains(stderr.String(), "#2 step two") {
		t.Errorf("build não foi para stderr: %q", stderr.String())
	}
}

func TestMCPWithoutConfig(t *testing.T) {
	msgs := mcpRun(t, &mcpServer{clientErr: io.EOF},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_apps","arguments":{}}}`)
	if byID(msgs, 1)["result"].(map[string]any)["isError"] != true {
		t.Fatal("esperava isError")
	}
}

func TestInstallImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"app":{"name":"web","deploy-source":"docker-image","git-sha":"localhost:5000/acme/web:v1"}}`)
	}))
	defer srv.Close()
	img, err := (&client.Client{URL: srv.URL, Token: "t"}).ImageForTag(context.Background(), "web", "v2")
	if err != nil || img != "localhost:5000/acme/web:v2" {
		t.Fatalf("img = %q, %v", img, err)
	}
}

func TestReleaseCmdExitCodes(t *testing.T) {
	if r := runCmd(t, nil, false, "release"); r.code != 2 {
		t.Errorf("sem --app: code %d", r.code)
	}
	if r := runCmd(t, nil, false, "release", "--app", "web"); r.code != 1 {
		t.Errorf("sem login: code %d", r.code)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"app não encontrada"}`)
	}))
	defer srv.Close()
	if r := runCmd(t, srv, false, "release", "--app", "web"); r.code != 1 || !strings.Contains(r.stderr, "app não encontrada") || !strings.Contains(r.stderr, "dokk apps") {
		t.Errorf("code %d, stderr %q", r.code, r.stderr)
	}
}
