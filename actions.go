package main

import (
	"context"
	"sync"
	"time"

	"dokk/dokku"
)

// Action é a última ação disparada numa app (restart, stop, config…).
type Action struct {
	Name       string    `json:"name"`
	Running    bool      `json:"running"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started-at"`
	FinishedAt time.Time `json:"finished-at,omitzero"`
}

type actionLog struct {
	mu   sync.Mutex
	last map[string]*Action
}

func newActions() *actionLog { return &actionLog{last: map[string]*Action{}} }

// start registra a ação; falha se já houver outra rodando na mesma app.
func (l *actionLog) start(app, name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.last[app]; a != nil && a.Running {
		return false
	}
	l.last[app] = &Action{Name: name, Running: true, StartedAt: time.Now()}
	return true
}

func (l *actionLog) finish(app string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.last[app]
	if a == nil {
		return
	}
	a.Running = false
	a.FinishedAt = time.Now()
	if err != nil {
		a.Error = err.Error()
	}
}

func (l *actionLog) get(app string) *Action {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.last[app]; a != nil {
		c := *a
		return &c
	}
	return nil
}

// releaseCache guarda as tags do registry por repositório: consultar o
// registry leva centenas de ms, então a rota responde do cache e atualiza
// em segundo plano quando ele envelhece.
type releaseCache struct {
	client *dokku.Client
	mu     sync.Mutex
	items  map[string]*releaseEntry
}

type releaseEntry struct {
	list       []dokku.Release
	err        error
	at         time.Time
	refreshing bool
	ready      chan struct{}
}

const releasesMaxAge = 2 * time.Minute

func newReleaseCache(c *dokku.Client) *releaseCache {
	return &releaseCache{client: c, items: map[string]*releaseEntry{}}
}

func (rc *releaseCache) get(ctx context.Context, ref dokku.ImageRef) ([]dokku.Release, error) {
	key := ref.Name()
	rc.mu.Lock()
	e := rc.items[key]
	if e == nil {
		e = &releaseEntry{ready: make(chan struct{})}
		rc.items[key] = e
		rc.refresh(e, ref)
	} else if time.Since(e.at) > releasesMaxAge {
		rc.refresh(e, ref)
	}
	rc.mu.Unlock()
	select {
	case <-e.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return e.list, e.err
}

// refresh busca em segundo plano; chamado com rc.mu travado.
func (rc *releaseCache) refresh(e *releaseEntry, ref dokku.ImageRef) {
	if e.refreshing {
		return
	}
	e.refreshing = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		list, err := rc.client.Releases(ctx, ref)
		rc.mu.Lock()
		defer rc.mu.Unlock()
		e.refreshing = false
		e.at = time.Now()
		// Falha com lista antiga em mãos: mantém a antiga.
		if err == nil || e.list == nil {
			e.list, e.err = list, err
		}
		select {
		case <-e.ready:
		default:
			close(e.ready)
		}
	}()
}

// warm busca de antemão as tags das imagens de todas as apps.
func (rc *releaseCache) warm(ctx context.Context, apps []dokku.AppWithHistory) {
	for _, a := range apps {
		if ref, ok := dokku.ParseImage(a.GitSHA); ok && a.DeploySource == "docker-image" {
			go rc.get(ctx, ref)
		}
	}
}
