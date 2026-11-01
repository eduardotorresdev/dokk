package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"dokk/dokku"
)

// Runner executa comandos externos (git, docker). Atrás de uma interface
// para os testes não precisarem de git nem docker.
type Runner interface {
	Run(ctx context.Context, dir string, out io.Writer, name string, args ...string) error
}

// ExecRunner roda os comandos de verdade; stdout e stderr vão para out.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// ReleaseOptions configura Release.
type ReleaseOptions struct {
	App        string
	Bump       string // patch | minor | major (padrão patch quando Version vazio)
	Version    string // versão explícita; tem precedência sobre Bump
	Dir        string // diretório do repositório/contexto do build (padrão ".")
	Notes      string
	Prerelease bool
	Install    bool
	Image      string // repositório da imagem; vazio = o da imagem atual da app
	Platform   string // padrão linux/amd64
	Builder    string // docker | podman | auto | caminho; vazio = DOKK_BUILDER > config > auto

	// Config é o dokk.yml (build e hooks); nil = build de Dir/Dockerfile, sem hooks.
	Config    *Config
	SkipHooks bool
	Hooks     HookRunner // padrão ShellHook

	Log          io.Writer // sem Reporter: saída em texto simples (padrão os.Stderr)
	Reporter     Reporter  // andamento por passos; padrão PlainReporter{Log}
	Runner       Runner    // padrão ExecRunner
	PollInterval time.Duration
	InstallWait  time.Duration // padrão 15 min
}

// ReleaseResult descreve o que foi publicado.
type ReleaseResult struct {
	Tag        string `json:"tag"`
	SHA        string `json:"sha"`
	Branch     string `json:"branch,omitempty"`
	Builder    string `json:"builder,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
	Image      string `json:"image"`
	URL        string `json:"url"`
	Installed  bool   `json:"installed"`
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

type semver struct{ major, minor, patch int }

func parseSemver(s string) (semver, string, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return semver{}, "", false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	return semver{a, b, c}, m[4], true
}

func (v semver) less(o semver) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

func (v semver) String() string { return fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch) }

// NextVersion calcula a próxima tag. explicit ganha de bump; sem releases
// estáveis anteriores, parte de v0.0.0. Pré-releases não contam como base.
func NextVersion(tags []string, bump, explicit string) (string, error) {
	if explicit != "" {
		v, pre, ok := parseSemver(explicit)
		if !ok {
			return "", fmt.Errorf("versão inválida %q (use 1.2.3 ou v1.2.3)", explicit)
		}
		return v.String() + pre, nil
	}
	var cur semver
	for _, t := range tags {
		if v, pre, ok := parseSemver(t); ok && pre == "" && cur.less(v) {
			cur = v
		}
	}
	switch bump {
	case "", "patch":
		cur.patch++
	case "minor":
		cur = semver{cur.major, cur.minor + 1, 0}
	case "major":
		cur = semver{cur.major + 1, 0, 0}
	default:
		return "", fmt.Errorf("bump inválido %q (patch, minor ou major)", bump)
	}
	return cur.String(), nil
}

// FormatTag preenche o formato de tag da app (ex. back@{version}) com a
// versão sem "v".
func FormatTag(format, version string) string {
	if !strings.Contains(format, "{version}") {
		format = "v{version}"
	}
	return strings.Replace(format, "{version}", version, 1)
}

var (
	dockerTagRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	tailSemverRe = regexp.MustCompile(`v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)$`)
)

// ImageTag é a tag docker de uma tag git: a própria quando o docker aceita
// (v1.2.3), senão só a versão (back@1.17.1 → 1.17.1).
func ImageTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if dockerTagRe.MatchString(tag) {
		return tag
	}
	if m := tailSemverRe.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return tag
}

// Reporter recebe o andamento da release em passos. Output recebe a saída
// dos comandos (docker) do passo corrente.
type Reporter interface {
	Step(title string)
	Note(msg string)               // linha de progresso dentro do passo
	Done(detail string, err error) // fim do passo corrente
	Output() io.Writer
}

// PlainReporter escreve "==> passo" e a saída crua em W. É o usado pelo MCP.
type PlainReporter struct{ W io.Writer }

func (p PlainReporter) Step(title string) { fmt.Fprintf(p.W, "==> %s\n", title) }
func (p PlainReporter) Note(msg string)   { fmt.Fprintf(p.W, "    %s\n", msg) }
func (p PlainReporter) Done(detail string, err error) {
	switch {
	case err != nil:
		fmt.Fprintf(p.W, "    erro: %v\n", err)
	case detail != "":
		fmt.Fprintf(p.W, "    %s\n", detail)
	}
}
func (p PlainReporter) Output() io.Writer { return p.W }

// Release roda o fluxo completo: versão → sha no remoto → build+push →
// release no GitHub via dokk → install opcional até healthy.
func (c *Client) Release(ctx context.Context, o ReleaseOptions) (ReleaseResult, error) {
	var res ReleaseResult
	if o.App == "" {
		return res, errors.New("app obrigatória")
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	if o.Platform == "" {
		o.Platform = "linux/amd64"
	}
	if o.Reporter == nil {
		if o.Log == nil {
			o.Log = os.Stderr
		}
		o.Reporter = PlainReporter{o.Log}
	}
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	if o.InstallWait <= 0 {
		o.InstallWait = 15 * time.Minute
	}
	rep := o.Reporter
	step := func(title string, fn func() (string, error)) error {
		rep.Step(title)
		detail, err := fn()
		rep.Done(detail, err)
		return err
	}

	git := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := o.Runner.Run(ctx, o.Dir, &out, "git", args...)
		return strings.TrimSpace(out.String()), err
	}
	err := step("Resolvendo versão", func() (string, error) {
		repo := o.Image
		if repo == "" {
			st, err := c.AppStatus(ctx, o.App)
			if err != nil {
				return "", err
			}
			ref, ok := dokku.ParseImage(st.App.GitSHA)
			if !ok || st.App.DeploySource != "docker-image" {
				return "", fmt.Errorf("app %s: %w", o.App, ErrNotImageDeploy)
			}
			repo = ref.Name()
		} else if ref, ok := dokku.ParseImage(repo); ok {
			repo = ref.Name()
		}
		sha, err := git("rev-parse", "HEAD")
		if err != nil || sha == "" {
			return "", fmt.Errorf("lendo o commit atual: %w", err)
		}
		res.SHA = sha
		branch, def := currentBranch(git), defaultBranch(git)
		res.Branch = branch
		onDefault := branch == def

		link, err := c.GithubLink(ctx, o.App)
		if err != nil {
			return "", err
		}
		rels, err := c.GithubReleases(ctx, o.App)
		if err != nil {
			return "", err
		}
		tags := make([]string, len(rels))
		// A base é o máximo entre as tags git da app e as tags semver do
		// registry (o registry pode estar à frente das releases).
		versions := c.registryVersions(ctx, o.App)
		for i, r := range rels {
			tags[i] = r.Tag
			v := r.Version
			if v == "" {
				if pv, ok := parseVersion(r.Tag); ok {
					v = fmt.Sprintf("%d.%d.%d", pv.core[0], pv.core[1], pv.core[2])
					if pv.pre != "" {
						v += "-" + pv.pre
					}
				}
			}
			versions = append(versions, v)
		}
		var version string
		switch {
		case o.Version != "":
			next, err := NextVersion(versions, "", o.Version)
			if err != nil {
				return "", err
			}
			version = strings.TrimPrefix(next, "v")
		case !onDefault:
			if o.Bump != "" && o.Bump != "patch" {
				return "", fmt.Errorf("--bump %s só vale na branch padrão (%s); em %s a versão é por hash", o.Bump, def, branch)
			}
			ts, err := git("show", "-s", "--format=%ct", "HEAD")
			sec, perr := strconv.ParseInt(ts, 10, 64)
			if err != nil || perr != nil {
				return "", fmt.Errorf("lendo a data do commit: %v", errors.Join(err, perr))
			}
			version = HashVersion(versions, time.Unix(sec, 0), sha)
			o.Prerelease = true
		default:
			next, err := NextVersion(versions, o.Bump, "")
			if err != nil {
				return "", err
			}
			version = strings.TrimPrefix(next, "v")
		}
		res.Prerelease = o.Prerelease
		tag := FormatTag(link.TagFormat, version)
		if slices.Contains(tags, tag) {
			return "", fmt.Errorf("%s: %w", tag, ErrTagExists)
		}
		res.Tag, res.Image = tag, repo+":"+ImageTag(tag)
		prev := "nenhuma"
		if len(tags) > 0 {
			prev = tags[0]
		}
		kind := ""
		if !onDefault && o.Version == "" {
			kind = ", por hash em " + branch
		}
		return fmt.Sprintf("%s (anterior: %s%s)", tag, prev, kind), nil
	})
	if err != nil {
		return res, err
	}

	// Commit precisa existir no remoto (a tag é criada pelo GitHub no sha).
	err = step("Conferindo commit no remoto", func() (string, error) {
		var out bytes.Buffer
		if err := o.Runner.Run(ctx, o.Dir, &out, "git", "branch", "-r", "--contains", res.SHA); err != nil || strings.TrimSpace(out.String()) == "" {
			return "", fmt.Errorf("commit %s: %w", short(res.SHA), ErrNotPushed)
		}
		return short(res.SHA) + " em " + strings.Fields(out.String())[0], nil
	})
	if err != nil {
		return res, err
	}

	builder, err := ResolveBuilder(ctx, o.Runner, o.Builder)
	if err != nil {
		rep.Step("Build e push")
		rep.Done("", err)
		return res, err
	}
	res.Builder = builder.Kind
	err = step("Build e push ("+builder.Kind+")", func() (string, error) {
		var context, dockerfile string
		if o.Config != nil {
			context, dockerfile = o.Config.BuildPaths(o.App, o.Dir)
		}
		if err := builder.BuildPush(ctx, o.Runner, o.Dir, rep.Output(), o.Platform, res.Image, dockerfile, context); err != nil {
			return "", err
		}
		return o.Platform, nil
	})
	if err != nil {
		return res, err
	}

	err = step("Criando release no GitHub", func() (string, error) {
		created, err := c.CreateRelease(ctx, o.App, NewRelease{
			Tag: res.Tag, SHA: res.SHA, Image: res.Image, Notes: o.Notes, Prerelease: o.Prerelease,
		})
		if err != nil {
			return "", fmt.Errorf("criando a release: %w", err)
		}
		res.URL = created.URL
		return created.URL, nil
	})
	if err != nil || !o.Install {
		return res, err
	}

	ir, err := c.Deploy(ctx, InstallOptions{
		App: o.App, Image: res.Image, Tag: res.Tag, SHA: res.SHA, Config: o.Config, SkipHooks: o.SkipHooks,
		Reporter: rep, Hooks: o.Hooks, PollInterval: o.PollInterval, Wait: o.InstallWait,
	})
	res.Installed = ir.Installed
	return res, err
}

// currentBranch é a branch do checkout; destacado (CI), a do GitHub Actions.
func currentBranch(git func(...string) (string, error)) string {
	b, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && b != "" && b != "HEAD" {
		return b
	}
	for _, k := range []string{"GITHUB_HEAD_REF", "GITHUB_REF_NAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return b
}

// defaultBranch é a branch padrão do remoto (origin/HEAD); sem ela, main.
func defaultBranch(git func(...string) (string, error)) string {
	if b, err := git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && b != "" {
		return strings.TrimPrefix(b, "origin/")
	}
	return "main"
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
