package dokku

import (
	"runtime"
	"testing"
)

func TestAppUsage(t *testing.T) {
	stats := map[string]containerStats{
		"api.web.1":    {CPUPerc: "50%", MemPerc: "2.5%"},
		"api.worker.1": {CPUPerc: "30%", MemPerc: "1.5%"},
		"apix.web.1":   {CPUPerc: "99%", MemPerc: "9%"},
	}
	u := appUsage(stats, "api")
	if u == nil || u.Mem != 4 || u.CPU != 80/float64(runtime.NumCPU()) {
		t.Fatalf("usage = %+v", u)
	}
	if appUsage(stats, "nada") != nil {
		t.Fatal("app sem containers deveria ser nil")
	}
}
