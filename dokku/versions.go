package dokku

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

// Version é um deploy registrado no repositório git da app. Em deploys por
// imagem (git:from-image) o dokku faz um commit trocando o FROM do
// Dockerfile, então Image diz qual imagem foi para o ar.
type Version struct {
	SHA     string    `json:"sha"`
	Date    time.Time `json:"date"`
	Author  string    `json:"author"`
	Message string    `json:"message"`
	Image   string    `json:"image,omitempty"`
	Current bool      `json:"current"`
}

const maxVersions = 30

// Versions lê os últimos commits do repositório da app (em HomeRoot).
func (c *Client) Versions(ctx context.Context, app, current string) ([]Version, error) {
	// O repositório é do usuário dokku; safe.directory evita a recusa do git
	// por "dubious ownership" rodando como root.
	git := []string{"-c", "safe.directory=*", "-C", filepath.Join(c.HomeRoot, app)}
	out, err := c.Runner.Run(ctx, "git", append(git, "log", "-n", "30", "--format=%x00%H%x1f%cI%x1f%an%x1f%s")...)
	if err != nil {
		// Sem repositório (app nunca recebeu deploy): sem versões.
		return []Version{}, nil
	}
	// Só o diff do Dockerfile, para achar a imagem de cada deploy por imagem
	// sem ler o diff inteiro de apps com deploy por git push.
	images, _ := c.Runner.Run(ctx, "git", append(git, "log", "-n", "30", "--format=%x00%H%x1f%cI%x1f%an%x1f%s", "-p", "--no-color", "-U0", "--", "Dockerfile")...)
	return parseVersions(out, images, current), nil
}

func parseVersions(out, images, current string) []Version {
	image := map[string]string{}
	for _, v := range parseLog(images) {
		image[v.SHA] = v.Image
	}
	versions := parseLog(out)
	if len(versions) > maxVersions {
		versions = versions[:maxVersions]
	}
	found := false
	for i := range versions {
		versions[i].Image = image[versions[i].SHA]
		versions[i].Current = current != "" && strings.HasPrefix(versions[i].SHA, current)
		found = found || versions[i].Current
	}
	// O git sha do relatório nem sempre bate com o repositório (deploy por
	// imagem); aí o commit mais recente é o que está no ar.
	if len(versions) > 0 && !found {
		versions[0].Current = true
	}
	return versions
}

func parseLog(out string) []Version {
	versions := []Version{}
	for _, chunk := range strings.Split(out, "\x00") {
		header, diff, _ := strings.Cut(chunk, "\n")
		f := strings.Split(header, "\x1f")
		if len(f) != 4 {
			continue
		}
		v := Version{SHA: f[0], Author: f[2], Message: f[3]}
		v.Date, _ = time.Parse(time.RFC3339, f[1])
		for _, line := range strings.Split(diff, "\n") {
			if img, ok := strings.CutPrefix(line, "+FROM "); ok {
				v.Image = strings.TrimSpace(img)
				break
			}
		}
		versions = append(versions, v)
	}
	return versions
}
