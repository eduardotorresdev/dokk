package dokku

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Status de saúde da app, derivado dos processos em execução.
const (
	StatusHealthy   = "healthy"
	StatusDegraded  = "degraded"
	StatusUnhealthy = "unhealthy"
	StatusUnknown   = "unknown"
	// Estados de transição e de pausa intencional.
	StatusStarting   = "starting"
	StatusRestarting = "restarting"
	StatusRemoving   = "removing"
	StatusSuspended  = "suspended"
)

// App reúne as informações exibidas na home. As tags JSON seguem o formato
// esperado pela UI (ui/src/mock/apps.js).
type App struct {
	Name         string              `json:"name"`
	Deployed     bool                `json:"deployed"`
	Locked       bool                `json:"locked"`
	DeploySource string              `json:"deploy-source"`
	GitSHA       string              `json:"git-sha"`
	LastDeployAt *time.Time          `json:"last-deploy-at"`
	ProxyType    string              `json:"proxy-type"`
	Domains      []string            `json:"domains"`
	SSL          bool                `json:"ssl"`
	Processes    map[string]*Process `json:"processes"`
	Checks       Checks              `json:"checks"`
}

type Process struct {
	Running int `json:"running"`
	Total   int `json:"total"`
}

type Checks struct {
	Status      string     `json:"status"`
	LastCheckAt *time.Time `json:"last-check-at"`
}

type Client struct {
	Runner Runner
	// LibRoot é o DOKKU_LIB_ROOT, de onde vêm a escala, o restore e o lock de
	// deploy de cada app.
	LibRoot string
	// HomeRoot é o DOKKU_ROOT, onde ficam os repositórios git das apps.
	HomeRoot string

	mu      sync.Mutex
	reports *reportSet    // cache dos relatórios da home
	details *DetailSource // cache da parte lenta dos detalhes (relatórios + variáveis)
}

func NewClient(r Runner) *Client {
	return &Client{Runner: r, LibRoot: "/var/lib/dokku", HomeRoot: "/home/dokku"}
}

// Relatórios do dokku que, rodados sem app, trazem todas as apps de uma vez.
// O ps:report fica de fora porque inspeciona container por container (~1s por
// app); o estado dos processos vem direto do docker.
var bulkReports = []string{"apps", "git", "domains", "proxy", "certs"}

// containersFormat gera "<app> <tipo> <estado>" a partir dos labels do dokku.
const containersFormat = `{{.Label "com.dokku.app-name"}} {{.Label "com.dokku.process-type"}} {{.State}} {{.Status}}`

// ReportsMaxAge é por quanto tempo os relatórios do dokku (lentos e caros:
// cada comando passa por sudo + vários scripts bash) são reaproveitados entre
// coletas. O estado dos containers vem do docker a cada coleta.
const ReportsMaxAge = time.Minute

type reportSet struct {
	names   []string
	reports []map[string]map[string]string
	at      time.Time
}

// Invalidate força a próxima coleta a reler os relatórios do dokku (depois
// de uma ação que muda a app).
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = nil
	c.details = nil
}

func (c *Client) bulk(ctx context.Context) (*reportSet, error) {
	c.mu.Lock()
	cached := c.reports
	c.mu.Unlock()
	if cached != nil && time.Since(cached.at) < ReportsMaxAge {
		return cached, nil
	}
	set := &reportSet{reports: make([]map[string]map[string]string, len(bulkReports)), at: time.Now()}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		out, err := c.Runner.Run(gctx, "dokku", "apps:list")
		set.names = parseList(out)
		return err
	})
	for i, p := range bulkReports {
		g.Go(func() error {
			out, err := c.Runner.Run(gctx, "dokku", p+":report")
			set.reports[i] = parseReports(out)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.reports = set
	c.mu.Unlock()
	return set, nil
}

// ListApps lista todas as apps com as informações da home.
func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	var (
		set        *reportSet
		containers map[string]*appContainers
	)
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		set, err = c.bulk(ctx)
		return err
	})
	g.Go(func() error {
		out, err := c.Runner.Run(ctx, "docker", "ps", "--all",
			"--filter", "label=com.dokku.app-name",
			"--filter", "label=com.dokku.container-type=deploy",
			"--format", containersFormat)
		containers = parseContainers(out)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	names, reports := set.names, set.reports
	apps := make([]App, len(names))
	for i, name := range names {
		state := c.readState(name)
		apps[i] = buildApp(name, reports[0][name], reports[1][name], reports[2][name], reports[3][name], reports[4][name], containers[name], state, now)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

// appState é o que o dokku guarda em disco sobre a app.
type appState struct {
	scale   map[string]int // escala configurada (ps:scale); vazio se não houver
	stopped bool           // ps:stop marca restore=false
	locked  bool           // há deploy/restart/start em andamento
}

func (c *Client) readState(app string) appState {
	st := appState{scale: map[string]int{}}
	psDir := filepath.Join(c.LibRoot, "config", "ps", app)
	if b, err := os.ReadFile(filepath.Join(psDir, "scale")); err == nil {
		for _, kv := range strings.FieldsFunc(string(b), func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
			typ, n, ok := strings.Cut(kv, "=")
			if v, err := strconv.Atoi(n); ok && err == nil {
				st.scale[typ] = v
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(psDir, "restore")); err == nil {
		st.stopped = strings.TrimSpace(string(b)) == "false"
	}
	_, err := os.Stat(filepath.Join(c.LibRoot, "data", "apps", app, ".deploy.lock"))
	st.locked = err == nil
	return st
}

func buildApp(name string, appsR, gitR, domainsR, proxyR, certsR map[string]string, ctr *appContainers, state appState, now time.Time) App {
	if ctr == nil {
		ctr = &appContainers{}
	}
	app := App{
		Name:         name,
		Deployed:     len(ctr.processes) > 0 || len(state.scale) > 0 && state.stopped,
		Locked:       appsR["app locked"] == "true",
		DeploySource: appsR["app deploy source"],
		GitSHA:       gitR["git sha"],
		ProxyType:    proxyR["proxy computed type"],
		Domains:      strings.Fields(domainsR["domains app vhosts"]),
		SSL:          certsR["ssl enabled"] == "true",
		Processes:    processes(ctr.processes, state.scale),
		LastDeployAt: parseUnix(gitR["git last updated at"]),
	}
	if app.DeploySource == "docker-image" || app.GitSHA == "" {
		if meta := appsR["app deploy source metadata"]; meta != "" {
			app.GitSHA = meta
		}
	}
	if app.Domains == nil {
		app.Domains = []string{}
	}
	app.Checks = Checks{Status: status(app, ctr, state)}
	if app.Deployed {
		app.Checks.LastCheckAt = &now
	}
	return app
}

// processes cruza os containers rodando com a escala configurada. O total
// vem da escala, não da contagem de containers: durante um restart o dokku
// mantém o container antigo (parado) ao lado do novo.
func processes(running map[string]int, scale map[string]int) map[string]*Process {
	procs := map[string]*Process{}
	for typ, n := range scale {
		if n > 0 {
			procs[typ] = &Process{Total: n}
		}
	}
	for typ, n := range running {
		p := procs[typ]
		if p == nil {
			p = &Process{}
			procs[typ] = p
		}
		p.Running = n
		if p.Total < n {
			p.Total = n
		}
	}
	return procs
}

func status(app App, ctr *appContainers, state appState) string {
	running := 0
	for _, n := range ctr.processes {
		running += n
	}
	switch {
	case ctr.removing:
		return StatusRemoving
	case ctr.restarting || state.locked && running > 0:
		return StatusRestarting
	case ctr.starting || state.locked:
		return StatusStarting
	case state.stopped && running == 0:
		return StatusSuspended
	}
	return healthStatus(app)
}

func healthStatus(app App) string {
	if !app.Deployed {
		return StatusUnknown
	}
	running, total := 0, 0
	for _, p := range app.Processes {
		running += p.Running
		total += p.Total
	}
	switch {
	case total == 0 || running == 0:
		return StatusUnhealthy
	case running < total:
		return StatusDegraded
	default:
		return StatusHealthy
	}
}

func parseUnix(s string) *time.Time {
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil || sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}
