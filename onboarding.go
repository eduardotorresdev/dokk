package main

// Onboarding: detecta o Dokku, instala pelo bootstrap oficial (ou pela
// imagem Docker nas distros que ele não cobre) e faz a
// configuração inicial. As tarefas longas (install, configure) rodam uma
// por vez em segundo plano; a UI acompanha o log por polling.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dokk/dokku"
)

type onboarding struct {
	dir     string     // dataDir: onboarding.json, bootstrap.sh, onboarding.log
	tag     string     // -dokku-tag
	mode    dokku.Mode // -dokku-mode: fixa o método de instalação
	client  *dokku.Client
	monitor *dokku.Monitor
	state   *onboardingState
	jobs    *jobRunner
}

func newOnboarding(dir, tag string, c *dokku.Client, m *dokku.Monitor) *onboarding {
	return &onboarding{
		dir: dir, tag: tag, client: c, monitor: m,
		state: loadOnboardingState(filepath.Join(dir, "onboarding.json")),
		jobs:  &jobRunner{logTo: filepath.Join(dir, "onboarding.log")},
	}
}

// onboardingState marca que o usuário terminou (ou pulou) o assistente,
// para a home não redirecionar de novo, e que o Dokku já foi detectado ou
// instalado uma vez, para o assistente não repetir esse passo.
type onboardingState struct {
	path      string
	mu        sync.Mutex
	Done      bool      `json:"done"`
	DoneAt    time.Time `json:"done-at,omitzero"`
	DokkuSeen bool      `json:"dokku-seen"`
}

func loadOnboardingState(path string) *onboardingState {
	s := &onboardingState{path: path}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, s)
	}
	return s
}

func (s *onboardingState) done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Done
}

func (s *onboardingState) dokkuSeen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DokkuSeen
}

func (s *onboardingState) markDone() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Done, s.DoneAt = true, time.Now()
	return s.save()
}

func (s *onboardingState) markDokkuSeen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DokkuSeen = true
	return s.save()
}

// save grava o estado de forma atômica; quem chama segura s.mu.
func (s *onboardingState) save() error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Job é a tarefa do onboarding em andamento ou a última.
type Job struct {
	ID         int       `json:"id"`
	Kind       string    `json:"kind"` // "install" | "configure"
	Step       string    `json:"step"`
	Running    bool      `json:"running"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started-at"`
	FinishedAt time.Time `json:"finished-at,omitzero"`
}

const maxJobLines = 5000

type jobRunner struct {
	mu     sync.Mutex
	job    *Job
	lines  []string // log do job atual (guarda as últimas maxJobLines)
	first  int      // índice absoluto de lines[0]
	nextID int
	logTo  string // arquivo onde o log também é gravado
}

// start dispara fn se não houver outra tarefa rodando. Zera o log, gera um
// novo ID. fn recebe log (uma linha) e step (troca o rótulo do passo).
func (j *jobRunner) start(kind string, fn func(ctx context.Context, log, step func(string)) error) (*Job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job != nil && j.job.Running {
		return nil, false
	}
	j.nextID++
	j.job = &Job{ID: j.nextID, Kind: kind, Running: true, StartedAt: time.Now()}
	j.lines, j.first = nil, 0
	id := j.nextID

	// O arquivo é só um registro extra (sobrevive a um restart do dokk).
	var file *os.File
	if j.logTo != "" {
		file, _ = os.OpenFile(j.logTo, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	logLine := func(s string) {
		j.mu.Lock()
		defer j.mu.Unlock()
		if file != nil {
			fmt.Fprintln(file, s)
		}
		if j.job == nil || j.job.ID != id {
			return
		}
		j.lines = append(j.lines, s)
		if over := len(j.lines) - maxJobLines; over > 0 {
			j.lines = append([]string(nil), j.lines[over:]...)
			j.first += over
		}
	}
	step := func(s string) {
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.job != nil && j.job.ID == id {
			j.job.Step = s
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer cancel()
		logLine(fmt.Sprintf("-----> %s %s", kind, time.Now().Format(time.RFC3339)))
		err := fn(ctx, logLine, step)
		if err != nil {
			logLine("!     " + err.Error())
			log.Printf("onboarding %s: %v", kind, err)
		}
		j.mu.Lock()
		if j.job != nil && j.job.ID == id {
			j.job.Running = false
			j.job.FinishedAt = time.Now()
			if err != nil {
				j.job.Error = err.Error()
			}
		}
		j.mu.Unlock()
		if file != nil {
			file.Close()
		}
	}()
	c := *j.job
	return &c, true
}

// get devolve uma cópia do job e as linhas a partir de since (índice absoluto).
func (j *jobRunner) get(since int) (job *Job, lines []string, next int, truncated bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	lines = []string{}
	if j.job == nil {
		return nil, lines, 0, false
	}
	c := *j.job
	if since < j.first {
		since, truncated = j.first, since > 0 || j.first > 0
	}
	end := j.first + len(j.lines)
	if since > end {
		since = end
	}
	lines = append(lines, j.lines[since-j.first:]...)
	return &c, lines, end, truncated
}

func (o *onboarding) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/onboarding", o.status)
	mux.HandleFunc("POST /api/onboarding/install", o.install)
	mux.HandleFunc("POST /api/onboarding/configure", o.configure)
	mux.HandleFunc("GET /api/onboarding/job", func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.Atoi(r.URL.Query().Get("since"))
		if since < 0 {
			since = 0
		}
		job, lines, next, truncated := o.jobs.get(since)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"job": job, "lines": lines, "next": next, "truncated": truncated})
	})
	mux.HandleFunc("POST /api/onboarding/done", func(w http.ResponseWriter, r *http.Request) {
		if err := o.state.markDone(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"done": true})
	})
	// Só lê o arquivo de estado: a UI decide o primeiro passo sem esperar o
	// status completo (que roda dokku version e lista as apps).
	mux.HandleFunc("GET /api/onboarding/seen", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]bool{"dokku-seen": o.state.dokkuSeen() || o.state.done()})
	})
	mux.HandleFunc("POST /api/onboarding/dokku-seen", func(w http.ResponseWriter, r *http.Request) {
		if err := o.state.markDokkuSeen(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"dokku-seen": true})
	})
}

func (o *onboarding) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	version, err := o.client.Version(ctx)
	installed := err == nil
	apps := 0
	if installed {
		// O monitor pode ainda não ter coletado depois de uma instalação.
		if list, err := o.monitor.Apps(ctx); err == nil {
			apps = len(list)
		}
	}
	regs, _ := o.client.Registries()
	domains := o.client.GlobalDomains()
	job, _, _, _ := o.jobs.get(0)
	seen := installed && (o.state.dokkuSeen() || o.state.done() || apps > 0)
	// Instalação antiga com apps: grava para o /seen responder certo depois.
	if seen && !o.state.dokkuSeen() {
		o.state.markDokkuSeen()
	}
	// O motivo vem em pt-BR do pacote dokku; aqui sai no idioma da requisição.
	host := dokku.Host(o.mode)
	if key := hostReasonKey(host.ReasonCode); key != "" {
		if host.ReasonArg != "" {
			host.Reason = msg(r, key, host.ReasonArg)
		} else {
			host.Reason = msg(r, key)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"needed": !installed || (!o.state.done() && apps == 0),
		"done":   o.state.done(),
		// Quem já passou pelo assistente ou já tem apps não vê de novo o
		// passo de detecção do Dokku.
		"dokku-seen":        seen,
		"dokku":             map[string]any{"installed": installed, "version": version},
		"host":              host,
		"global-domains":    domains,
		"suggested-domain":  suggestedDomain(r.Host, domains),
		"letsencrypt":       o.client.LetsencryptInstalled(),
		"letsencrypt-email": o.client.LetsencryptEmail() != "",
		"apps":              apps,
		"registries":        len(regs),
		"job":               job,
	})
}

// suggestedDomain: acessando pelo IP, o sslip.io já resolve sem configurar DNS.
func suggestedDomain(host string, domains []string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// Loopback (túnel SSH) e IP privado não servem: o endereço apontaria
	// para a máquina de quem acessa, não para o servidor.
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() {
		return ip.To4().String() + ".sslip.io"
	}
	if len(domains) > 0 {
		return domains[0]
	}
	return ""
}

func (o *onboarding) installed(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := o.client.Version(ctx)
	return err == nil
}

func (o *onboarding) install(w http.ResponseWriter, r *http.Request) {
	if o.installed(r.Context()) {
		writeError(w, r, http.StatusConflict, "err.dokkuInstalled")
		return
	}
	h := dokku.Host(o.mode)
	if !h.CanInstall {
		if key := hostReasonKey(h.ReasonCode); key == "" {
			writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": h.Reason})
		} else if h.ReasonArg != "" {
			writeError(w, r, http.StatusPreconditionFailed, key, h.ReasonArg)
		} else {
			writeError(w, r, http.StatusPreconditionFailed, key)
		}
		return
	}
	// O log do job sai no idioma de quem disparou; step é um código que a
	// UI traduz.
	lang := requestLang(r)
	job, ok := o.jobs.start("install", func(ctx context.Context, log, step func(string)) error {
		step("fetch-latest")
		tag := o.tag
		if tag == "" {
			tag = dokku.LatestTag(ctx)
		}
		if h.Method == dokku.MethodDocker {
			step("docker")
			log("-----> " + msgLang(lang, "joblog.docker"))
			if err := dokku.EnsureDocker(ctx, h, log); err != nil {
				return fmt.Errorf("docker: %w", err)
			}
			log("-----> " + msgLang(lang, "joblog.installingDocker", tag))
			step("install")
			if err := dokku.RunDokkuContainer(ctx, o.client.Runner, tag, log); err != nil {
				return err
			}
		} else {
			log("-----> " + msgLang(lang, "joblog.installing", tag))
			step("install")
			if err := dokku.Bootstrap(ctx, tag, o.dir, log); err != nil {
				var dl *dokku.DownloadError
				if errors.As(err, &dl) {
					return errors.New(errMsg(lang, err))
				}
				return fmt.Errorf("bootstrap: %w", err)
			}
		}
		// O modo (host ou container) é detectado de novo com o Dokku no ar.
		o.client.ResetMode()
		step("verify")
		v, err := o.client.Version(ctx)
		if err != nil {
			if errors.Is(err, dokku.ErrNoVersion) {
				return errors.New(msgLang(lang, "job.bootstrapNoResponse"))
			}
			return fmt.Errorf("%s: %w", msgLang(lang, "job.bootstrapNoResponse"), err)
		}
		log("-----> " + msgLang(lang, "joblog.installed", v))
		o.client.Invalidate()
		o.monitor.Poke()
		return nil
	})
	if !ok {
		writeError(w, r, http.StatusConflict, "err.jobRunning")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (o *onboarding) configure(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain string `json:"domain"`
		SSHKey string `json:"ssh-key"`
		Email  string `json:"letsencrypt-email"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "err.invalidBody")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(body.Domain))
	key := strings.TrimSpace(body.SSHKey)
	email := strings.TrimSpace(body.Email)
	bad := func(key string) { writeError(w, r, http.StatusBadRequest, key) }
	switch {
	case !o.installed(r.Context()):
		writeError(w, r, http.StatusConflict, "err.dokkuNotInstalled")
		return
	case domain != "" && !dokku.ValidDomain(domain):
		bad("err.invalidDomain")
		return
	case key != "" && !dokku.ValidSSHKey(key):
		bad("err.invalidSSHKey")
		return
	case email != "" && !validEmail(email):
		bad("err.invalidEmail")
		return
	case domain == "" && key == "" && email == "":
		bad("err.nothingToConfigure")
		return
	}
	lang := requestLang(r)
	job, ok := o.jobs.start("configure", func(ctx context.Context, log, step func(string)) error {
		defer func() {
			o.client.Invalidate()
			o.monitor.Poke()
		}()
		if domain != "" {
			step("domain")
			log("-----> " + msgLang(lang, "joblog.domain", domain))
			if err := o.client.SetGlobalDomain(ctx, domain); err != nil {
				return err
			}
		}
		if key != "" {
			step("ssh-key")
			log("-----> " + msgLang(lang, "joblog.sshKey"))
			if err := o.client.EnsureSSHKey(ctx, "dokk-admin", key,
				func() { log("-----> " + msgLang(lang, "joblog.sshKeyReplace", "dokk-admin")) },
				func() { log("-----> " + msgLang(lang, "joblog.sshKeyExists")) }); err != nil {
				return err
			}
		}
		if email != "" {
			if !o.client.LetsencryptInstalled() {
				step("letsencrypt-plugin")
				log("-----> " + msgLang(lang, "joblog.letsencryptPlugin"))
				out, err := o.client.InstallLetsencrypt(ctx)
				for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
					if l != "" {
						log(dokku.StripANSI(l))
					}
				}
				if err != nil {
					return err
				}
			}
			step("letsencrypt")
			log("-----> " + msgLang(lang, "joblog.letsencrypt", email))
			if err := o.client.SetupLetsencrypt(ctx, email); err != nil {
				return err
			}
		}
		log("-----> " + msgLang(lang, "joblog.done"))
		return nil
	})
	if !ok {
		writeError(w, r, http.StatusConflict, "err.jobRunning")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// validEmail aceita só o endereço puro (sem "Nome <...>"), que vai como
// argumento do letsencrypt:set.
func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && !strings.HasPrefix(s, "-")
}
