package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClientID é o client_id público da CLI/MCP (app nativo, sem segredo).
const ClientID = "dokk-cli"

// LoginOptions configura Login.
type LoginOptions struct {
	URL     string
	HTTP    *http.Client
	Open    func(authURL string) error // abre o navegador; erro = não abriu
	Notify  func(authURL string)       // chamado antes de abrir (para imprimir a URL)
	Timeout time.Duration              // espera do callback; padrão 5 min
}

type oauthMetadata struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// PKCEChallenge é o code_challenge S256 de um verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Login faz OAuth 2.1 authorization code + PKCE com callback em loopback
// e devolve o access token.
func Login(ctx context.Context, o LoginOptions) (string, error) {
	base := NormalizeURL(o.URL)
	if base == "" {
		return "", errors.New("URL do dokk obrigatória")
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}

	meta, err := fetchMetadata(ctx, hc, base)
	if err != nil {
		return "", err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	verifier, state := randomString(32), randomString(16)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("state do callback não confere; login abortado")
		case q.Get("error") != "":
			res.err = fmt.Errorf("autorização negada: %s %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = errors.New("callback sem code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, CallbackPage(false, res.err.Error()))
		} else {
			io.WriteString(w, CallbackPage(true, ""))
		}
		select {
		case done <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {PKCEChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	authURL := meta.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	if o.Notify != nil {
		o.Notify(authURL)
	}
	if o.Open != nil {
		o.Open(authURL) // falhou: o Notify já mostrou a URL
	}

	wctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	var res result
	select {
	case res = <-done:
	case <-wctx.Done():
		return "", fmt.Errorf("tempo esgotado esperando o login no navegador (%s)", o.Timeout)
	}
	if res.err != nil {
		return "", res.err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"client_id":     {ClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	json.Unmarshal(body, &tok)
	if resp.StatusCode != http.StatusOK || tok.AccessToken == "" {
		m := tok.Error
		if tok.Description != "" {
			m += ": " + tok.Description
		}
		if m == "" {
			m = strings.TrimSpace(string(body))
		}
		return "", fmt.Errorf("troca do code falhou (HTTP %d): %s", resp.StatusCode, m)
	}
	return tok.AccessToken, nil
}

func fetchMetadata(ctx context.Context, hc *http.Client, base string) (oauthMetadata, error) {
	var m oauthMetadata
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return m, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return m, fmt.Errorf("conectando em %s: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("%s não parece um dokk com login OAuth (metadata HTTP %d)", base, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("metadata OAuth inválido: %w", err)
	}
	if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
		return m, errors.New("metadata OAuth sem authorization_endpoint/token_endpoint")
	}
	// Endpoints relativos são resolvidos contra a base.
	b, _ := url.Parse(base + "/")
	for _, p := range []*string{&m.AuthorizationEndpoint, &m.TokenEndpoint} {
		if u, err := b.Parse(*p); err == nil {
			*p = u.String()
		}
	}
	return m, nil
}
