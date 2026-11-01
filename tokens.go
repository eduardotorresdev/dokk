package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Tokens de API para clientes fora do navegador (CLI, MCP): mandados como
// "Authorization: Bearer dokk_…". No disco (<data>/tokens.json) fica só o
// sha256 do token; o valor em claro aparece uma única vez, na criação.
const (
	tokenPrefix      = "dokk_"
	maxTokens        = 50
	maxTokenName     = 64
	lastUsedInterval = time.Minute // não regrava o arquivo a cada requisição
)

type apiToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hash       string     `json:"hash"` // hex(sha256(token))
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

type tokenStore struct {
	path string
	mu   sync.Mutex
	list []apiToken
	now  func() time.Time
}

func loadTokens(path string) (*tokenStore, error) {
	s := &tokenStore{path: path, now: time.Now}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.list); err != nil {
		return nil, err
	}
	return s, nil
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// save grava de forma atômica (temporário + rename). Chamar com mu travado.
func (s *tokenStore) save() error {
	b, _ := json.MarshalIndent(s.list, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// create devolve o token novo e o valor em claro (só agora).
func (s *tokenStore) create(name string) (apiToken, string, error) {
	raw := tokenPrefix + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	t := apiToken{ID: hex.EncodeToString(randomBytes(8)), Name: name, Hash: hashToken(raw), CreatedAt: s.now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, t)
	if err := s.save(); err != nil {
		s.list = s.list[:len(s.list)-1]
		return apiToken{}, "", err
	}
	return t, raw, nil
}

func (s *tokenStore) remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.list, func(t apiToken) bool { return t.ID == id })
	if i < 0 {
		return false, nil
	}
	old := slices.Clone(s.list)
	s.list = slices.Delete(s.list, i, i+1)
	if err := s.save(); err != nil {
		s.list = old
		return false, err
	}
	return true, nil
}

func (s *tokenStore) all() []apiToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.list)
}

func (s *tokenStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list)
}

// check compara o hash com todos os tokens em tempo constante e atualiza o
// lastUsedAt de quem bater.
func (s *tokenStore) check(raw string) bool {
	if s == nil || !strings.HasPrefix(raw, tokenPrefix) {
		return false
	}
	h := []byte(hashToken(raw))
	s.mu.Lock()
	defer s.mu.Unlock()
	found := -1
	for i, t := range s.list {
		if subtle.ConstantTimeCompare(h, []byte(t.Hash)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return false
	}
	now := s.now().UTC()
	if last := s.list[found].LastUsedAt; last == nil || now.Sub(*last) >= lastUsedInterval {
		s.list[found].LastUsedAt = &now
		if err := s.save(); err != nil {
			log.Printf("tokens: %v", err)
		}
	}
	return true
}

// bearer extrai o token do cabeçalho Authorization ("" se não houver).
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, tok, _ := strings.Cut(h, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(tok), true
}

type tokenJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func tokenRoutes(mux *http.ServeMux, a *auth, s *tokenStore) {
	// Criar e apagar exigem a sessão do navegador: um token não cria outro.
	cookieOnly := func(w http.ResponseWriter, r *http.Request) bool {
		if _, has := bearer(r); has || !a.cookieLoggedIn(r) {
			writeError(w, r, http.StatusForbidden, "err.cookieRequired")
			return false
		}
		return true
	}

	mux.HandleFunc("GET /api/tokens", func(w http.ResponseWriter, r *http.Request) {
		out := []tokenJSON{}
		for _, t := range s.all() {
			out = append(out, tokenJSON{t.ID, t.Name, t.CreatedAt, t.LastUsedAt})
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("POST /api/tokens", func(w http.ResponseWriter, r *http.Request) {
		if !cookieOnly(w, r) {
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		name := strings.TrimSpace(body.Name)
		if name == "" || len(name) > maxTokenName {
			writeError(w, r, http.StatusBadRequest, "err.invalidTokenName")
			return
		}
		if s.count() >= maxTokens {
			writeError(w, r, http.StatusConflict, "err.tooManyTokens", maxTokens)
			return
		}
		t, raw, err := s.create(name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"id": t.ID, "name": t.Name, "token": raw})
	})

	mux.HandleFunc("DELETE /api/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !cookieOnly(w, r) {
			return
		}
		ok, err := s.remove(r.PathValue("id"))
		switch {
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		case !ok:
			writeError(w, r, http.StatusNotFound, "err.tokenNotFound")
		default:
			writeJSON(w, http.StatusOK, map[string]string{})
		}
	})
}

// idOf devolve o id do token (sem mexer no lastUsedAt); "" se não existir.
func (s *tokenStore) idOf(raw string) string {
	if s == nil {
		return ""
	}
	h := []byte(hashToken(raw))
	s.mu.Lock()
	defer s.mu.Unlock()
	id := ""
	for _, t := range s.list {
		if subtle.ConstantTimeCompare(h, []byte(t.Hash)) == 1 {
			id = t.ID
		}
	}
	return id
}
