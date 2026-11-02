package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Vínculo app ↔ repositório, independente do provedor (GitHub App ou
// GitLab OAuth). Rotas neutras /api/apps/{name}/repo[/releases]; as antigas
// /api/apps/{name}/github[/releases] ficam como alias (clientes antigos).

const (
	providerGitHub = "github"
	providerGitLab = "gitlab"
)

// repoProvider é o que as rotas de release precisam de um provedor.
type repoProvider interface {
	configured() bool
	access(ctx context.Context, repo string) error
	releases(ctx context.Context, repo, format string) ([]releaseJSON, error)
	createRelease(ctx context.Context, repo, tag, sha, image, notes string, prerelease bool) (string, error)
	repos(ctx context.Context) ([]ghRepo, error)
}

type providers struct {
	links *github // dono do github-apps.json (vínculos de todos os provedores)
	gh    *github
	gl    *gitlab
}

func (p providers) get(name string) repoProvider {
	switch name {
	case providerGitHub:
		return p.gh
	case providerGitLab:
		if p.gl != nil {
			return p.gl
		}
	}
	return nil
}

// normalizeRepo tira URL e ".git" e valida no formato do provedor.
func (p providers) normalizeRepo(provider, repo string) (string, bool) {
	repo = strings.Trim(strings.TrimSuffix(strings.TrimSpace(repo), ".git"), "/")
	switch provider {
	case providerGitHub:
		repo = strings.TrimPrefix(repo, "https://github.com/")
		return repo, repoRe.MatchString(repo) && !strings.Contains(repo, "..")
	case providerGitLab:
		if p.gl != nil {
			repo = strings.TrimPrefix(repo, p.gl.baseURL()+"/")
		}
		return repo, len(repo) <= 255 && gitlabRepoRe.MatchString(repo) && !strings.Contains(repo, "..")
	}
	return "", false
}

type repoJSON struct {
	Provider string `json:"provider"`
	FullName string `json:"fullName"`
	Private  bool   `json:"private"`
}

func repoRoutes(mux *http.ServeMux, p providers, appExists func(context.Context, string) (bool, error)) {
	upstream := func(w http.ResponseWriter, err error) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	notConfiguredKey := map[string]string{providerGitHub: "err.githubNotConfigured", providerGitLab: "err.gitlabNotConnected"}

	// GET /api/repos: repos de todos os provedores conectados. Um provedor
	// que falha não esconde os outros; só é 502 se todos falharem.
	mux.HandleFunc("GET /api/repos", func(w http.ResponseWriter, r *http.Request) {
		out := []repoJSON{}
		var firstErr error
		tried := 0
		for _, name := range []string{providerGitHub, providerGitLab} {
			if want := r.URL.Query().Get("provider"); want != "" && want != name {
				continue
			}
			prov := p.get(name)
			if prov == nil || !prov.configured() {
				continue
			}
			tried++
			list, err := prov.repos(r.Context())
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			for _, x := range list {
				out = append(out, repoJSON{name, x.FullName, x.Private})
			}
		}
		if tried > 0 && len(out) == 0 && firstErr != nil {
			upstream(w, firstErr)
			return
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Provider != out[j].Provider {
				return out[i].Provider < out[j].Provider
			}
			return strings.ToLower(out[i].FullName) < strings.ToLower(out[j].FullName)
		})
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	})

	// app resolve {name} contra as apps reais do Dokku.
	app := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		name := r.PathValue("name")
		ok, err := appExists(r.Context(), name)
		switch {
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
		case !ok:
			writeError(w, r, http.StatusNotFound, "err.appNotFound")
		}
		return name, err == nil && ok
	}
	// ready: repo vinculado + provedor pronto; 400 caso contrário.
	ready := func(w http.ResponseWriter, r *http.Request) (appLink, repoProvider, bool) {
		name, ok := app(w, r)
		if !ok {
			return appLink{}, nil, false
		}
		l := p.links.link(name)
		prov := p.get(l.Provider)
		switch {
		case l.Repo == "":
			writeError(w, r, http.StatusBadRequest, "err.noRepo")
			return l, nil, false
		case prov == nil || !prov.configured():
			writeError(w, r, http.StatusBadRequest, notConfiguredKey[l.Provider])
			return l, nil, false
		}
		return l, prov, true
	}
	// accessErr: sem acesso ao repo vira 400 com a dica do provedor; resto é 502.
	accessErr := func(w http.ResponseWriter, r *http.Request, provider string, err error) {
		switch {
		case provider == providerGitHub && ghStatus(err) == 404:
			writeError(w, r, http.StatusBadRequest, "err.githubNotInstalled")
		case provider == providerGitLab && (glStatus(err) == 404 || glStatus(err) == 403):
			writeError(w, r, http.StatusBadRequest, "err.gitlabNoAccess")
		case errors.Is(err, errGitLabNotConnected):
			writeError(w, r, http.StatusBadRequest, "err.gitlabNotConnected")
		default:
			upstream(w, err)
		}
	}

	linkJSON := func(ctx context.Context, name string) map[string]any {
		l := p.links.link(name)
		if l.Repo == "" {
			return map[string]any{"provider": nil, "repo": nil, "installed": false, "tagFormat": l.TagFormat}
		}
		installed := false
		if prov := p.get(l.Provider); prov != nil && prov.configured() {
			installed = prov.access(ctx, l.Repo) == nil
		}
		return map[string]any{"provider": l.Provider, "repo": l.Repo, "installed": installed, "tagFormat": l.TagFormat}
	}
	getLink := func(w http.ResponseWriter, r *http.Request) {
		name, ok := app(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, linkJSON(r.Context(), name))
	}
	putLink := func(w http.ResponseWriter, r *http.Request) {
		name, ok := app(w, r)
		if !ok {
			return
		}
		var body struct {
			Provider  string  `json:"provider"`
			Repo      string  `json:"repo"`
			TagFormat *string `json:"tagFormat"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		cur := p.links.link(name)
		provider := strings.ToLower(strings.TrimSpace(body.Provider))
		if provider == "" {
			provider = cur.Provider
		}
		if provider == "" || strings.HasSuffix(r.URL.Path, "/github") {
			provider = providerGitHub // a rota antiga é sempre GitHub
		}
		if p.get(provider) == nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidProvider")
			return
		}
		repo, valid := p.normalizeRepo(provider, body.Repo)
		if !valid {
			writeError(w, r, http.StatusBadRequest, "err.invalidRepo")
			return
		}
		format := cur.TagFormat
		if body.TagFormat != nil {
			format = strings.TrimSpace(*body.TagFormat)
			if format == "" {
				format = defaultTagFormat
			}
		}
		if !validTagFormat(format) {
			writeError(w, r, http.StatusBadRequest, "err.invalidTagFormat")
			return
		}
		if err := p.links.setLink(name, provider, repo, format); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, linkJSON(r.Context(), name))
	}
	deleteLink := func(w http.ResponseWriter, r *http.Request) {
		name, ok := app(w, r)
		if !ok {
			return
		}
		if err := p.links.setLink(name, "", "", ""); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"provider": nil, "repo": nil, "installed": false, "tagFormat": defaultTagFormat})
	}

	listReleases := func(w http.ResponseWriter, r *http.Request) {
		l, prov, ok := ready(w, r)
		if !ok {
			return
		}
		rels, err := prov.releases(r.Context(), l.Repo, l.TagFormat)
		if err != nil {
			accessErr(w, r, l.Provider, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, rels)
	}
	createRelease := func(w http.ResponseWriter, r *http.Request) {
		l, prov, ok := ready(w, r)
		if !ok {
			return
		}
		var body struct {
			Tag        string `json:"tag"`
			SHA        string `json:"sha"`
			Image      string `json:"image"`
			Notes      string `json:"notes"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		tag, sha, image := strings.TrimSpace(body.Tag), strings.TrimSpace(body.SHA), strings.TrimSpace(body.Image)
		switch {
		case !validTag(tag):
			writeError(w, r, http.StatusBadRequest, "err.invalidTag")
			return
		case !matchesFormat(l.TagFormat, tag):
			writeError(w, r, http.StatusBadRequest, "err.tagFormatMismatch", tag, l.TagFormat)
			return
		case !shaRe.MatchString(sha):
			writeError(w, r, http.StatusUnprocessableEntity, "err.shaNotFound")
			return
		case image == "" || len(image) > imageMax || strings.ContainsAny(image, " \t\n`"):
			writeError(w, r, http.StatusBadRequest, "err.invalidImage")
			return
		}
		u, err := prov.createRelease(r.Context(), l.Repo, tag, sha, image, body.Notes, body.Prerelease)
		switch {
		case errors.Is(err, errTagExists):
			writeError(w, r, http.StatusConflict, "err.tagExists", tag)
		case errors.Is(err, errSHAUnknown):
			writeError(w, r, http.StatusUnprocessableEntity, "err.shaNotFound")
		case err != nil:
			accessErr(w, r, l.Provider, err)
		default:
			writeJSON(w, http.StatusOK, map[string]string{"provider": l.Provider, "tag": tag, "url": u})
		}
	}

	for _, base := range []string{"/api/apps/{name}/repo", "/api/apps/{name}/github"} {
		mux.HandleFunc("GET "+base, getLink)
		mux.HandleFunc("PUT "+base, putLink)
		mux.HandleFunc("DELETE "+base, deleteLink)
		mux.HandleFunc("GET "+base+"/releases", listReleases)
		mux.HandleFunc("POST "+base+"/releases", createRelease)
	}
}
