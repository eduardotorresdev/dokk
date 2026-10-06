package dokku

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// Detail reúne o que a página interna de uma app mostra além do resumo.
type Detail struct {
	Containers []Container `json:"containers"`
	Ports      []string    `json:"ports"`
	Storage    []Mount     `json:"storage"`
	SSL        SSL         `json:"ssl"`
	ConfigKeys []string    `json:"config-keys"`
	Builds     []Build     `json:"builds"`
	// Services são as outras apps que esta usa (via variável que aponta
	// para o host delas na rede do dokku); UsedBy é o caminho inverso.
	Services []Link   `json:"services"`
	UsedBy   []Link   `json:"used-by"`
	Networks []string `json:"networks"`
}

// Link liga duas apps pelas variáveis em que uma referencia a outra. Só os
// nomes das variáveis saem do servidor, nunca os valores.
type Link struct {
	App  string   `json:"app"`
	Vars []string `json:"vars"`
}

type Container struct {
	Name     string `json:"name"`
	Process  string `json:"process"`
	State    string `json:"state"`
	Status   string `json:"status"`
	Image    string `json:"image"`
	Created  string `json:"created"`
	CPU      string `json:"cpu"`
	Memory   string `json:"memory"`
	MemPerc  string `json:"mem-perc"`
	Retiring bool   `json:"retiring"` // container antigo que o dokku ainda não removeu
}

type Mount struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	Readonly  bool   `json:"readonly"`
}

type SSL struct {
	Enabled   bool     `json:"enabled"`
	ExpiresAt string   `json:"expires-at"`
	Issuer    string   `json:"issuer"`
	Hostnames []string `json:"hostnames"`
}

// Build é um registro de deploy/rebuild/restart que o dokku grava em
// DOKKU_LIB_ROOT/data/builds/<app>/<id>.json.
type Build struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Source     string     `json:"source"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	ExitCode   *int       `json:"exit_code"`
}

const maxBuilds = 20

// DetailSource guarda as saídas brutas, de todas as apps de uma vez, de que
// a página interna precisa. Coletar leva segundos (o docker stats sozinho
// passa de 1s), então o Monitor mantém uma cópia atualizada em segundo plano
// e a rota só monta o Detail em memória.
type DetailSource struct {
	reports map[string]map[string]map[string]string // plugin → app → chave → valor
	ps      string
	stats   map[string]containerStats
	configs map[string]map[string]string // os valores nunca saem do servidor
	known   []string
	at      time.Time // quando relatórios e variáveis foram lidos
}

var detailReports = []string{"ports", "storage", "certs", "network"}

// CollectDetails roda os relatórios em massa (sem app) e o config:export de
// cada app conhecida.
//
// Os relatórios e as variáveis (vários comandos dokku, caros) são
// reaproveitados por ReportsMaxAge; containers e stats vêm do docker sempre.
func (c *Client) CollectDetails(ctx context.Context, known []string) (*DetailSource, error) {
	c.mu.Lock()
	slow := c.details
	c.mu.Unlock()
	fresh := slow != nil && time.Since(slow.at) < ReportsMaxAge && sameNames(slow.known, known)

	src := &DetailSource{reports: map[string]map[string]map[string]string{}, configs: map[string]map[string]string{}, known: known, at: time.Now()}
	if fresh {
		src.reports, src.configs, src.at = slow.reports, slow.configs, slow.at
	}
	outs := make([]string, len(detailReports))
	configs := make([]map[string]string, len(known))

	g, gctx := errgroup.WithContext(ctx)
	for i, p := range detailReports {
		if fresh {
			break
		}
		g.Go(func() error {
			out, err := c.Runner.Run(gctx, "dokku", p+":report")
			outs[i] = out
			return err
		})
	}
	cfg := new(errgroup.Group)
	cfg.SetLimit(4)
	for i, name := range known {
		if fresh {
			break
		}
		cfg.Go(func() error {
			configs[i], _ = c.Config(gctx, name)
			return nil
		})
	}
	g.Go(cfg.Wait)
	g.Go(func() error {
		out, err := c.Runner.Run(gctx, "docker", "ps", "--all",
			"--filter", "label=com.dokku.app-name",
			"--filter", "label=com.dokku.container-type=deploy",
			"--format", "{{json .}}")
		src.ps = out
		return err
	})
	g.Go(func() error {
		// Só os containers rodando têm stats; erro aqui não derruba a página.
		out, _ := c.Runner.Run(gctx, "docker", "stats", "--no-stream", "--format", "{{json .}}")
		src.stats = parseStats(out)
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if !fresh {
		for i, p := range detailReports {
			src.reports[p] = parseReports(outs[i])
		}
		for i, name := range known {
			if configs[i] != nil {
				src.configs[name] = configs[i]
			}
		}
		c.mu.Lock()
		c.details = src
		c.mu.Unlock()
	}
	return src, nil
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// withConfig devolve uma cópia rasa da fonte com as variáveis de uma app
// trocadas (depois de um config:set, sem esperar a próxima coleta).
func (s *DetailSource) withConfig(app string, vars map[string]string) *DetailSource {
	cp := *s
	cp.configs = make(map[string]map[string]string, len(s.configs))
	for k, v := range s.configs {
		cp.configs[k] = v
	}
	cp.configs[app] = vars
	return &cp
}

// GetDetail coleta e monta os detalhes de uma app na hora (o Monitor usa a
// versão em cache). O nome deve ter sido validado contra a lista de apps.
func (c *Client) GetDetail(ctx context.Context, app string, known []string) (Detail, error) {
	src, err := c.CollectDetails(ctx, known)
	if err != nil {
		return Detail{}, err
	}
	return c.BuildDetail(src, app, known), nil
}

// BuildDetail monta o Detail de uma app a partir da fonte coletada; known é
// a lista de todas as apps, usada para achar as ligações entre elas.
func (c *Client) BuildDetail(src *DetailSource, app string, known []string) Detail {
	d := Detail{Containers: []Container{}, Ports: []string{}, Storage: []Mount{}, ConfigKeys: []string{}, Services: []Link{}, UsedBy: []Link{}, Networks: []string{}}
	report := func(p string) map[string]string { return src.reports[p][app] }

	d.Ports = strings.Fields(report("ports")["ports map"])
	if len(d.Ports) == 0 {
		d.Ports = strings.Fields(report("ports")["ports map detected"])
	}
	d.Storage = parseMounts(report("storage"))
	certs := report("certs")
	d.SSL = SSL{
		Enabled:   certs["ssl enabled"] == "true",
		ExpiresAt: certs["ssl expires at"],
		Issuer:    certs["ssl issuer"],
		Hostnames: strings.Fields(certs["ssl hostnames"]),
	}
	d.Networks = strings.Fields(report("network")["network computed attach post create"])
	d.Containers = parseDetailContainers(src.ps, src.stats, app)
	for _, name := range known {
		if name == app {
			for k := range src.configs[name] {
				d.ConfigKeys = append(d.ConfigKeys, k)
			}
			sort.Strings(d.ConfigKeys)
			d.Services = links(src.configs[name], known, app)
		} else if vars := referencing(src.configs[name], app); len(vars) > 0 {
			d.UsedBy = append(d.UsedBy, Link{App: name, Vars: vars})
		}
	}
	d.Builds = c.readBuilds(app)
	return d
}

// links devolve as apps referenciadas nas variáveis de uma app.
func links(config map[string]string, known []string, self string) []Link {
	out := []Link{}
	for _, other := range known {
		if other == self {
			continue
		}
		if vars := referencing(config, other); len(vars) > 0 {
			out = append(out, Link{App: other, Vars: vars})
		}
	}
	return out
}

// referencing devolve as variáveis cujo valor aponta para o host de uma app
// ("portile-db", "portile-db.web", "mysql://portile-db:3306"...).
func referencing(config map[string]string, app string) []string {
	host := regexp.MustCompile(`(^|[/@:=\s,])` + regexp.QuoteMeta(app) + `(\.web|\.worker|[.:/]|$)`)
	vars := []string{}
	for k, v := range config {
		if host.MatchString(v) {
			vars = append(vars, k)
		}
	}
	sort.Strings(vars)
	return vars
}

func parseMounts(report map[string]string) []Mount {
	mounts := []Mount{}
	for i := 1; ; i++ {
		prefix := "storage attachment " + strconv.Itoa(i) + " "
		host, ok := report[prefix+"host path"]
		if !ok {
			return mounts
		}
		mounts = append(mounts, Mount{
			Host:      host,
			Container: report[prefix+"container path"],
			Readonly:  report[prefix+"readonly"] == "true",
		})
	}
}

type containerStats struct{ Name, CPUPerc, MemUsage, MemPerc string }

// parseStats lê `docker stats --format {{json .}}`, indexado pelo nome.
func parseStats(out string) map[string]containerStats {
	stats := map[string]containerStats{}
	for _, line := range strings.Split(out, "\n") {
		var s containerStats
		if json.Unmarshal([]byte(line), &s) == nil {
			stats[s.Name] = s
		}
	}
	return stats
}

// Containers do dokku se chamam <app>.<tipo>.<n>; os antigos ganham um
// sufixo com timestamp (<app>.<tipo>.<n>.<ts>) até serem removidos.
var retiringName = regexp.MustCompile(`\.\d+\.\d{6,}$`)

func parseDetailContainers(out string, stats map[string]containerStats, app string) []Container {
	containers := []Container{}
	for _, line := range strings.Split(out, "\n") {
		var p struct{ Names, State, Status, Image, CreatedAt, Labels string }
		if json.Unmarshal([]byte(line), &p) != nil || !strings.HasPrefix(p.Names, app+".") {
			continue
		}
		c := Container{
			Name:     p.Names,
			State:    p.State,
			Status:   p.Status,
			Image:    p.Image,
			Created:  p.CreatedAt,
			Retiring: retiringName.MatchString(p.Names),
		}
		for _, label := range strings.Split(p.Labels, ",") {
			if v, ok := strings.CutPrefix(label, "com.dokku.process-type="); ok {
				c.Process = v
			}
		}
		if s, ok := stats[p.Names]; ok {
			c.CPU, c.Memory, c.MemPerc = s.CPUPerc, s.MemUsage, s.MemPerc
		}
		containers = append(containers, c)
	}
	sort.Slice(containers, func(i, j int) bool {
		if containers[i].Retiring != containers[j].Retiring {
			return !containers[i].Retiring
		}
		return containers[i].Name < containers[j].Name
	})
	return containers
}

var buildID = regexp.MustCompile(`^[a-z0-9]+$`)

func (c *Client) buildsDir(app string) string {
	return filepath.Join(c.LibRoot, "data", "builds", app)
}

func (c *Client) readBuilds(app string) []Build {
	builds := []Build{}
	files, _ := filepath.Glob(filepath.Join(c.buildsDir(app), "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var build Build
		if json.Unmarshal(b, &build) == nil && build.ID != "" {
			builds = append(builds, build)
		}
	}
	sort.Slice(builds, func(i, j int) bool {
		a, b := builds[i].StartedAt, builds[j].StartedAt
		return a != nil && (b == nil || a.After(*b))
	})
	if len(builds) > maxBuilds {
		builds = builds[:maxBuilds]
	}
	return builds
}

// BuildLog devolve o log de um build, sem as sequências de cor ANSI.
func (c *Client) BuildLog(app, id string) (string, error) {
	if !buildID.MatchString(id) {
		return "", os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(c.buildsDir(app), id+".log"))
	if err != nil {
		return "", err
	}
	return StripANSI(string(b)), nil
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// StreamLogs roda `dokku logs <app> -t` e entrega cada linha até o contexto
// acabar (o processo é morto junto).
func StreamLogs(ctx context.Context, app string, tail int, line func(string)) error {
	cmd := exec.CommandContext(ctx, "dokku", "logs", app, "-t", "-n", strconv.Itoa(tail))
	// O dokku logs passa por sudo, bash e docker logs. Rodando num grupo de
	// processos próprio dá pra matar todos ao desconectar; matar só o
	// primeiro deixaria os filhos vivos para sempre.
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

func readLines(r io.Reader, line func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line(StripANSI(sc.Text()))
	}
}
