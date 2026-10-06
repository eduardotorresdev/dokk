package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// prefs guarda as preferências da instalação (por ora só o idioma da UI) em
// <data>/prefs.json. Lang vazio = ainda não escolhido (a UI usa o navegador).
type prefs struct {
	path string
	mu   sync.Mutex
	Lang string `json:"lang"`
}

func loadPrefs(path string) *prefs {
	p := &prefs{path: path}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, p)
	}
	p.Lang = normalizeLang(p.Lang)
	return p
}

func (p *prefs) lang() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Lang
}

// setLang grava de forma atômica (temporário + rename), como o onboarding.
func (p *prefs) setLang(l string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.Lang
	p.Lang = l
	b, err := json.Marshal(p)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(p.path), 0o700)
	}
	tmp := p.path + ".tmp"
	if err == nil {
		err = os.WriteFile(tmp, b, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, p.path)
	}
	if err != nil {
		p.Lang = old
	}
	return err
}

// validLang aceita só os códigos exatos que a UI manda.
func validLang(l string) bool {
	for _, x := range langs {
		if l == x {
			return true
		}
	}
	return false
}

func prefsRoutes(mux *http.ServeMux, p *prefs) {
	mux.HandleFunc("PUT /api/prefs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Lang string `json:"lang"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil || !validLang(body.Lang) {
			writeError(w, r, http.StatusBadRequest, "err.invalidLang")
			return
		}
		if err := p.setLang(body.Lang); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"lang": body.Lang})
	})
}
