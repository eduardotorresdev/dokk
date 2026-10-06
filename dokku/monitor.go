package dokku

import (
	"context"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HistorySize é quantas verificações ficam guardadas por app.
const HistorySize = 24

// AppWithHistory é a App acrescida do histórico de checks, como a UI espera.
type AppWithHistory struct {
	App
	Checks ChecksWithHistory `json:"checks"`
	Usage  *Usage            `json:"usage,omitempty"`
	// Action é a ação em andamento na app (ex.: "destroy"), para a home
	// mostrar o estado sem esperar a próxima coleta.
	Action string `json:"action,omitempty"`
}

// Usage é quanto da máquina os containers da app consomem, em % (CPU sobre
// todos os núcleos; memória sobre a RAM total, que é o limite padrão do
// docker stats quando a app não define um).
type Usage struct {
	CPU float64 `json:"cpu"`
	Mem float64 `json:"mem"`
}

type ChecksWithHistory struct {
	Checks
	History []string `json:"history"`
}

// Monitor consulta o dokku periodicamente, guarda o último estado de cada app
// e o histórico dos últimos HistorySize status (em memória).
type Monitor struct {
	Client   *Client
	Interval time.Duration

	mu        sync.RWMutex
	apps      []App
	history   map[string][]string
	lastErr   error
	refreshed chan struct{}
	// changed é fechado e substituído a cada coleta, acordando quem espera.
	changed chan struct{}
	poke    chan struct{}
	// busy guarda a ação rodando em cada app (ver SetAction).
	busy map[string]string
	// removed guarda as apps tiradas por Remove: uma coleta que começou antes
	// do destroy (ou que leu o cache velho de relatórios) ainda as traria de
	// volta. Ficam escondidas até o dokku confirmar ou o prazo acabar.
	removed map[string]time.Time

	// Cache da página interna, atualizado em segundo plano depois de cada
	// coleta (ver DetailSource).
	details      *DetailSource
	detailsReady chan struct{}
	collecting   atomic.Bool
	force        atomic.Bool // próxima coleta de detalhes ignora o intervalo
	detailsAt    time.Time
}

// DetailsEvery é o intervalo mínimo entre coletas dos detalhes (docker stats
// leva ~2s e não precisa rodar a cada coleta da home).
const DetailsEvery = 15 * time.Second

func NewMonitor(c *Client, interval time.Duration) *Monitor {
	return &Monitor{Client: c, Interval: interval, history: map[string][]string{}, busy: map[string]string{}, removed: map[string]time.Time{}, refreshed: make(chan struct{}), changed: make(chan struct{}), poke: make(chan struct{}, 1), detailsReady: make(chan struct{})}
}

// Run faz a primeira coleta e repete a cada Interval até o contexto acabar.
func (m *Monitor) Run(ctx context.Context) {
	m.refresh(ctx)
	close(m.refreshed)
	go m.refreshDetails(ctx)
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.refresh(ctx)
		case <-m.poke:
			m.refresh(ctx)
			t.Reset(m.Interval)
		}
		go m.refreshDetails(ctx)
	}
}

func (m *Monitor) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	apps, err := m.Client.ListApps(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.notifyLocked()
	m.lastErr = err
	if err != nil {
		log.Printf("dokku: falha ao listar apps: %v", err)
		return
	}
	apps = m.dropRemovedLocked(apps)
	seen := map[string]bool{}
	for _, a := range apps {
		seen[a.Name] = true
		h := append(m.history[a.Name], a.Checks.Status)
		if len(h) > HistorySize {
			h = h[len(h)-HistorySize:]
		}
		m.history[a.Name] = h
	}
	for name := range m.history {
		if !seen[name] {
			delete(m.history, name)
		}
	}
	m.apps = apps
}

// RemovedTTL é por quanto tempo uma app removida fica escondida mesmo que a
// coleta ainda a liste (o cache de relatórios do client dura ReportsMaxAge).
const RemovedTTL = 2 * ReportsMaxAge

// dropRemovedLocked tira da coleta as apps removidas há pouco. A marca cai
// quando o dokku já não lista a app ou quando o prazo acaba. Exige m.mu.
func (m *Monitor) dropRemovedLocked(apps []App) []App {
	if len(m.removed) == 0 {
		return apps
	}
	listed := map[string]bool{}
	out := make([]App, 0, len(apps))
	for _, a := range apps {
		listed[a.Name] = true
		if at, ok := m.removed[a.Name]; ok && time.Since(at) < RemovedTTL {
			continue
		}
		out = append(out, a)
	}
	for name, at := range m.removed {
		if !listed[name] || time.Since(at) >= RemovedTTL {
			delete(m.removed, name)
		}
	}
	return out
}

// notifyLocked acorda quem espera em Changed. Exige m.mu travado.
func (m *Monitor) notifyLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// SetAction marca (ou, com label vazio, desmarca) a ação em andamento na
// app e avisa os assinantes na hora.
func (m *Monitor) SetAction(app, label string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if label == "" {
		delete(m.busy, app)
	} else {
		m.busy[app] = label
	}
	m.notifyLocked()
}

// Remove tira a app do snapshot imediatamente (depois de um destroy bem
// sucedido), sem esperar a próxima coleta, e avisa os assinantes.
func (m *Monitor) Remove(app string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	apps := make([]App, 0, len(m.apps))
	for _, a := range m.apps {
		if a.Name != app {
			apps = append(apps, a)
		}
	}
	if m.apps != nil {
		m.apps = apps
	}
	delete(m.history, app)
	delete(m.busy, app)
	m.removed[app] = time.Now()
	m.notifyLocked()
}

// Added desfaz a marca de Remove: uma app com o mesmo nome foi criada e
// deve aparecer na próxima coleta.
func (m *Monitor) Added(app string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.removed, app)
}

// Poke pede uma coleta fora do intervalo (depois de uma ação na app).
func (m *Monitor) Poke() {
	m.Client.Invalidate()
	m.force.Store(true)
	select {
	case m.poke <- struct{}{}:
	default:
	}
}

// Apps devolve o último snapshot. Bloqueia até a primeira coleta terminar.
func (m *Monitor) Apps(ctx context.Context) ([]AppWithHistory, error) {
	select {
	case <-m.refreshed:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.apps == nil && m.lastErr != nil {
		return nil, m.lastErr
	}
	out := make([]AppWithHistory, len(m.apps))
	for i, a := range m.apps {
		out[i] = AppWithHistory{App: a, Checks: ChecksWithHistory{
			Checks:  a.Checks,
			History: append([]string(nil), m.history[a.Name]...),
		}}
		out[i].Action = m.busy[a.Name]
		if m.details != nil {
			out[i].Usage = appUsage(m.details.stats, a.Name)
		}
	}
	return out, nil
}

// Changed devolve um canal que fecha na próxima coleta.
func (m *Monitor) Changed() <-chan struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.changed
}

// App devolve o último snapshot de uma app e se ela existe.
func (m *Monitor) App(ctx context.Context, name string) (AppWithHistory, bool, error) {
	apps, err := m.Apps(ctx)
	if err != nil {
		return AppWithHistory{}, false, err
	}
	for _, a := range apps {
		if a.Name == name {
			return a, true, nil
		}
	}
	return AppWithHistory{}, false, nil
}

// refreshDetails recoleta o cache da página interna; se uma coleta ainda
// estiver rodando, esta é pulada.
func (m *Monitor) refreshDetails(ctx context.Context) {
	if !m.collecting.CompareAndSwap(false, true) {
		return
	}
	defer m.collecting.Store(false)
	m.mu.RLock()
	recent := time.Since(m.detailsAt) < DetailsEvery
	m.mu.RUnlock()
	if recent && !m.force.Swap(false) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	src, err := m.Client.CollectDetails(ctx, m.names())
	if err != nil {
		log.Printf("dokku: falha ao coletar detalhes: %v", err)
		return
	}
	m.mu.Lock()
	first := m.details == nil
	m.details = src
	m.detailsAt = time.Now()
	m.mu.Unlock()
	if first {
		close(m.detailsReady)
	}
}

func (m *Monitor) names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, len(m.apps))
	for i, a := range m.apps {
		names[i] = a.Name
	}
	return names
}

// Detail monta os detalhes de uma app a partir do cache. Só a primeira
// chamada depois de subir o servidor espera a coleta inicial.
func (m *Monitor) Detail(ctx context.Context, app string) (Detail, error) {
	select {
	case <-m.detailsReady:
	case <-ctx.Done():
		return Detail{}, ctx.Err()
	}
	m.mu.RLock()
	src := m.details
	m.mu.RUnlock()
	return m.Client.BuildDetail(src, app, m.names()), nil
}

// RefreshConfig relê as variáveis de uma app para o cache (depois de um
// config:set/unset), sem esperar a próxima coleta completa.
func (m *Monitor) RefreshConfig(ctx context.Context, app string) {
	vars, err := m.Client.Config(ctx, app)
	if err != nil {
		return
	}
	// O cache de variáveis do client está velho: a próxima coleta relê tudo.
	m.Client.Invalidate()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.details != nil {
		m.details = m.details.withConfig(app, vars)
	}
}

// Config devolve as variáveis de uma app (com valores) do cache.
func (m *Monitor) Config(ctx context.Context, app string) (map[string]string, error) {
	select {
	case <-m.detailsReady:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	vars := make(map[string]string, len(m.details.configs[app]))
	for k, v := range m.details.configs[app] {
		vars[k] = v
	}
	return vars, nil
}

// appUsage soma o docker stats dos containers <app>.* (inclusive os que
// ainda estão saindo, que também pesam na máquina).
func appUsage(stats map[string]containerStats, app string) *Usage {
	var u Usage
	found := false
	for name, s := range stats {
		if !strings.HasPrefix(name, app+".") {
			continue
		}
		found = true
		u.CPU += parsePerc(s.CPUPerc)
		u.Mem += parsePerc(s.MemPerc)
	}
	if !found {
		return nil
	}
	u.CPU /= float64(runtime.NumCPU())
	return &u
}

func parsePerc(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return v
}
