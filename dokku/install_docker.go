package dokku

// Instalação via Docker (https://dokku.com/docs/getting-started/install/docker/)
// para as distros que o bootstrap.sh não cobre: garante o Docker pelo
// gerenciador de pacotes da distro (ou get.docker.com) e sobe a imagem
// oficial dokku/dokku como o container "dokku".

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Image é a imagem oficial; a tag é a versão sem o "v".
const Image = "dokku/dokku"

// dockerPkgScript devolve o trecho de shell que instala o Docker na distro
// (ID e ID_LIKE do os-release). Distros que o get.docker.com conhece usam
// o script oficial; as outras, o pacote da própria distro.
func dockerPkgScript(id, like string) string {
	fam := " " + id + " " + like + " "
	has := func(names ...string) bool {
		for _, n := range names {
			if strings.Contains(fam, " "+n+" ") {
				return true
			}
		}
		return false
	}
	switch {
	case id == "ubuntu" || id == "debian" || id == "raspbian" || id == "centos" || id == "fedora" || id == "rhel":
		return getDockerScript
	case id == "amzn":
		return "if command -v dnf >/dev/null 2>&1; then dnf -y install docker; else yum -y install docker; fi"
	case has("rhel", "centos", "fedora"):
		// Rocky, Alma, Oracle e afins: repositório do docker-ce para CentOS.
		// O config-manager do dnf5 mudou de sintaxe; tenta as duas.
		return "dnf -y install dnf-plugins-core\n" +
			"dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo || dnf config-manager addrepo --from-repofile=https://download.docker.com/linux/centos/docker-ce.repo\n" +
			"dnf -y install docker-ce docker-ce-cli containerd.io"
	case has("arch", "archarm", "manjaro"):
		return "pacman -Sy --noconfirm --needed docker"
	case has("suse", "opensuse", "sles") || strings.HasPrefix(id, "opensuse"):
		return "zypper --non-interactive install docker"
	case id == "alpine":
		return "apk add --no-cache docker"
	case has("debian", "ubuntu"):
		return "apt-get update\nDEBIAN_FRONTEND=noninteractive apt-get install -y docker.io"
	}
	return getDockerScript
}

const getDockerScript = `if command -v curl >/dev/null 2>&1; then curl -fsSL https://get.docker.com -o /tmp/get-docker.sh; else wget -qO /tmp/get-docker.sh https://get.docker.com; fi
sh /tmp/get-docker.sh
rm -f /tmp/get-docker.sh`

// dockerScript instala o Docker se faltar, liga o serviço (systemd ou
// OpenRC) e espera o daemon responder.
func dockerScript(id, like string) string {
	return `set -e
if command -v docker >/dev/null 2>&1; then
  echo "Docker found: $(docker --version)"
else
` + indent(dockerPkgScript(id, like)) + `
fi
if docker info >/dev/null 2>&1; then exit 0; fi
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl enable --now docker
elif command -v rc-update >/dev/null 2>&1; then
  rc-update add docker default
  rc-service docker start
else
  service docker start
fi
i=0
until docker info >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then echo "Docker did not start" >&2; exit 1; fi
  sleep 1
done
echo "Docker is running: $(docker --version)"
`
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

// EnsureDocker instala e liga o Docker (h vem do Host), mandando o log para line.
func EnsureDocker(ctx context.Context, h HostInfo, line func(string)) error {
	return runLines(ctx, nil, line, "sh", "-c", dockerScript(h.OS, h.OSLike))
}

// dokkuRunArgs são os argumentos do `docker container run` da documentação,
// com o nginx do container direto nas portas 80/443 do host (não há outro
// nginx) e o SSH do git push na 3022.
func dokkuRunArgs(tag, hostname string) []string {
	return []string{
		"container", "run", "-d",
		"--name", DokkuContainer,
		"--restart", "unless-stopped",
		"--env", "DOKKU_HOSTNAME=" + hostname,
		"--env", "DOKKU_HOST_ROOT=" + DockerDataRoot + "/home/dokku",
		"--env", "DOKKU_LIB_HOST_ROOT=" + DockerDataRoot + "/var/lib/dokku",
		"--publish", "3022:22",
		"--publish", "80:80",
		"--publish", "443:443",
		"--volume", DockerDataRoot + ":/mnt/dokku",
		"--volume", "/var/run/docker.sock:/var/run/docker.sock",
		Image + ":" + strings.TrimPrefix(tag, "v"),
	}
}

// RunDokkuContainer baixa a imagem da versão tag, cria (ou religa) o
// container e espera o healthcheck da imagem ficar healthy.
func RunDokkuContainer(ctx context.Context, r Runner, tag string, line func(string)) error {
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("versão inválida: %q", tag)
	}
	image := Image + ":" + strings.TrimPrefix(tag, "v")
	if err := runLines(ctx, nil, line, "docker", "pull", image); err != nil {
		return fmt.Errorf("docker pull %s: %w", image, err)
	}
	// Container parado de uma instalação anterior: só religa, mantendo o
	// que ele já tinha.
	if _, err := r.Run(ctx, "docker", "inspect", "--format", "{{.Id}}", DokkuContainer); err == nil {
		line("docker start " + DokkuContainer)
		if _, err := r.Run(ctx, "docker", "start", DokkuContainer); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(DockerDataRoot, 0o755); err != nil {
			return err
		}
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "dokku.me"
		}
		args := dokkuRunArgs(tag, hostname)
		line("docker " + strings.Join(args, " "))
		if _, err := r.Run(ctx, "docker", args...); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	return waitHealthy(ctx, r, line, 3*time.Second)
}

// waitHealthy acompanha o HEALTHCHECK da imagem (starting → healthy), que só
// passa depois do primeiro boot completo do Dokku.
func waitHealthy(ctx context.Context, r Runner, line func(string), every time.Duration) error {
	last := ""
	for {
		out, err := r.Run(ctx, "docker", "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", DokkuContainer)
		if err != nil {
			return err
		}
		state, health, _ := strings.Cut(strings.TrimSpace(out), " ")
		if s := strings.TrimSpace(out); s != last {
			line("container " + DokkuContainer + ": " + s)
			last = s
		}
		switch {
		case state != "running" && state != "created" && state != "restarting":
			logs, _ := r.Run(ctx, "docker", "logs", "--tail", "50", DokkuContainer)
			for _, l := range strings.Split(strings.TrimSpace(logs), "\n") {
				line(StripANSI(l))
			}
			return fmt.Errorf("o container %s parou (%s)", DokkuContainer, state)
		case health == "healthy", state == "running" && health == "":
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}
