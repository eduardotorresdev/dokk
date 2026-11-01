package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdvise(t *testing.T) {
	cases := []struct {
		err  error
		want Advice
	}{
		{nil, AdviceNone},
		{ErrNotLoggedIn, AdviceLogin},
		{fmt.Errorf("x: %w", ErrNotPushed), AdvicePush},
		{&APIError{Status: 401}, AdviceBadToken},
		{&APIError{Status: 404}, AdviceAppNotFound},
		{&APIError{Status: 409, Path: "/api/apps/web/restart"}, AdviceBusy},
		{&APIError{Status: 409, Path: "/api/apps/web/github/releases"}, AdviceTagExists},
		{&APIError{Status: 422}, AdvicePush},
		{&APIError{Status: 400, Path: "/api/apps/web/github/releases"}, AdviceLinkRepo},
		{errors.New("outra"), AdviceNone},
	}
	for _, c := range cases {
		if got := Advise(c.err); got != c.want {
			t.Errorf("Advise(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	_, err := (&Client{URL: "http://127.0.0.1:1", Token: "t"}).Apps(context.Background())
	if Advise(err) != AdviceUnreachable {
		t.Errorf("rede: %v → %v", err, Advise(err))
	}
}

func TestInstallTagAndStatus(t *testing.T) {
	installed := ""
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps/web", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"app":{"name":"web","deploy-source":"docker-image","git-sha":"ghcr.io/acme/web:v1@sha256:abc","domains":["w.dev"],"ssl":true,"checks":{"status":"healthy"}}}`)
	})
	mux.HandleFunc("GET /api/apps/web/github/releases", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"tag":"v1","sha":"s1","url":"u1"}]`)
	})
	mux.HandleFunc("POST /api/apps/web/install", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		installed = string(b)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "t"}
	img, err := c.InstallTag(context.Background(), "web", "v2", time.Millisecond, nil)
	if err != nil || img != "ghcr.io/acme/web:v2" || installed != `{"image":"ghcr.io/acme/web:v2"}` {
		t.Fatalf("img %q err %v body %s", img, err, installed)
	}
	st, err := c.Status(context.Background(), "web")
	if err != nil || st.Version != "v1" || st.Commit != "s1" || st.App.URL() != "https://w.dev" {
		t.Fatalf("status = %+v %v", st, err)
	}
}
