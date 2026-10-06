package dokku

import (
	"context"
	"testing"
	"time"
)

// monitorWith monta um Monitor já "coletado" com as apps dadas, sem dokku.
func monitorWith(names ...string) *Monitor {
	m := NewMonitor(nil, 0)
	for _, n := range names {
		m.apps = append(m.apps, App{Name: n})
		m.history[n] = []string{StatusHealthy}
	}
	close(m.refreshed)
	return m
}

func TestMonitorSetActionApareceNaListaEAvisa(t *testing.T) {
	m := monitorWith("api", "web")
	changed := m.Changed()
	m.SetAction("api", "destroy")
	select {
	case <-changed:
	default:
		t.Fatal("SetAction deveria acordar quem espera em Changed")
	}
	apps, _ := m.Apps(context.Background())
	if apps[0].Action != "destroy" || apps[1].Action != "" {
		t.Fatalf("actions = %q, %q", apps[0].Action, apps[1].Action)
	}
	m.SetAction("api", "")
	apps, _ = m.Apps(context.Background())
	if apps[0].Action != "" {
		t.Fatalf("action deveria ter sido limpa: %q", apps[0].Action)
	}
}

func TestMonitorRemoveTiraAppNaHora(t *testing.T) {
	m := monitorWith("api", "web")
	m.SetAction("api", "destroy")
	changed := m.Changed()
	m.Remove("api")
	select {
	case <-changed:
	default:
		t.Fatal("Remove deveria acordar quem espera em Changed")
	}
	apps, _ := m.Apps(context.Background())
	if len(apps) != 1 || apps[0].Name != "web" {
		t.Fatalf("apps = %+v", apps)
	}
	if _, ok, _ := m.App(context.Background(), "api"); ok {
		t.Fatal("app removida ainda encontrada")
	}
	if _, ok := m.history["api"]; ok {
		t.Fatal("histórico da app removida deveria sumir")
	}
	if _, ok := m.busy["api"]; ok {
		t.Fatal("ação da app removida deveria sumir")
	}
}

// Coleta que começou antes do destroy (ou leu o cache velho de relatórios)
// não pode trazer a app removida de volta.
func TestMonitorRemoveEscondeAppDeColetaAtrasada(t *testing.T) {
	m := monitorWith("api", "web")
	m.Remove("api")
	m.mu.Lock()
	m.apps = m.dropRemovedLocked([]App{{Name: "api"}, {Name: "web"}})
	m.mu.Unlock()
	if _, ok, _ := m.App(context.Background(), "api"); ok {
		t.Fatal("coleta atrasada trouxe a app removida de volta")
	}
	// O dokku já não lista a app: a marca cai.
	m.mu.Lock()
	m.dropRemovedLocked([]App{{Name: "web"}})
	_, marked := m.removed["api"]
	m.mu.Unlock()
	if marked {
		t.Fatal("marca de remoção deveria cair quando o dokku não lista mais a app")
	}
}

func TestMonitorAddedMostraAppRecriada(t *testing.T) {
	m := monitorWith("api")
	m.Remove("api")
	m.Added("api")
	m.mu.Lock()
	apps := m.dropRemovedLocked([]App{{Name: "api"}})
	m.mu.Unlock()
	if len(apps) != 1 {
		t.Fatalf("app recriada com o mesmo nome deveria aparecer: %+v", apps)
	}
}

func TestMonitorRemoveMarcaExpira(t *testing.T) {
	m := monitorWith("api")
	m.Remove("api")
	m.removed["api"] = time.Now().Add(-RemovedTTL)
	m.mu.Lock()
	apps := m.dropRemovedLocked([]App{{Name: "api"}})
	m.mu.Unlock()
	if len(apps) != 1 {
		t.Fatalf("depois do prazo a app listada pelo dokku deveria aparecer: %+v", apps)
	}
}
