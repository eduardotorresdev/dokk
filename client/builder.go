package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dokk/dokku"
)

// ---- config do usuário (~/.config/dokk/config.json) ----------------------

// UserConfig é a configuração por máquina, ao lado do credentials.json.
// O builder é daqui (e de flag/env), nunca do dokk.yml do repo: cada máquina
// builda com o que tem (podman no Mac, docker buildx no CI).
type UserConfig struct {
	Builder string `json:"builder,omitempty"`
}

// UserConfigKeys são as chaves aceitas por `dokk config`.
var UserConfigKeys = []string{"builder"}

// UserConfigPath é ~/.config/dokk/config.json (ou $XDG_CONFIG_HOME/dokk).
func UserConfigPath() (string, error) {
	p, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "config.json"), nil
}

// LoadUserConfig lê a config; inexistente vira vazia.
func LoadUserConfig() (UserConfig, error) {
	var c UserConfig
	p, err := UserConfigPath()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

// SaveUserConfig grava atômico, 0600 (diretório 0700).
func SaveUserConfig(c UserConfig) error {
	p, err := UserConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Get/Set/Unset por nome de chave.
func (c *UserConfig) Get(key string) (string, error) {
	switch key {
	case "builder":
		return c.Builder, nil
	}
	return "", fmt.Errorf("chave desconhecida %q (use: %s)", key, strings.Join(UserConfigKeys, ", "))
}

func (c *UserConfig) Set(key, value string) error {
	switch key {
	case "builder":
		if _, err := builderKind(value); err != nil {
			return err
		}
		c.Builder = value
		return nil
	}
	_, err := c.Get(key)
	return err
}

// ---- builders --------------------------------------------------------------

// Builder é como esta máquina builda e publica a imagem.
type Builder struct {
	Kind   string `json:"kind"`   // docker | podman
	Bin    string `json:"bin"`    // binário (docker, podman, /opt/podman/bin/podman)
	Value  string `json:"value"`  // o valor configurado (auto, podman, caminho…)
	Source string `json:"source"` // flag | env | config | auto
}

// PodmanPaths são os lugares procurados além do PATH (o instalador oficial
// do podman no macOS põe o binário em /opt/podman/bin).
var PodmanPaths = []string{"podman", "/opt/podman/bin/podman"}

// ErrNoBuilder: nem docker com buildx nem podman nesta máquina.
var ErrNoBuilder = errors.New("nenhum builder encontrado: instale o docker com buildx ou o podman, ou configure com `dokk config set builder <docker|podman|caminho>`")

// builderKind deduz o tipo pelo valor: auto, docker, podman ou um caminho de
// binário cujo nome diz qual é.
func builderKind(v string) (string, error) {
	switch v {
	case "", "auto":
		return "auto", nil
	case "docker", "podman":
		return v, nil
	}
	base := strings.ToLower(filepath.Base(v))
	switch {
	case strings.Contains(base, "podman"):
		return "podman", nil
	case strings.Contains(base, "docker"):
		return "docker", nil
	}
	return "", fmt.Errorf("builder inválido %q (use auto, docker, podman ou o caminho de um deles)", v)
}

// BuilderSetting é o valor efetivo e de onde veio: flag > DOKK_BUILDER >
// config do usuário > auto.
func BuilderSetting(flagValue string) (value, source string, err error) {
	switch {
	case flagValue != "":
		return flagValue, "flag", nil
	case os.Getenv("DOKK_BUILDER") != "":
		return os.Getenv("DOKK_BUILDER"), "env", nil
	}
	uc, err := LoadUserConfig()
	if err != nil {
		return "", "", err
	}
	if uc.Builder != "" {
		return uc.Builder, "config", nil
	}
	return "auto", "auto", nil
}

// ResolveBuilder aplica a precedência e, em auto, detecta: docker se
// `docker buildx version` funcionar, senão o primeiro podman que responder.
func ResolveBuilder(ctx context.Context, run Runner, flagValue string) (Builder, error) {
	value, source, err := BuilderSetting(flagValue)
	if err != nil {
		return Builder{}, err
	}
	kind, err := builderKind(value)
	if err != nil {
		return Builder{}, fmt.Errorf("%s (de %s)", err, source)
	}
	b := Builder{Kind: kind, Bin: value, Value: value, Source: source}
	if kind != "auto" {
		return b, nil
	}
	if run.Run(ctx, ".", io.Discard, "docker", "buildx", "version") == nil {
		b.Kind, b.Bin = "docker", "docker"
		return b, nil
	}
	for _, p := range PodmanPaths {
		if run.Run(ctx, ".", io.Discard, p, "--version") == nil {
			b.Kind, b.Bin = "podman", p
			return b, nil
		}
	}
	return b, ErrNoBuilder
}

// ErrRegistryAuth: o push foi recusado por falta de login no registry.
var ErrRegistryAuth = errors.New("o registry recusou o push (login)")

var authFailure = regexp.MustCompile(`(?i)unauthorized|authentication required|denied|401|403|no basic auth credentials|not logged in`)

// BuildPush builda a imagem e publica: docker com `buildx build --push`;
// podman com `build` e depois `push` (o build do podman não publica).
// context vazio = ".".
func (b Builder) BuildPush(ctx context.Context, run Runner, dir string, out io.Writer, platform, image, dockerfile, context string) error {
	if context == "" {
		context = "."
	}
	file := func(args []string) []string {
		if dockerfile != "" {
			args = append(args, "-f", dockerfile)
		}
		return append(args, context)
	}
	// O que o comando escreve vai para out e para um rabo guardado, para
	// reconhecer erro de login.
	var tail bytes.Buffer
	w := io.MultiWriter(out, &tail)
	wrap := func(what string, err error) error {
		if err == nil {
			return nil
		}
		if authFailure.MatchString(tail.String()) {
			reg := "<registry>"
			if ref, ok := dokku.ParseImage(image); ok {
				reg = ref.Registry
			}
			return fmt.Errorf("%s: %w; rode `%s login %s` nesta máquina e tente de novo", what, ErrRegistryAuth, b.Bin, reg)
		}
		return fmt.Errorf("%s (%s): %w", what, b.Kind, err)
	}
	switch b.Kind {
	case "docker":
		args := file([]string{"buildx", "build", "--platform", platform, "-t", image, "--push"})
		return wrap("build e push da imagem", run.Run(ctx, dir, w, b.Bin, args...))
	case "podman":
		args := file([]string{"build", "--platform", platform, "-t", image})
		if err := run.Run(ctx, dir, w, b.Bin, args...); err != nil {
			return wrap("build da imagem", err)
		}
		tail.Reset()
		return wrap("push da imagem", run.Run(ctx, dir, w, b.Bin, "push", image))
	}
	return fmt.Errorf("builder %q sem suporte", b.Kind)
}
