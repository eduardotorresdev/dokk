package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// URLs limpas: rotas da SPA recebem o index.html (link direto e F5 funcionam),
// arquivos reais saem do FileServer e o que não existe fora da SPA é 404.
func TestUIHandler(t *testing.T) {
	ui, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		t.Fatal(err)
	}
	h := uiHandler(ui)

	cases := []struct {
		path, wantType string
		wantStatus     int
		wantIndex      bool
	}{
		{"/", "text/html", 200, true},
		{"/apps/foo", "text/html", 200, true},
		{"/registries", "text/html", 200, true},
		{"/rota/que/nao/existe", "text/html", 200, true},
		{"/src/main.js", "javascript", 200, false},
		{"/assets/favicon.svg", "image/svg+xml", 200, false},
		{"/assets/manifest.webmanifest", "application/manifest+json", 200, false},
		{"/apps/meu.app", "text/html", 200, true},
		{"/src/nao-existe.js", "text/plain", 404, false},
		{"/src/pages/sem-extensao", "text/plain", 404, false},
		{"/assets/nao-existe.png", "text/plain", 404, false},
		{"/favicon.ico", "text/plain", 404, false},
		{"/src", "text/plain", 404, false},
		{"/src/", "text/plain", 404, false},
		{"/node_modules/shablon/", "text/plain", 404, false},
		{"/node_modules/shablon/index.js", "javascript", 200, false},
		{"/api", "text/plain", 404, false},
		{"/api/nao-existe", "text/plain", 404, false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
			if w.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, c.wantStatus)
			}
			if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, c.wantType) {
				t.Errorf("Content-Type = %q, want %q", ct, c.wantType)
			}
			if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", cc)
			}
			isIndex := strings.Contains(w.Body.String(), `<div id="app"></div>`)
			if isIndex != c.wantIndex {
				t.Errorf("index.html = %v, want %v", isIndex, c.wantIndex)
			}
		})
	}
}
