package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dokk/dokku"
)

// version é preenchido no build de release (-ldflags "-X main.version=v1.2.3").
var version = "dev"

//go:embed ui/index.html ui/assets ui/src ui/node_modules/shablon
var uiFiles embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:7070", "endereço HTTP")
	dataDir := flag.String("data", "/var/lib/dokk", "diretório de dados do dokk (superusuário)")
	interval := flag.Duration("interval", 5*time.Second, "intervalo entre checks")
	dokkuTag := flag.String("dokku-tag", "", "versão do Dokku instalada pelo onboarding (vazio = a mais recente)")
	showVersion := flag.Bool("version", false, "mostra a versão e sai")
	flag.Parse()
	if *showVersion {
		fmt.Println("dokk", version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	actions := newActions()
	client := dokku.NewClient(dokku.LocalRunner{})
	monitor := dokku.NewMonitor(client, *interval)
	go monitor.Run(ctx)
	host := &hostSampler{}
	go host.run(ctx, 3*time.Second)
	releases := newReleaseCache(client)
	go func() {
		// Aquece o cache das tags logo após a primeira coleta e a cada 5 min.
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			if apps, err := monitor.Apps(ctx); err == nil {
				releases.warm(ctx, apps)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	ui, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		apps, err := monitor.Apps(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		writeJSON(w, http.StatusOK, apps)
	})
	mux.HandleFunc("GET /api/host", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, host.get())
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, msg(r, "err.streamingUnsupported"), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			changed := monitor.Changed()
			apps, err := monitor.Apps(r.Context())
			if err != nil && r.Context().Err() != nil {
				return
			}
			event, payload := "apps", any(apps)
			if err != nil {
				event, payload = "failure", map[string]string{"error": err.Error()}
			}
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			flusher.Flush()

		wait:
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
				goto wait
			}
		}
	})
	// Resolve {name} contra a lista de apps conhecidas: o nome vira argumento
	// de comando, então nunca repassamos algo que não seja uma app real.
	lookup := func(w http.ResponseWriter, r *http.Request) (dokku.AppWithHistory, bool) {
		app, ok, err := monitor.App(r.Context(), r.PathValue("name"))
		switch {
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
		case !ok:
			writeError(w, r, http.StatusNotFound, "err.appNotFound")
		}
		return app, err == nil && ok
	}

	mux.HandleFunc("GET /api/apps/{name}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		detail, err := monitor.Detail(r.Context(), app.Name)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"app": app, "detail": detail, "action": actions.get(app.Name)})
	})

	// Ações que reiniciam a app demoram (às vezes minutos): rodam em segundo
	// plano e o andamento aparece no detalhe e no status do monitor.
	// start registra e dispara fn em segundo plano; false se já houver
	// outra ação rodando na app.
	// lang é o idioma de quem disparou: o erro guardado na ação sai traduzido.
	start := func(lang, app, label string, fn func(context.Context) error) (*Action, bool) {
		if !actions.start(app, label) {
			return nil, false
		}
		monitor.SetAction(app, label)
		monitor.Poke()
		go func() {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
			defer cancel()
			err := fn(actx)
			if err != nil {
				log.Printf("%s %s: %v", label, app, err)
				err = errors.New(errMsg(lang, err))
			}
			// destroy bem sucedido: some da lista na hora (SSE inclusive),
			// sem esperar a coleta que o Poke dispara. Antes do finish, para
			// quem consulta a ação já não achar a app quando ela terminar.
			if err == nil && label == "destroy" {
				monitor.Remove(app)
			}
			actions.finish(app, err)
			monitor.SetAction(app, "")
			monitor.Poke()
		}()
		return actions.get(app), true
	}
	run := func(w http.ResponseWriter, r *http.Request, app, label string, fn func(context.Context) error) {
		a, ok := start(requestLang(r), app, label, fn)
		if !ok {
			writeError(w, r, http.StatusConflict, "err.actionRunning")
			return
		}
		writeJSON(w, http.StatusAccepted, a)
	}

	// Cria a app (apps:create + porta + variáveis) e, com imagem, já dispara
	// o primeiro deploy em segundo plano.
	mux.HandleFunc("POST /api/apps", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name  string            `json:"name"`
			Image string            `json:"image"`
			Port  int               `json:"port"`
			Env   map[string]string `json:"env"`
			SSL   bool              `json:"ssl"`
		}
		bad := func(key string, args ...any) { writeError(w, r, http.StatusBadRequest, key, args...) }
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			bad("err.invalidBody")
			return
		}
		if !dokku.ValidAppName(body.Name) {
			bad("err.invalidAppName")
			return
		}
		if _, ok := dokku.ParseImage(body.Image); body.Image != "" && !ok {
			bad("err.invalidImage")
			return
		}
		if body.Port < 0 || body.Port > 65535 {
			bad("err.invalidPort")
			return
		}
		for k := range body.Env {
			if !dokku.ValidConfigKey(k) {
				bad("err.invalidEnvKey", k)
				return
			}
		}
		_, exists, err := monitor.App(r.Context(), body.Name)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		if exists {
			writeError(w, r, http.StatusConflict, "err.appExists")
			return
		}
		c, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := client.CreateApp(c, body.Name); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		// Um destroy recente com o mesmo nome a esconderia da lista.
		monitor.Added(body.Name)
		// A app já existe: um erro daqui em diante não desfaz a criação.
		if body.Port > 0 {
			err = client.SetPort(c, body.Name, body.Port)
		}
		if err == nil && len(body.Env) > 0 {
			err = client.SetConfigs(c, body.Name, body.Env, false)
		}
		monitor.Poke()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": errMsg(requestLang(r), err), "created": true, "name": body.Name})
			return
		}
		if body.Image == "" {
			writeJSON(w, http.StatusCreated, map[string]any{"name": body.Name, "action": nil})
			return
		}
		name, image, ssl := body.Name, body.Image, body.SSL
		a, _ := start(requestLang(r), name, "deploy", func(c context.Context) error {
			if err := client.Install(c, name, image); err != nil {
				return err
			}
			// O HTTPS é opcional: com DNS ainda não apontado ou porta 80
			// fechada o letsencrypt falha, mas a app já está no ar. Não vira
			// falha do deploy; dá para ativar depois pelo painel.
			if ssl && client.LetsencryptInstalled() {
				if err := client.EnableSSL(c, name); err != nil {
					log.Printf("deploy %s: letsencrypt:enable falhou (app no ar sem HTTPS): %v", name, err)
				}
			}
			return nil
		})
		writeJSON(w, http.StatusAccepted, map[string]any{"name": name, "action": a})
	})

	mux.HandleFunc("GET /api/apps/{name}/config", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		vars, err := monitor.Config(r.Context(), app.Name)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, vars)
	})

	mux.HandleFunc("PUT /api/apps/{name}/config/{key}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		var body struct {
			Value   string `json:"value"`
			Restart bool   `json:"restart"`
		}
		key := r.PathValue("key")
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || !dokku.ValidConfigKey(key) {
			writeError(w, r, http.StatusBadRequest, "err.invalidConfig")
			return
		}
		if !body.Restart {
			if err := client.SetConfig(r.Context(), app.Name, key, body.Value, false); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
				return
			}
			monitor.RefreshConfig(r.Context(), app.Name)
			writeJSON(w, http.StatusOK, map[string]string{})
			return
		}
		run(w, r, app.Name, "config", func(c context.Context) error { return client.SetConfig(c, app.Name, key, body.Value, true) })
	})

	mux.HandleFunc("DELETE /api/apps/{name}/config/{key}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		key := r.PathValue("key")
		if !dokku.ValidConfigKey(key) {
			writeError(w, r, http.StatusBadRequest, "err.invalidConfig")
			return
		}
		restart := r.URL.Query().Get("restart") == "1"
		if !restart {
			if err := client.UnsetConfig(r.Context(), app.Name, key, false); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
				return
			}
			monitor.RefreshConfig(r.Context(), app.Name)
			writeJSON(w, http.StatusOK, map[string]string{})
			return
		}
		run(w, r, app.Name, "config", func(c context.Context) error { return client.UnsetConfig(c, app.Name, key, true) })
	})

	mux.HandleFunc("GET /api/apps/{name}/versions", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		versions, err := client.Versions(r.Context(), app.Name, app.GitSHA)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		writeJSON(w, http.StatusOK, versions)
	})

	// Tags publicadas no registry da imagem da app (só deploy por imagem).
	mux.HandleFunc("GET /api/apps/{name}/releases", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		ref, isImage := dokku.ParseImage(app.GitSHA)
		if !isImage || app.DeploySource != "docker-image" {
			writeJSON(w, http.StatusOK, map[string]any{"image": nil, "releases": []dokku.Release{}})
			return
		}
		list, err := releases.get(r.Context(), ref)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"image": ref.Name(), "releases": []dokku.Release{}, "error": errMsg(requestLang(r), err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"image": ref.Name(), "current": app.GitSHA, "releases": list})
	})

	// Instala outra versão da mesma imagem (git:from-image). Só aceita o
	// mesmo repositório da imagem atual, nunca uma imagem qualquer.
	mux.HandleFunc("POST /api/apps/{name}/install", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		var body struct {
			Image string `json:"image"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		cur, ok1 := dokku.ParseImage(app.GitSHA)
		next, ok2 := dokku.ParseImage(body.Image)
		if !ok1 || !ok2 || app.DeploySource != "docker-image" || cur.Name() != next.Name() {
			writeError(w, r, http.StatusBadRequest, "err.installSameImage")
			return
		}
		run(w, r, app.Name, "install", func(c context.Context) error { return client.Install(c, app.Name, body.Image) })
	})

	// Primeiro deploy (ou nova tentativa) de uma app criada pelo onboarding.
	mux.HandleFunc("POST /api/apps/{name}/deploy", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		// Já com deploy, só aceita se a app não está saudável (o primeiro
		// deploy subiu mas falha nos checks): o onboarding pode tentar de novo
		// ou trocar a imagem. Uma app saudável troca de versão pela aba Versões.
		if app.Deployed && (app.Checks.Status == dokku.StatusHealthy || app.Checks.Status == dokku.StatusStarting) {
			writeError(w, r, http.StatusConflict, "err.alreadyDeployed")
			return
		}
		var body struct {
			Image string `json:"image"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		if _, ok := dokku.ParseImage(body.Image); !ok {
			writeError(w, r, http.StatusBadRequest, "err.invalidImage")
			return
		}
		// git:from-image com a mesma imagem não vê mudança e pula o deploy.
		if app.DeploySource == "docker-image" && app.GitSHA == body.Image {
			run(w, r, app.Name, "deploy", func(c context.Context) error { return client.Rebuild(c, app.Name) })
			return
		}
		run(w, r, app.Name, "deploy", func(c context.Context) error { return client.Install(c, app.Name, body.Image) })
	})

	mux.HandleFunc("POST /api/apps/{name}/rename", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		var body struct {
			To string `json:"to"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		if !dokku.ValidAppName(body.To) {
			writeError(w, r, http.StatusBadRequest, "err.invalidAppName")
			return
		}
		if _, exists, _ := monitor.App(r.Context(), body.To); exists {
			writeError(w, r, http.StatusConflict, "err.appExists")
			return
		}
		// O nome novo pode ser o de uma app removida há pouco (ver Added).
		monitor.Added(body.To)
		run(w, r, app.Name, "rename", func(c context.Context) error { return client.Rename(c, app.Name, body.To) })
	})

	mux.HandleFunc("GET /api/ssl/autorenew", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": client.SSLAutoRenew()})
	})
	mux.HandleFunc("PUT /api/ssl/autorenew", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		if err := client.SetSSLAutoRenew(r.Context(), body.Enabled); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": client.SSLAutoRenew()})
	})
	mux.HandleFunc("POST /api/apps/{name}/{action}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		action := r.PathValue("action")
		switch action {
		case "start", "stop", "restart":
		case "ssl-renew":
			run(w, r, app.Name, action, func(c context.Context) error { return client.RenewSSL(c, app.Name) })
			return
		default:
			writeError(w, r, http.StatusNotFound, "err.unknownAction")
			return
		}
		run(w, r, app.Name, action, func(c context.Context) error { return client.PsAction(c, app.Name, action) })
	})

	mux.HandleFunc("DELETE /api/apps/{name}", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		// Confirmação explícita: o corpo precisa repetir o nome da app.
		var body struct {
			Confirm string `json:"confirm"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		if body.Confirm != app.Name {
			writeError(w, r, http.StatusBadRequest, "err.confirmMismatch")
			return
		}
		run(w, r, app.Name, "destroy", func(c context.Context) error { return client.Destroy(c, app.Name) })
	})

	mux.HandleFunc("GET /api/apps/{name}/builds/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		log, err := client.BuildLog(app.Name, r.PathValue("id"))
		if err != nil {
			writeError(w, r, http.StatusNotFound, "err.logNotFound")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, log)
	})

	mux.HandleFunc("GET /api/apps/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		app, ok := lookup(w, r)
		if !ok {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, msg(r, "err.streamingUnsupported"), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher.Flush()

		err := dokku.StreamLogs(r.Context(), app.Name, 200, func(line string) {
			data, _ := json.Marshal(line)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		})
		if err != nil && r.Context().Err() == nil {
			data, _ := json.Marshal(err.Error())
			fmt.Fprintf(w, "event: failure\ndata: %s\n\n", data)
			flusher.Flush()
		}
	})

	mux.Handle("GET /", uiHandler(ui))

	// Superusuário criado no primeiro acesso (formulário de setup).
	a, err := newAuth(filepath.Join(*dataDir, "auth.json"))
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	// Idioma da UI escolhido pelo usuário (vale para a instalação toda).
	p := loadPrefs(filepath.Join(*dataDir, "prefs.json"))
	a.prefs = p
	prefsRoutes(mux, p)
	newOnboarding(*dataDir, *dokkuTag, client, monitor).routes(mux)
	registryRoutes(mux, client, monitor)
	mux.HandleFunc("POST /api/setup", a.setup)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/session", a.session)

	srv := &http.Server{Addr: *addr, Handler: sameOrigin(a.middleware(mux))}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("dokk ouvindo em http://%s", *addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// uiHandler serve a UI embutida. Arquivos reais (src, assets, node_modules)
// vão pelo FileServer; qualquer outro caminho é rota da SPA ("/apps/foo",
// "/registries"...) e recebe o index.html, para que links diretos e o F5
// funcionem — o router do cliente decide a tela (inclusive o 404). Ficam de
// fora /api/* desconhecido, diretórios (sem listagem) e arquivo inexistente
// dentro de src/, assets/, node_modules/ ou na raiz com extensão
// ("/favicon.ico"), que respondem 404 de verdade em vez de HTML — senão um
// módulo JS faltando viraria HTML. Rota com ponto ("/apps/meu.app", nome
// válido no Dokku) continua indo para o index.
func uiHandler(ui fs.FS) http.Handler {
	static := http.FileServerFS(ui)
	index, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		log.Fatalf("ui: %v", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Os arquivos embutidos não têm data de modificação; sem no-cache o
		// navegador pode misturar módulos JS antigos e novos depois de um deploy.
		w.Header().Set("Cache-Control", "no-cache")
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if st, err := fs.Stat(ui, p); err == nil && !st.IsDir() {
				if strings.HasSuffix(p, ".webmanifest") {
					w.Header().Set("Content-Type", "application/manifest+json")
				}
				static.ServeHTTP(w, r)
				return
			}
			top, _, nested := strings.Cut(p, "/")
			staticDir := false
			if st, err := fs.Stat(ui, top); err == nil && st.IsDir() {
				staticDir = true
			}
			if top == "api" || staticDir || (!nested && path.Ext(p) != "") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// sameOrigin barra requisições que mudam estado vindas de outro site: só o
// fetch da própria UI manda o cabeçalho X-Dokk (um form externo não consegue).
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Dokk") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
