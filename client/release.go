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

	Log          io.Writer // sem Reporter: saída em texto simples (padrão os.Stderr)
	Reporter     Reporter  // andamento por passos; padrão PlainReporter{Log}
	Runner       Runner    // padrão ExecRunner
	PollInterval time.Duration
	InstallWait  time.Duration // padrão 15 min
}

// ReleaseResult descreve o que foi publicado.
type ReleaseResult struct {
	Tag       string `json:"tag"`
	SHA       string `json:"sha"`
	Image     string `json:"image"`
	URL       string `json:"url"`
	Installed bool   `json:"installed"`
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
		rels, err := c.GithubReleases(ctx, o.App)
		if err != nil {
			return "", err
		}
		tags := make([]string, len(rels))
		for i, r := range rels {
			tags[i] = r.Tag
		}
		tag, err := NextVersion(tags, o.Bump, o.Version)
		if err != nil {
			return "", err
		}
		if slices.Contains(tags, tag) {
			return "", fmt.Errorf("%s: %w", tag, ErrTagExists)
		}
		res.Tag, res.Image = tag, repo+":"+tag
		prev := "nenhuma"
		if len(tags) > 0 {
			prev = tags[0]
		}
		return fmt.Sprintf("%s (anterior: %s)", tag, prev), nil
	})
	if err != nil {
		return res, err
	}

	// Commit precisa existir no remoto (a tag é criada pelo GitHub no sha).
	err = step("Conferindo commit no remoto", func() (string, error) {
		var out bytes.Buffer
		if err := o.Runner.Run(ctx, o.Dir, &out, "git", "rev-parse", "HEAD"); err != nil {
			return "", fmt.Errorf("lendo o commit atual: %w", err)
		}
		res.SHA = strings.TrimSpace(out.String())
		out.Reset()
		if err := o.Runner.Run(ctx, o.Dir, &out, "git", "branch", "-r", "--contains", res.SHA); err != nil || strings.TrimSpace(out.String()) == "" {
			return "", fmt.Errorf("commit %s: %w", short(res.SHA), ErrNotPushed)
		}
		return short(res.SHA) + " em " + strings.Fields(out.String())[0], nil
	})
	if err != nil {
		return res, err
	}

	err = step("Build e push de "+res.Image, func() (string, error) {
		if err := o.Runner.Run(ctx, o.Dir, rep.Output(), "docker", "buildx", "build",
			"--platform", o.Platform, "-t", res.Image, "--push", "."); err != nil {
			return "", fmt.Errorf("build da imagem: %w", err)
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

	err = step("Instalando em "+o.App, func() (string, error) {
		if err := c.Install(ctx, o.App, res.Image); err != nil {
			return "", fmt.Errorf("install: %w", err)
		}
		wctx, cancel := context.WithTimeout(ctx, o.InstallWait)
		defer cancel()
		if err := c.WaitHealthy(wctx, o.App, o.PollInterval, rep.Note); err != nil {
			return "", err
		}
		res.Installed = true
		return "healthy", nil
	})
	return res, err
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
