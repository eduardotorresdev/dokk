package main

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HostStats são os números da máquina mostrados na home.
type HostStats struct {
	CPU       float64   `json:"cpu"`        // % de uso de CPU (todas as CPUs)
	PerCore   []float64 `json:"per-core"`   // % de uso de cada núcleo
	Cores     int       `json:"cores"`      // núcleos da máquina
	CoresUsed float64   `json:"cores-used"` // núcleos consumidos (CPU% × núcleos)
	MemUsed   uint64    `json:"mem-used"`   // bytes
	MemTotal  uint64    `json:"mem-total"`
	DiskUsed  uint64    `json:"disk-used"`
	DiskTotal uint64    `json:"disk-total"`
	At        time.Time `json:"at"`
}

// hostSampler lê /proc e o disco a cada intervalo; a rota só devolve o
// último valor.
type hostSampler struct {
	mu   sync.RWMutex
	last HostStats
}

func (h *hostSampler) get() HostStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.last
}

func (h *hostSampler) run(ctx context.Context, every time.Duration) {
	prev := cpuTimes()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur := cpuTimes()
		s := HostStats{Cores: runtime.NumCPU(), At: time.Now().UTC(), PerCore: []float64{}}
		for i := range cur {
			if i >= len(prev) {
				break
			}
			pct := 0.0
			if dt := cur[i].total - prev[i].total; dt > 0 {
				pct = 100 * float64(dt-(cur[i].idle-prev[i].idle)) / float64(dt)
			}
			if i == 0 {
				s.CPU = pct
			} else {
				s.PerCore = append(s.PerCore, pct)
			}
		}
		prev = cur
		s.CoresUsed = s.CPU / 100 * float64(s.Cores)
		s.MemTotal, s.MemUsed = memInfo()
		var fs syscall.Statfs_t
		if syscall.Statfs("/", &fs) == nil {
			s.DiskTotal = fs.Blocks * uint64(fs.Bsize)
			s.DiskUsed = (fs.Blocks - fs.Bfree) * uint64(fs.Bsize)
		}
		h.mu.Lock()
		h.last = s
		h.mu.Unlock()
	}
}

type cpuTime struct{ idle, total uint64 }

// cpuTimes lê de /proc/stat a linha "cpu" (total) seguida de "cpu0", "cpu1"...
// (idle inclui iowait).
func cpuTimes() []cpuTime {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil
	}
	var out []cpuTime
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var t cpuTime
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			t.total += n
			if i == 3 || i == 4 {
				t.idle += n
			}
		}
		out = append(out, t)
	}
	return out
}

func memInfo() (total, used uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = n * 1024
		case "MemAvailable":
			avail = n * 1024
		}
	}
	return total, total - avail
}
