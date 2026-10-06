package dokku

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// InstallTag é a versão instalada quando o GitHub não responde.
const InstallTag = "v0.38.31"

var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// LatestTag pergunta ao GitHub a última versão estável do Dokku. Qualquer
// falha ou tag fora do padrão devolve InstallTag.
func LatestTag(ctx context.Context) string {
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := getJSON(ctx, "https://api.github.com/repos/dokku/dokku/releases/latest", "", &body); err != nil || !tagRe.MatchString(body.TagName) {
		return InstallTag
	}
	return body.TagName
}

// Version roda `dokku version` ("dokku version 0.38.31") e devolve "0.38.31".
// Erro = Dokku ausente ou quebrado.
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.Runner.Run(ctx, "dokku", "version")
	if err != nil {
		return "", err
	}
	v := parseVersion(out)
	if v == "" {
		return "", ErrNoVersion
	}
	return v, nil
}

func parseVersion(out string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	f := strings.Fields(first)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimPrefix(f[len(f)-1], "v")
}

// HostInfo diz se dá para instalar o Dokku nesta máquina e como.
type HostInfo struct {
	OS        string `json:"os"`
	OSVersion string `json:"os-version"`
	// OSLike é o ID_LIKE do os-release (família da distro).
	OSLike     string `json:"-"`
	Arch       string `json:"arch"`
	Root       bool   `json:"root"`
	CanInstall bool   `json:"can-install"`
	// Method é "bootstrap" (bootstrap.sh oficial, Ubuntu/Debian suportados)
	// ou "docker" (imagem dokku/dokku, qualquer outra distro).
	Method string `json:"method,omitempty"`
	Reason string `json:"reason,omitempty"`
	// ReasonCode ("os", "arch", "root") e ReasonArg deixam a UI e a API
	// traduzirem o motivo; Reason fica em pt-BR.
	ReasonCode string `json:"reason-code,omitempty"`
	ReasonArg  string `json:"reason-arg,omitempty"`
}

const (
	MethodBootstrap = "bootstrap"
	MethodDocker    = "docker"
)

// Host lê o sistema; force (flag -dokku-mode) fixa o método de instalação.
func Host(force Mode) HostInfo {
	osRelease := ""
	if runtime.GOOS == "linux" {
		b, _ := os.ReadFile("/etc/os-release")
		osRelease = string(b)
	}
	return hostInfo(runtime.GOOS, runtime.GOARCH, osRelease, os.Geteuid(), force)
}

// Versões em que o bootstrap.sh roda. O resto instala pela imagem Docker.
var bootstrapOS = map[string][]string{
	"ubuntu": {"22.04", "24.04", "26.04"},
	"debian": {"11", "12", "13"},
}

func hostInfo(goos, arch, osRelease string, euid int, force Mode) HostInfo {
	h := HostInfo{OS: goos, Arch: arch, Root: euid == 0}
	for _, l := range strings.Split(osRelease, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			h.OS = v
		case "VERSION_ID":
			h.OSVersion = v
		case "ID_LIKE":
			h.OSLike = v
		}
	}
	switch {
	case force == ModeHost, force == ModeAuto && contains(bootstrapOS[h.OS], h.OSVersion):
		h.Method = MethodBootstrap
	default:
		h.Method = MethodDocker
	}
	switch {
	case goos != "linux":
		h.Method = ""
		h.Reason, h.ReasonCode, h.ReasonArg = fmt.Sprintf("O Dokku só roda em Linux (%s).", goos), "os", goos
	case arch != "amd64" && arch != "arm64":
		h.Reason, h.ReasonCode, h.ReasonArg = fmt.Sprintf("Arquitetura não suportada (%s).", arch), "arch", arch
	case euid != 0:
		h.Reason, h.ReasonCode = "O dokk precisa rodar como root para instalar o Dokku.", "root"
	default:
		h.CanInstall = true
	}
	return h
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// O bootstrap.sh é maior e o GitHub/CDN às vezes demora: o httpClient de 15s
// é curto demais.
var downloadClient = &http.Client{Timeout: 60 * time.Second}

// Bootstrap baixa o bootstrap.sh oficial da versão tag para dir e roda com
// bash, mandando cada linha (stdout+stderr, sem ANSI) para line. Demora
// minutos (apt, docker, nginx).
func Bootstrap(ctx context.Context, tag, dir string, line func(string)) error {
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("versão inválida: %q", tag)
	}
	path := filepath.Join(dir, "bootstrap.sh")
	if err := download(ctx, "https://dokku.com/install/"+tag+"/bootstrap.sh", path); err != nil {
		return &DownloadError{Err: err}
	}
	return runLines(ctx, []string{"DOKKU_TAG=" + tag, "DEBIAN_FRONTEND=noninteractive", "DOKKU_SKIP_KEY_FILE=true"}, line, "bash", path)
}

// runLines roda o comando com env extra e manda cada linha (stdout+stderr,
// sem ANSI) para line.
func runLines(ctx context.Context, env []string, line func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	// Mesmo esquema do StreamLogs: grupo próprio para matar apt, docker e
	// cia. junto se o contexto acabar.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	readLines(out, line)
	return cmd.Wait()
}

func download(ctx context.Context, u, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %s", u, res.Status)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// GlobalDomains lê HomeRoot/VHOST (domínios globais, um por linha).
func (c *Client) GlobalDomains() []string {
	out := []string{}
	f, err := os.Open(filepath.Join(c.home(), "VHOST"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if d := strings.TrimSpace(sc.Text()); d != "" {
			out = append(out, d)
		}
	}
	return out
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

func ValidDomain(d string) bool { return len(d) <= 253 && domainRe.MatchString(d) }

// SetGlobalDomain roda domains:set-global.
func (c *Client) SetGlobalDomain(ctx context.Context, domain string) error {
	if !ValidDomain(domain) {
		return fmt.Errorf("domínio inválido: %q", domain)
	}
	_, err := c.Runner.Run(ctx, "dokku", "domains:set-global", domain)
	return err
}

var sshKeyRe = regexp.MustCompile(`^(ssh-(rsa|ed25519|dss)|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com) [A-Za-z0-9+/=]+( .*)?$`)

func ValidSSHKey(k string) bool {
	k = strings.TrimSpace(k)
	return !strings.ContainsAny(k, "\r\n") && sshKeyRe.MatchString(k)
}

// AddSSHKey cadastra uma chave pública (stdin de `ssh-keys:add <name>`).
func (c *Client) AddSSHKey(ctx context.Context, name, key string) error {
	if !ValidSSHKey(key) {
		return errors.New("chave SSH inválida")
	}
	_, err := c.runInput(ctx, strings.TrimSpace(key)+"\n", "dokku", "ssh-keys:add", name)
	return err
}

// EnsureSSHKey cadastra a chave com esse nome de forma idempotente: se o
// nome já existe, troca a chave (ssh-keys:remove + add); se a mesma chave já
// está cadastrada com outro nome, não faz nada. replacing e existing avisam
// cada caso (quem chama escreve a linha de log no idioma do usuário).
func (c *Client) EnsureSSHKey(ctx context.Context, name, key string, replacing, existing func()) error {
	if !ValidSSHKey(key) {
		return errors.New("chave SSH inválida")
	}
	// Sem nenhuma chave o ssh-keys:list sai com erro; trata como lista vazia.
	out, _ := c.Runner.Run(ctx, "dokku", "ssh-keys:list")
	if strings.Contains(out, `NAME="`+name+`"`) {
		replacing()
		if _, err := c.Runner.Run(ctx, "dokku", "ssh-keys:remove", name); err != nil {
			return err
		}
	}
	err := c.AddSSHKey(ctx, name, key)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		existing()
		return nil
	}
	return err
}

// LetsencryptEmail devolve o e-mail global do plugin letsencrypt ("" se não
// houver), lido da propriedade em LibRoot/config/letsencrypt/--global/email.
func (c *Client) LetsencryptEmail() string {
	b, err := os.ReadFile(filepath.Join(c.lib(), "config", "letsencrypt", "--global", "email"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// LetsencryptInstalled diz se o plugin está habilitado. No modo docker os
// plugins ficam na camada do container, fora do volume: pergunta ao Dokku.
func (c *Client) LetsencryptInstalled() bool {
	if c.Mode(context.Background()) == ModeDocker {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := c.Runner.Run(ctx, "dokku", "plugin:installed", "letsencrypt")
		return err == nil
	}
	_, err := os.Stat(filepath.Join(c.lib(), "plugins", "enabled", "letsencrypt"))
	return err == nil
}

const letsencryptRepo = "https://github.com/dokku/dokku-letsencrypt.git"

// InstallLetsencrypt instala o plugin oficial; devolve o stdout para o log.
// No modo docker também entra no plugin-list do volume, que o container
// reinstala ao ser recriado (numa atualização da imagem, por exemplo).
func (c *Client) InstallLetsencrypt(ctx context.Context) (string, error) {
	out, err := c.Runner.Run(ctx, "dokku", "plugin:install", letsencryptRepo, "letsencrypt")
	if err != nil || c.Mode(ctx) != ModeDocker {
		return out, err
	}
	return out, addPluginList(filepath.Join(DockerDataRoot, "plugin-list"), "letsencrypt", letsencryptRepo)
}

// addPluginList acrescenta "nome: repo" ao plugin-list se o nome não estiver lá.
func addPluginList(path, name, repo string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, _, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == name {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	prefix := ""
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		prefix = "\n"
	}
	if _, err := fmt.Fprintf(f, "%s%s: %s\n", prefix, name, repo); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SetupLetsencrypt grava o e-mail global e liga o cron de renovação.
func (c *Client) SetupLetsencrypt(ctx context.Context, email string) error {
	if _, err := c.Runner.Run(ctx, "dokku", "letsencrypt:set", "--global", "email", email); err != nil {
		return err
	}
	_, err := c.Runner.Run(ctx, "dokku", "letsencrypt:cron-job", "--add")
	return err
}
