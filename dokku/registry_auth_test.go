package dokku

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChallenge(t *testing.T) {
	s, p := parseChallenge(`Bearer realm="https://gitlab.com/jwt/auth",service="container_registry",scope="repository:a/b:pull"`)
	if s != "bearer" || p["realm"] != "https://gitlab.com/jwt/auth" || p["service"] != "container_registry" || p["scope"] != "repository:a/b:pull" {
		t.Errorf("%s %v", s, p)
	}
	s, p = parseChallenge(`Basic realm=Registry`)
	if s != "basic" || p["realm"] != "Registry" {
		t.Errorf("%s %v", s, p)
	}
}

// Registry no estilo GitLab: token num realm em outro caminho, service
// próprio, Basic do login do dokku no pedido do token; lista paginada.
func TestRegistryWWWAuthenticate(t *testing.T) {
	registryScheme = "http"
	defer func() { registryScheme = "https" }()
	basic := base64.StdEncoding.EncodeToString([]byte("eduardo:glpat"))
	var srv *httptest.Server
	mode := "bearer"
	tokenCalls := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			switch mode {
			case "open":
				w.WriteHeader(200)
			case "basic":
				w.Header().Set("WWW-Authenticate", `Basic realm="reg"`)
				w.WriteHeader(401)
			default:
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/jwt/auth",service="container_registry"`, srv.URL))
				w.WriteHeader(401)
			}
		case r.URL.Path == "/jwt/auth":
			tokenCalls++
			q := r.URL.Query()
			if q.Get("service") != "container_registry" || q.Get("scope") != "repository:grp/sub/app:pull" || r.Header.Get("Authorization") != "Basic "+basic {
				w.WriteHeader(403)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "glt"})
		case r.URL.Path == "/v2/grp/sub/app/tags/list":
			ok := map[string]string{"bearer": "Bearer glt", "basic": "Basic " + basic, "open": ""}[mode]
			if r.Header.Get("Authorization") != ok {
				w.WriteHeader(401)
				return
			}
			var tags []string
			if r.URL.Query().Get("last") == "" {
				for i := range tagPageSize {
					tags = append(tags, fmt.Sprintf("0.0.%d", i))
				}
			} else {
				tags = []string{"2.0.0", "1.10.0"}
			}
			json.NewEncoder(w).Encode(map[string]any{"tags": tags})
		case strings.HasPrefix(r.URL.Path, "/v2/grp/sub/app/manifests/"):
			if r.Header.Get("Authorization") != "Bearer glt" {
				w.WriteHeader(401)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/2.0.0") {
				w.Header().Set("Docker-Content-Digest", "sha256:"+sha64)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".docker"), 0o700)
	cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": basic}}})
	os.WriteFile(filepath.Join(home, ".docker", "config.json"), cfg, 0o600)
	c := &Client{Runner: &recRunner{}, HomeRoot: home}
	ref := ImageRef{Registry: host, Repo: "grp/sub/app"}

	for _, m := range []string{"bearer", "basic", "open"} {
		mode = m
		rels, err := c.Releases(context.Background(), ref)
		if err != nil || len(rels) == 0 || rels[0].Tag != "2.0.0" || rels[0].Image != host+"/grp/sub/app:2.0.0" {
			t.Fatalf("%s: %v %v", m, err, rels)
		}
	}
	if tokenCalls != 1 {
		t.Errorf("pedidos de token = %d", tokenCalls)
	}

	mode = "bearer"
	ref.Digest = "sha256:" + sha64
	tag, err := c.TagForDigest(context.Background(), ref, []Release{{Tag: "1.10.0"}, {Tag: "2.0.0"}}, 5)
	if err != nil || tag != "2.0.0" {
		t.Errorf("TagForDigest = %q %v", tag, err)
	}

	// Sem login: o realm nega e o erro sobe.
	os.Remove(filepath.Join(home, ".docker", "config.json"))
	if _, err := c.Releases(context.Background(), ImageRef{Registry: host, Repo: "grp/sub/app"}); err == nil {
		t.Error("sem login deveria falhar")
	}
}
