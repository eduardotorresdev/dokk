package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Formato de tag por app: "v{version}" (padrão) ou, num monorepo,
// "back@{version}". A lista de releases da app são as tags que casam com o
// formato (com ou sem GitHub Release), ordenadas por semver.

const defaultTagFormat = "v{version}"

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

// appLink é o vínculo app → repo. No disco, versões antigas guardavam só a
// string do repo; loadLinks aceita os dois.
type appLink struct {
	Provider  string `json:"provider,omitempty"` // github (vazio, legado) ou gitlab
	Repo      string `json:"repo"`
	TagFormat string `json:"tagFormat,omitempty"`
}

func loadLinks(path string) (map[string]appLink, error) {
	raw := map[string]json.RawMessage{}
	if err := readJSON(path, &raw); err != nil {
		return nil, err
	}
	out := map[string]appLink{}
	for app, v := range raw {
		var repo string
		if json.Unmarshal(v, &repo) == nil {
			out[app] = appLink{Repo: repo}
			continue
		}
		var l appLink
		if err := json.Unmarshal(v, &l); err != nil {
			return nil, fmt.Errorf("github-apps.json: %s: %w", app, err)
		}
		out[app] = l
	}
	return out, nil
}

func validTag(tag string) bool {
	return tagRe.MatchString(tag) && !strings.Contains(tag, "..") && !strings.Contains(tag, "@{") && !strings.HasSuffix(tag, ".lock") && !strings.HasSuffix(tag, "/")
}

// validTagFormat: exatamente um {version} e, preenchido, um ref de tag válido.
func validTagFormat(f string) bool {
	return strings.Count(f, "{version}") == 1 && len(f) <= 100 &&
		validTag(strings.Replace(f, "{version}", "1.2.3", 1)) && validTag(strings.Replace(f, "{version}", "1.2.3-rc.1", 1))
}

// versionOf extrai a versão (semver sem "v") da tag, se casar com o formato.
func versionOf(format, tag string) (string, bool) {
	pre, suf, _ := strings.Cut(format, "{version}")
	if !strings.HasPrefix(tag, pre) || !strings.HasSuffix(tag, suf) || len(tag) < len(pre)+len(suf) {
		return "", false
	}
	v := tag[len(pre) : len(tag)-len(suf)]
	return v, versionRe.MatchString(v)
}

func matchesFormat(format, tag string) bool {
	_, ok := versionOf(format, tag)
	return ok
}

// versionLess compara semver (pré-release vem antes da estável).
func versionLess(a, b string) bool {
	ma, mb := versionRe.FindStringSubmatch(a), versionRe.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x < y
		}
	}
	switch {
	case ma[4] == mb[4]:
		return false
	case ma[4] == "":
		return false
	case mb[4] == "":
		return true
	}
	return ma[4] < mb[4]
}

type releaseJSON struct {
	Tag        string    `json:"tag"`
	Version    string    `json:"version"`
	Name       string    `json:"name"`
	SHA        string    `json:"sha"`
	URL        string    `json:"url"` // vazio quando é só tag, sem GitHub Release
	CreatedAt  time.Time `json:"createdAt"`
	Prerelease bool      `json:"prerelease"`
}

const maxPages = 20 // até 2000 refs/releases

// releases lista as tags do formato (maior versão primeiro), juntando os
// dados da GitHub Release quando existe.
func (g *github) releases(ctx context.Context, repo, format string) ([]releaseJSON, error) {
	tok, err := g.installationToken(ctx, repo)
	if err != nil {
		return nil, err
	}
	auth := "Bearer " + tok
	prefix, _, _ := strings.Cut(format, "{version}")

	byTag := map[string]*releaseJSON{}
	for page := 1; page <= maxPages; page++ {
		var refs []struct {
			Ref    string `json:"ref"`
			Object struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			} `json:"object"`
		}
		if err := g.call(ctx, "GET", fmt.Sprintf("/repos/%s/git/matching-refs/tags/%s?per_page=100&page=%d", repo, prefix, page), auth, nil, &refs); err != nil {
			return nil, err
		}
		for _, ref := range refs {
			tag := strings.TrimPrefix(ref.Ref, "refs/tags/")
			v, ok := versionOf(format, tag)
			if !ok {
				continue
			}
			rel := &releaseJSON{Tag: tag, Version: v, Name: tag, Prerelease: strings.Contains(v, "-")}
			if ref.Object.Type == "commit" {
				rel.SHA = ref.Object.SHA
			}
			byTag[tag] = rel
		}
		if len(refs) < 100 {
			break
		}
	}

	for page := 1; page <= maxPages; page++ {
		var rels []struct {
			TagName    string    `json:"tag_name"`
			Name       string    `json:"name"`
			HTMLURL    string    `json:"html_url"`
			CreatedAt  time.Time `json:"created_at"`
			Prerelease bool      `json:"prerelease"`
			Draft      bool      `json:"draft"`
		}
		if err := g.call(ctx, "GET", fmt.Sprintf("/repos/%s/releases?per_page=100&page=%d", repo, page), auth, nil, &rels); err != nil {
			return nil, err
		}
		for _, r := range rels {
			if rel, ok := byTag[r.TagName]; ok && !r.Draft {
				rel.Name, rel.URL, rel.CreatedAt, rel.Prerelease = r.Name, r.HTMLURL, r.CreatedAt, r.Prerelease
			}
		}
		if len(rels) < 100 {
			break
		}
	}

	out := make([]releaseJSON, 0, len(byTag))
	for _, r := range byTag {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out, nil
}
