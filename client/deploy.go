package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dokk/dokku"
)

// RegistryList é GET /api/apps/{name}/releases: as tags do registry da
// imagem da app, a imagem atual e (quando ela é por digest) a tag que o
// servidor resolveu para esse digest.
type RegistryList struct {
	Image      string          `json:"image"`
	Current    string          `json:"current"`
	CurrentTag string          `json:"currentTag,omitempty"`
	Releases   []dokku.Release `json:"releases"`
	Error      string          `json:"error,omitempty"`
}

// RegistryReleases lista as tags do registry da imagem da app.
func (c *Client) RegistryReleases(ctx context.Context, app string) (RegistryList, error) {
	var l RegistryList
	err := c.getJSON(ctx, appPath(app, "releases"), &l)
	return l, err
}

// ---- versões -------------------------------------------------------------

var versionRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// version é um semver lido do fim de uma tag (v1.2.3, back@1.2.3, 1.2.3-sha.x).
type version struct {
	core [3]int
	pre  string
	tag  string
}

func parseVersion(tag string) (version, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return version{}, false
	}
	var v version
	for i := range 3 {
		v.core[i], _ = strconv.Atoi(m[i+1])
	}
	v.pre, v.tag = m[4], tag
	return v, true
}

// compareVersion segue o semver: núcleo, depois sem prerelease > com, e
// prereleases por identificador (numéricos por valor).
func compareVersion(a, b version) int {
	for i := range 3 {
		if a.core[i] != b.core[i] {
			return a.core[i] - b.core[i]
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	pa, pb := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil && na != nb:
			return na - nb
		case ea == nil && eb != nil:
			return -1
		case ea != nil && eb == nil:
			return 1
		case pa[i] != pb[i]:
			return strings.Compare(pa[i], pb[i])
		}
	}
	return len(pa) - len(pb)
}

// PreviousVersion é a maior versão estável de tags menor que current.
func PreviousVersion(tags []string, current string) (string, bool) {
	cur, ok := parseVersion(current)
	if !ok {
		return "", false
	}
	var best *version
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || v.pre != "" || compareVersion(v, cur) >= 0 {
			continue
		}
		if best == nil || compareVersion(v, *best) > 0 {
			best = &v
		}
	}
	if best == nil {
		return "", false
	}
	return best.tag, true
}

// HashVersion é a versão por hash fora da branch padrão:
// <próximo patch da maior estável>-sha.<ts UTC do commit>.g<sha7>.
func HashVersion(tags []string, commitTime time.Time, sha string) string {
	next, _ := NextVersion(tags, "patch", "")
	return fmt.Sprintf("%s-sha.%s.g%s", strings.TrimPrefix(next, "v"), commitTime.UTC().Format("20060102150405"), short(sha))
}

// ---- hooks ---------------------------------------------------------------

// HookRunner roda um hook: `sh -c command` em dir, com env, saída em out.
type HookRunner func(ctx context.Context, dir string, env []string, out io.Writer, command string) error

// ShellHook é o HookRunner de verdade.
func ShellHook(ctx context.Context, dir string, env []string, out io.Writer, command string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// HookError é um hook que saiu com erro.
type HookError struct {
	Hook    string // pre-install | post-install
	Command string
	Err     error
}

func (e *HookError) Error() string {
	return fmt.Sprintf("hook %s (%s) falhou: %v", e.Hook, e.Command, e.Err)
}
func (e *HookError) Unwrap() error { return e.Err }

// ---- install com hooks -----------------------------------------------------

// InstallOptions configura Deploy: todo install (install, release --install,
// rollback, MCP) passa por aqui para os hooks valerem sempre.
type InstallOptions struct {
	App       string
	Image     string // imagem de destino (repo:tag ou repo@sha256:…)
	Tag       string // tag git da release (ex. back@2.9.1), para {tag}; padrão a da imagem
	SHA       string // commit da release, para {sha}; padrão o da release do GitHub da tag
	Action    string // install | rollback (padrão install)
	Config    *Config
	SkipHooks bool

	Reporter     Reporter   // padrão PlainReporter{os.Stderr}
	Hooks        HookRunner // padrão ShellHook
	PollInterval time.Duration
	Wait         time.Duration // padrão 15 min
}

// InstallResult descreve o install.
type InstallResult struct {
	App         string `json:"app"`
	Action      string `json:"action"`
	FromImage   string `json:"fromImage,omitempty"`
	FromVersion string `json:"fromVersion,omitempty"`
	Image       string `json:"image"`
	Version     string `json:"version,omitempty"`
	Installed   bool   `json:"installed"`
	HooksRun    bool   `json:"hooksRun"`
}

func imageParts(img string) (version, digest string) {
	ref, ok := dokku.ParseImage(img)
	if !ok {
		return "", ""
	}
	return ref.Tag, ref.Digest
}

// HookEnv é o ambiente dos hooks: o do processo mais as variáveis do install.
func (c *Client) hookEnv(r InstallResult, fromDigest, toDigest string) []string {
	return append(os.Environ(),
		"ACTION="+r.Action,
		"APP="+r.App,
		"DOKK_URL="+c.URL,
		"FROM_IMAGE="+r.FromImage,
		"TO_IMAGE="+r.Image,
		"FROM_VERSION="+r.FromVersion,
		"TO_VERSION="+r.Version,
		"FROM_DIGEST="+fromDigest,
		"TO_DIGEST="+toDigest,
	)
}

// Deploy instala a imagem na app com os hooks do dokk.yml em volta, na
// máquina do cliente: pre-install com erro aborta sem tocar na app;
// post-install com erro marca o resultado como falho (o install já foi feito).
func (c *Client) Deploy(ctx context.Context, o InstallOptions) (InstallResult, error) {
	if o.Action == "" {
		o.Action = "install"
	}
	if o.Reporter == nil {
		o.Reporter = PlainReporter{os.Stderr}
	}
	if o.Hooks == nil {
		o.Hooks = ShellHook
	}
	if o.Wait <= 0 {
		o.Wait = 15 * time.Minute
	}
	rep := o.Reporter
	res := InstallResult{App: o.App, Action: o.Action, Image: o.Image}
	st, err := c.AppStatus(ctx, o.App)
	if err != nil {
		return res, err
	}
	res.FromImage = st.App.Image()
	fromVersion, fromDigest := imageParts(res.FromImage)
	if fromVersion == "" && fromDigest != "" {
		if l, err := c.RegistryReleases(ctx, o.App); err == nil {
			fromVersion = l.CurrentTag
		}
	}
	res.FromVersion = fromVersion
	toVersion, toDigest := imageParts(o.Image)
	res.Version = toVersion

	hooks := o.Config.App(o.App).Hooks
	root := "."
	if o.Config != nil {
		root = o.Config.Root
	}
	env := c.hookEnv(res, fromDigest, toDigest)
	runHook := func(name, command string) error {
		rep.Step("Hook " + name)
		err := o.Hooks(ctx, root, env, rep.Output(), command)
		if err != nil {
			err = &HookError{Hook: name, Command: command, Err: err}
		}
		rep.Done(command, err)
		return err
	}
	if o.SkipHooks && (hooks.PreInstall != "" || hooks.PostInstall != "") {
		rep.Note("hooks do dokk.yml pulados a pedido (--no-hooks)")
		hooks = Hooks{}
	}

	if hooks.PreInstall != "" {
		res.HooksRun = true
		if err := runHook("pre-install", hooks.PreInstall); err != nil {
			return res, err
		}
	}

	if err := c.applyConfig(ctx, o, res); err != nil {
		return res, err
	}

	title := "Instalando "
	if o.Action == "rollback" {
		title = "Rollback para "
	}
	rep.Step(title + o.Image + " em " + o.App)
	err = c.Install(ctx, o.App, o.Image)
	if err == nil {
		wctx, cancel := context.WithTimeout(ctx, o.Wait)
		err = c.WaitHealthy(wctx, o.App, o.PollInterval, rep.Note)
		cancel()
	}
	if err != nil {
		rep.Done("", err)
		return res, err
	}
	rep.Done("healthy", nil)
	res.Installed = true

	if hooks.PostInstall != "" {
		res.HooksRun = true
		if err := runHook("post-install", hooks.PostInstall); err != nil {
			return res, err
		}
	}
	return res, nil
}

// SetConfig grava uma variável da app sem restart (vale no próximo deploy).
func (c *Client) SetConfig(ctx context.Context, app, key, value string) error {
	_, err := c.Do(ctx, http.MethodPut, appPath(app, "config", key), map[string]any{"value": value, "restart": false})
	return err
}

// ExpandPlaceholders troca {version} {tag} {image} {sha} em s.
func ExpandPlaceholders(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// applyConfig grava o config: do dokk.yml antes do git:from-image.
func (c *Client) applyConfig(ctx context.Context, o InstallOptions, res InstallResult) error {
	cfg := o.Config.App(o.App).Config
	if len(cfg) == 0 {
		return nil
	}
	tag := o.Tag
	if tag == "" {
		tag = res.Version
	}
	version := res.Version
	if v, ok := parseVersion(tag); ok {
		version = fmt.Sprintf("%d.%d.%d", v.core[0], v.core[1], v.core[2])
		if v.pre != "" {
			version += "-" + v.pre
		}
	}
	sha := o.SHA
	if sha == "" && tag != "" {
		if rels, err := c.GithubReleases(ctx, o.App); err == nil {
			for _, r := range rels {
				if r.Tag == tag || ImageTag(r.Tag) == tag {
					sha = r.SHA
					break
				}
			}
		}
	}
	vars := map[string]string{"version": version, "tag": tag, "image": o.Image, "sha": sha}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	o.Reporter.Step("Config da app (" + strings.Join(keys, ", ") + ")")
	for _, k := range keys {
		if err := c.SetConfig(ctx, o.App, k, ExpandPlaceholders(cfg[k], vars)); err != nil {
			err = fmt.Errorf("gravando %s: %w", k, err)
			o.Reporter.Done("", err)
			return err
		}
	}
	o.Reporter.Done("sem restart", nil)
	return nil
}

// InstallTag instala uma tag da imagem da app (com os hooks de o).
func (c *Client) InstallTag(ctx context.Context, tag string, o InstallOptions) (InstallResult, error) {
	if tag == "" {
		return InstallResult{App: o.App}, errors.New("tag obrigatória")
	}
	image, err := c.ImageForTag(ctx, o.App, tag)
	if err != nil {
		return InstallResult{App: o.App}, err
	}
	o.Image = image
	if o.Tag == "" {
		o.Tag = tag
	}
	return c.Deploy(ctx, o)
}

// ErrNoPrevious: não há versão anterior à atual para o rollback.
var ErrNoPrevious = errors.New("não há versão anterior à atual no registry")

// Rollback instala version ou, vazio, a maior versão estável do registry
// menor que a atual. Passa pelos hooks com ACTION=rollback.
func (c *Client) Rollback(ctx context.Context, version string, o InstallOptions) (InstallResult, error) {
	o.Action = "rollback"
	res := InstallResult{App: o.App, Action: "rollback"}
	if version == "" {
		l, err := c.RegistryReleases(ctx, o.App)
		if err != nil {
			return res, err
		}
		cur, _ := imageParts(l.Current)
		if cur == "" {
			cur = l.CurrentTag
		}
		if cur == "" {
			return res, fmt.Errorf("não sei a versão atual de %s (%s): diga a versão do rollback", o.App, l.Current)
		}
		tags := make([]string, len(l.Releases))
		for i, r := range l.Releases {
			tags[i] = r.Tag
		}
		prev, ok := PreviousVersion(tags, cur)
		if !ok {
			return res, fmt.Errorf("%s em %s: %w", o.App, cur, ErrNoPrevious)
		}
		version = prev
	}
	return c.InstallTag(ctx, version, o)
}

// CurrentVersion é a versão instalada para exibição: a tag da imagem, a tag
// que o registry resolveu para o digest ou o digest curto.
func (c *Client) CurrentVersion(ctx context.Context, a App) string {
	if t := a.Tag(); t != "" {
		return t
	}
	_, digest := imageParts(a.Image())
	if digest == "" {
		return ""
	}
	if l, err := c.RegistryReleases(ctx, a.Name); err == nil && l.CurrentTag != "" {
		return l.CurrentTag
	}
	return ShortDigest(digest)
}

// ShortDigest: sha256:abcdef… → @sha256:abcdef123456.
func ShortDigest(d string) string {
	if len(d) > 19 {
		d = d[:19]
	}
	return "@" + d
}

// registryVersions são as tags semver do registry (as da imagem atual da app).
func (c *Client) registryVersions(ctx context.Context, app string) []string {
	l, err := c.RegistryReleases(ctx, app)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(l.Releases))
	for _, r := range l.Releases {
		if _, ok := parseVersion(r.Tag); ok {
			out = append(out, r.Tag)
		}
	}
	sort.Strings(out)
	return out
}
