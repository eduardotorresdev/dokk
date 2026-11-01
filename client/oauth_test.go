package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOAuth é um servidor de autorização: guarda o challenge do authorize
// e só emite o token se o verifier bater.
type fakeOAuth struct {
	mu        sync.Mutex
	challenge string
	redirect  string
	code      string
}

func (f *fakeOAuth) start(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": srv.URL + "/oauth/authorize",
			"token_endpoint":         "/oauth/token", // relativo de propósito
		})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		ok := r.Form.Get("grant_type") == "authorization_code" &&
			r.Form.Get("client_id") == ClientID &&
			r.Form.Get("code") == f.code &&
			r.Form.Get("redirect_uri") == f.redirect &&
			PKCEChallenge(r.Form.Get("code_verifier")) == f.challenge
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		io.WriteString(w, `{"access_token":"dokk_abc","token_type":"Bearer"}`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// browser simula o usuário autorizando: valida o authorize e chama o
// callback com o code (e o state recebido, ou outro se tamper).
func (f *fakeOAuth) browser(t *testing.T, tamper string) func(string) error {
	return func(auth string) error {
		u, err := url.Parse(auth)
		if err != nil {
			t.Error(err)
			return err
		}
		q := u.Query()
		if q.Get("response_type") != "code" || q.Get("client_id") != ClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
			t.Errorf("authorize inválido: %s", auth)
		}
		if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(q.Get("redirect_uri"), "/callback") {
			t.Errorf("redirect_uri = %s", q.Get("redirect_uri"))
		}
		f.mu.Lock()
		f.challenge, f.redirect, f.code = q.Get("code_challenge"), q.Get("redirect_uri"), "c0de"
		f.mu.Unlock()
		state := q.Get("state")
		if tamper != "" {
			state = tamper
		}
		go func() {
			resp, err := http.Get(q.Get("redirect_uri") + "?code=c0de&state=" + url.QueryEscape(state))
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestLoginPKCE(t *testing.T) {
	f := &fakeOAuth{}
	srv := f.start(t)
	var notified string
	tok, err := Login(context.Background(), LoginOptions{URL: srv.URL + "/", Open: f.browser(t, ""), Notify: func(u string) { notified = u }})
	if err != nil {
		t.Fatal(err)
	}
	if tok != "dokk_abc" {
		t.Errorf("token = %q", tok)
	}
	if !strings.HasPrefix(notified, srv.URL+"/oauth/authorize?") {
		t.Errorf("notify = %q", notified)
	}
}

func TestLoginRejectsWrongState(t *testing.T) {
	f := &fakeOAuth{}
	srv := f.start(t)
	_, err := Login(context.Background(), LoginOptions{URL: srv.URL, Open: f.browser(t, "forjado")})
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginTimeout(t *testing.T) {
	f := &fakeOAuth{}
	srv := f.start(t)
	_, err := Login(context.Background(), LoginOptions{URL: srv.URL, Open: func(string) error { return nil }, Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "tempo esgotado") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginNotDokk(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := Login(context.Background(), LoginOptions{URL: srv.URL}); err == nil {
		t.Fatal("esperava erro sem metadata")
	}
}

func TestCredentialsAndFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("DOKK_URL", "")
	t.Setenv("DOKK_TOKEN", "")
	if _, err := FromEnv(); err != ErrNotLoggedIn {
		t.Fatalf("sem nada: %v", err)
	}
	path, _ := CredentialsPath()
	if path != filepath.Join(dir, "dokk", "credentials.json") {
		t.Fatalf("path = %s", path)
	}
	c, _ := LoadCredentials(path)
	c.Set("https://a.example/", "tA")
	c.Set("b.example", "tB")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", st.Mode().Perm())
	}
	cl, err := FromEnv()
	if err != nil || cl.URL != "https://b.example" || cl.Token != "tB" {
		t.Fatalf("atual: %+v %v", cl, err)
	}
	t.Setenv("DOKK_URL", "https://a.example")
	if cl, _ := FromEnv(); cl == nil || cl.Token != "tA" {
		t.Fatalf("por DOKK_URL: %+v", cl)
	}
	t.Setenv("DOKK_TOKEN", "envtok")
	if cl, _ := FromEnv(); cl.Token != "envtok" {
		t.Fatalf("env ganha: %+v", cl)
	}
	c, _ = LoadCredentials(path)
	if got := c.Remove(""); got != "https://b.example" || c.Current != "https://a.example" {
		t.Fatalf("remove: %q current %q", got, c.Current)
	}
}

func TestExecAndLogs(t *testing.T) {
	var got ExecRequest
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/apps/web/exec", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"output":"hi\n","exitCode":3}`)
	})
	mux.HandleFunc("GET /api/apps/web/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("lines") != "50" {
			t.Errorf("lines = %q", r.URL.Query().Get("lines"))
		}
		io.WriteString(w, `["a","b"]`)
	})
	mux.HandleFunc("GET /api/apps/txt/logs", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "l1\nl2\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "t"}
	res, err := c.Exec(context.Background(), "web", ExecRequest{Cmd: []string{"echo", "hi"}})
	if err != nil || res.ExitCode != 3 || res.Output != "hi\n" {
		t.Fatalf("exec = %+v %v", res, err)
	}
	if got.Mode != "enter" || strings.Join(got.Cmd, " ") != "echo hi" {
		t.Errorf("req = %+v", got)
	}
	if _, err := c.Exec(context.Background(), "web", ExecRequest{Cmd: []string{"x"}, Mode: "host"}); err == nil {
		t.Error("mode inválido aceito")
	}
	if l, err := c.Logs(context.Background(), "web", 50); err != nil || l != "a\nb" {
		t.Errorf("logs = %q %v", l, err)
	}
	if l, _ := c.Logs(context.Background(), "txt", 0); l != "l1\nl2\n" {
		t.Errorf("logs txt = %q", l)
	}
}

func TestAppActionWaits(t *testing.T) {
	polls := 0
	posted := ""
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/apps/web/{action}", func(w http.ResponseWriter, r *http.Request) {
		posted = r.PathValue("action")
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /api/apps/web", func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls < 3 {
			io.WriteString(w, `{"app":{"checks":{"status":"restarting"}},"action":{"name":"restart","running":true}}`)
			return
		}
		io.WriteString(w, `{"app":{"checks":{"status":"healthy"}},"action":{"name":"restart","running":false}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "t"}
	if err := c.AppAction(context.Background(), "web", "restart", time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if posted != "restart" || polls < 3 {
		t.Errorf("posted %q polls %d", posted, polls)
	}
	if err := c.AppAction(context.Background(), "web", "destroy", time.Millisecond, nil); err == nil {
		t.Error("ação inválida aceita")
	}
}
