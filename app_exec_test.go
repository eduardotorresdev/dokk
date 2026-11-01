package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dokk/dokku"
)

func TestExecRoute(t *testing.T) {
	a, _ := newAuth(filepath.Join(t.TempDir(), "auth.json"))
	var got struct {
		mode, app, process string
		cmd                []string
	}
	run := func(ctx context.Context, mode, app, process string, cmd []string) (dokku.ExecResult, error) {
		got.mode, got.app, got.process, got.cmd = mode, app, process, cmd
		if cmd[0] == "boom" {
			return dokku.ExecResult{}, errors.New("dokku caiu")
		}
		return dokku.ExecResult{Output: "ok\n", ExitCode: 2}, nil
	}
	mux := http.NewServeMux()
	execRoutes(mux, a, func(_ context.Context, n string) (bool, error) { return n == "web", nil }, run)
	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}

	w := post("/api/apps/web/exec", `{"cmd":["ls","-la"]}`)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["output"] != "ok\n" || out["exitCode"] != float64(2) {
		t.Fatalf("exec = %d %s", w.Code, w.Body)
	}
	if got.mode != "enter" || got.process != "web" || !reflect.DeepEqual(got.cmd, []string{"ls", "-la"}) {
		t.Errorf("padrões = %+v", got)
	}
	post("/api/apps/web/exec", `{"cmd":"echo $HOME | wc -c","mode":"run","process":"worker.2"}`)
	if got.mode != "run" || got.process != "worker.2" || !reflect.DeepEqual(got.cmd, []string{"/bin/sh", "-c", "echo $HOME | wc -c"}) {
		t.Errorf("string/run = %+v", got)
	}

	for body, code := range map[string]int{
		`{"cmd":[]}`:                        400,
		`{"cmd":""}`:                        400,
		`{"cmd":["ls"],"mode":"shell"}`:     400,
		`{"cmd":["ls"],"process":"../x"}`:   400,
		`{"cmd":["ls"],"process":"web;rm"}`: 400,
		`{"cmd":["boom"]}`:                  502,
	} {
		if c := post("/api/apps/web/exec", body).Code; c != code {
			t.Errorf("%s = %d, quer %d", body, c, code)
		}
	}
	if c := post("/api/apps/nope/exec", `{"cmd":["ls"]}`).Code; c != 404 {
		t.Errorf("app inexistente = %d", c)
	}
}

func TestLogsSnapshot(t *testing.T) {
	var asked int
	logs := func(_ context.Context, app string, n int) ([]string, error) {
		asked = n
		return []string{"a", "b"}, nil
	}
	get := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		logsSnapshot(w, httptest.NewRequest("GET", "/api/apps/web/logs"+q, nil), logs, "web")
		return w
	}
	if w := get("?lines=50"); w.Code != 200 || asked != 50 || w.Body.String() != `{"lines":["a","b"]}`+"\n" {
		t.Fatalf("lines=50 = %d %s (%d)", w.Code, w.Body, asked)
	}
	if get("?lines=999999"); asked != maxLogLines {
		t.Errorf("teto = %d", asked)
	}
	if get(""); asked != defaultLogTop {
		t.Errorf("padrão = %d", asked)
	}
	if c := get("?lines=abc").Code; c != 400 {
		t.Errorf("inválido = %d", c)
	}
}
