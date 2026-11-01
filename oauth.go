package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// OAuth 2.1 com PKCE para clientes nativos (RFC 8252): só redirect de
// loopback, só S256, cliente público (sem segredo). O token emitido é um
// apiToken comum ("<client_id> (OAuth)"), sem escopos nem expiração,
// revogável em Configurações. Os codes ficam só em memória.

const codeTTL = 60 * time.Second

var (
	clientIDRe  = regexp.MustCompile(`^[\x21-\x7e][\x20-\x7e]{0,99}$`)
	challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)       // base64url(sha256) sem padding
	verifierRe  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`) // RFC 7636
)

type authCode struct {
	clientID, redirect, challenge string
	exp                           time.Time
}

type oauthServer struct {
	auth  *auth
	mu    sync.Mutex
	codes map[string]authCode
	now   func() time.Time
}

// authzParams são os parâmetros da autorização, validados.
type authzParams struct {
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
	Challenge   string `json:"code_challenge"`
	Method      string `json:"code_challenge_method"`
	State       string `json:"state"`
	Type        string `json:"response_type"`
}

// loopbackRedirect: http://127.0.0.1|localhost|[::1]:<porta>/…, sem userinfo
// nem fragmento.
func loopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	switch h := u.Hostname(); h {
	case "localhost":
		return true
	default:
		ip := net.ParseIP(h)
		return ip != nil && ip.IsLoopback()
	}
}

// validate devolve a chave de erro ("" = ok). redirectOK diz se o redirect
// já foi validado (só então dá para devolver erro para ele).
func (p authzParams) validate() (key string, redirectOK bool) {
	if !clientIDRe.MatchString(p.ClientID) {
		return "err.oauthClientID", false
	}
	if !loopbackRedirect(p.RedirectURI) {
		return "err.oauthRedirect", false
	}
	if p.Type != "code" {
		return "err.oauthResponseType", true
	}
	if p.Method != "S256" || !challengeRe.MatchString(p.Challenge) {
		return "err.oauthChallenge", true
	}
	if len(p.State) > 512 {
		return "err.oauthState", true
	}
	return "", true
}

func paramsFrom(q url.Values) authzParams {
	return authzParams{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), Challenge: q.Get("code_challenge"),
		Method: q.Get("code_challenge_method"), State: q.Get("state"), Type: q.Get("response_type")}
}

// withQuery acrescenta parâmetros à query do redirect (mantendo os dele).
func withQuery(raw string, kv ...string) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" || kv[i] != "state" {
			q.Set(kv[i], kv[i+1])
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// publicOrigin é a origem como o cliente vê (atrás do nginx, pelos
// cabeçalhos X-Forwarded-*).
func publicOrigin(r *http.Request) string {
	proto := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		proto = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host, _, _ = strings.Cut(h, ",")
		host = strings.TrimSpace(host)
	}
	return proto + "://" + host
}

// Página de erro do /oauth/authorize na identidade do dokk (usa os tokens e
// a fonte do painel; o servidor tem os assets).
var oauthErrorPage = template.Must(template.New("e").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} - dokk</title>
<link rel="icon" href="/assets/favicon.svg" type="image/svg+xml">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600;700&family=Geist+Mono:wght@400;500&display=swap">
<link rel="stylesheet" href="/src/style.css">
</head>
<body class="oauth-status-body">
<main class="oauth-status">
<a class="oauth-status-brand" href="/"><img src="/assets/favicon.svg" alt="" width="36" height="36"><span>dokk</span></a>
<section class="oauth-status-card">
<div class="oauth-status-badge" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><path d="M12 7.5v5.5M12 16.5h.01"/></svg></div>
<h1>{{.Title}}</h1>
<p class="muted">{{.Msg}}</p>
<p class="oauth-status-hint muted">{{.Hint}}</p>
</section>
</main>
</body>
</html>`))

func (o *oauthServer) errorPage(w http.ResponseWriter, r *http.Request, key string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	oauthErrorPage.Execute(w, map[string]string{"Lang": requestLang(r), "Title": msg(r, "err.oauthTitle"), "Msg": msg(r, key), "Hint": msg(r, "err.oauthHint")})
}

func (o *oauthServer) issueCode(p authzParams) string {
	code := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	for k, c := range o.codes {
		if now.After(c.exp) {
			delete(o.codes, k)
		}
	}
	o.codes[code] = authCode{p.ClientID, p.RedirectURI, p.Challenge, now.Add(codeTTL)}
	return code
}

// takeCode tira o code do mapa (uso único, mesmo quando a troca falha).
func (o *oauthServer) takeCode(code string) (authCode, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	c, ok := o.codes[code]
	delete(o.codes, code)
	if !ok || o.now().After(c.exp) {
		return authCode{}, false
	}
	return c, true
}

func oauthRoutes(mux *http.ServeMux, a *auth, ui http.Handler) *oauthServer {
	o := &oauthServer{auth: a, codes: map[string]authCode{}, now: time.Now}

	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		origin := publicOrigin(r)
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                origin,
			"authorization_endpoint":                origin + "/oauth/authorize",
			"token_endpoint":                        origin + "/oauth/token",
			"code_challenge_methods_supported":      []string{"S256"},
			"grant_types_supported":                 []string{"authorization_code"},
			"response_types_supported":              []string{"code"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})

	// GET: parâmetros inválidos → página de erro (nunca redireciona para uma
	// URI não validada). Válidos → a SPA mostra o consentimento (e, sem
	// sessão, o guard dela manda para o login e volta para esta URL).
	mux.HandleFunc("GET /oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		p := paramsFrom(r.URL.Query())
		if key, _ := p.validate(); key != "" {
			o.errorPage(w, r, key)
			return
		}
		ui.ServeHTTP(w, r)
	})

	// POST (da tela de consentimento): exige cookie e o X-Dokk do sameOrigin.
	// Devolve para onde o navegador deve ir.
	mux.HandleFunc("POST /oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		if _, has := bearer(r); has || !a.cookieLoggedIn(r) || r.Header.Get("X-Dokk") != "1" {
			writeError(w, r, http.StatusUnauthorized, "err.unauthenticated")
			return
		}
		var body struct {
			authzParams
			Approve bool `json:"approve"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		p := body.authzParams
		key, redirectOK := p.validate()
		switch {
		case key != "" && !redirectOK:
			writeError(w, r, http.StatusBadRequest, key)
		case key != "":
			writeJSON(w, http.StatusOK, map[string]string{"redirect": withQuery(p.RedirectURI, "error", "invalid_request", "state", p.State)})
		case !body.Approve:
			writeJSON(w, http.StatusOK, map[string]string{"redirect": withQuery(p.RedirectURI, "error", "access_denied", "state", p.State)})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"redirect": withQuery(p.RedirectURI, "code", o.issueCode(p), "state", p.State)})
		}
	})

	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fail := func(code int, e, desc string) {
			writeJSON(w, code, map[string]string{"error": e, "error_description": desc})
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			fail(400, "invalid_request", "corpo inválido")
			return
		}
		f := r.PostForm
		if f.Get("grant_type") != "authorization_code" {
			fail(400, "unsupported_grant_type", "use authorization_code")
			return
		}
		code, verifier := f.Get("code"), f.Get("code_verifier")
		if code == "" || f.Get("client_id") == "" || f.Get("redirect_uri") == "" || !verifierRe.MatchString(verifier) {
			fail(400, "invalid_request", "code, client_id, redirect_uri e code_verifier são obrigatórios")
			return
		}
		c, ok := o.takeCode(code)
		if !ok {
			fail(400, "invalid_grant", "code inválido, expirado ou já usado")
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		computed := base64.RawURLEncoding.EncodeToString(sum[:])
		if c.clientID != f.Get("client_id") || c.redirect != f.Get("redirect_uri") || subtle.ConstantTimeCompare([]byte(computed), []byte(c.challenge)) != 1 {
			fail(400, "invalid_grant", "client_id, redirect_uri ou code_verifier não conferem")
			return
		}
		if a.tokens.count() >= maxTokens {
			fail(400, "invalid_request", "limite de tokens atingido; revogue algum em Configurações")
			return
		}
		name := c.clientID
		if len(name) > maxTokenName-8 {
			name = name[:maxTokenName-8]
		}
		_, raw, err := a.tokens.create(name + " (OAuth)")
		if err != nil {
			fail(500, "server_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"access_token": raw, "token_type": "Bearer"})
	})
	return o
}
