package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"

	"dokk/client"
)

// `dokk mcp`: servidor MCP por stdio (JSON-RPC 2.0, uma mensagem por linha)
// que expõe a API do dokk como tools. stdout é só do protocolo: logs e o
// output do build vão para stderr e, quando o cliente manda progressToken,
// também como notifications/progress.

const mcpProtocolVersion = "2025-06-18"

func runMCP(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "-help") {
		fmt.Fprintln(os.Stderr, "uso: dokk mcp   (servidor MCP por stdio; usa `dokk login` ou DOKK_URL/DOKK_TOKEN)")
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c, err := client.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dokk mcp:", err)
	}
	s := &mcpServer{client: c, clientErr: err, out: os.Stdout, log: os.Stderr, open: openBrowser}
	if err := s.serve(ctx, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "dokk mcp:", err)
		return 1
	}
	return 0
}

type mcpServer struct {
	client    *client.Client
	clientErr error
	runner    client.Runner     // nil = comandos reais
	hooks     client.HookRunner // nil = sh -c de verdade
	open      func(string) error
	out       io.Writer
	log       io.Writer

	mu  sync.Mutex // serializa escritas em out
	cmu sync.Mutex // protege client/clientErr (a tool login troca)
	wg  sync.WaitGroup
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *mcpServer) send(v any) {
	b, _ := json.Marshal(v)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(append(b, '\n'))
}

func (s *mcpServer) reply(id json.RawMessage, result any, err *rpcError) {
	m := map[string]any{"jsonrpc": "2.0", "id": id}
	if err != nil {
		m["error"] = err
	} else {
		m["result"] = result
	}
	s.send(m)
}

func (s *mcpServer) serve(ctx context.Context, in io.Reader) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	defer s.wg.Wait()
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.reply(json.RawMessage("null"), nil, &rpcError{-32700, "parse error"})
			continue
		}
		isNotification := len(req.ID) == 0
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			pv := p.ProtocolVersion
			if pv == "" {
				pv = mcpProtocolVersion
			}
			s.reply(req.ID, map[string]any{
				"protocolVersion": pv,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "dokk", "version": version},
			}, nil)
		case "ping":
			s.reply(req.ID, map[string]any{}, nil)
		case "tools/list":
			s.reply(req.ID, map[string]any{"tools": mcpTools}, nil)
		case "tools/call":
			// Em paralelo: um build longo não trava ping nem outras chamadas.
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.reply(req.ID, s.call(ctx, req.Params), nil)
			}()
		default:
			if !isNotification {
				s.reply(req.ID, nil, &rpcError{-32601, "method not found: " + req.Method})
			}
		}
	}
	return sc.Err()
}

func schema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var mcpTools = []map[string]any{
	{"name": "list_apps", "description": "Lista as apps do Dokku com status dos checks.", "inputSchema": schema(map[string]any{})},
	{"name": "app_status", "description": "Detalhe de uma app: imagem atual, checks, processos e a ação em andamento.", "inputSchema": schema(map[string]any{"app": str("nome da app")}, "app")},
	{"name": "list_releases", "description": "Releases do GitHub vinculadas à app (mais recente primeiro).", "inputSchema": schema(map[string]any{"app": str("nome da app")}, "app")},
	{"name": "create_release", "description": "Calcula a versão (máximo entre as tags git e as do registry; fora da branch padrão, versão por hash e prerelease), confere que o commit está no remoto, faz build+push da imagem (linux/amd64, build do dokk.yml), cria a release no GitHub e opcionalmente instala até ficar healthy, com os hooks do dokk.yml.", "inputSchema": schema(map[string]any{
		"app":        str("nome da app"),
		"bump":       map[string]any{"type": "string", "enum": []string{"patch", "minor", "major"}, "description": "incremento semver (padrão patch)"},
		"version":    str("versão explícita, ex. 1.4.0 (ganha de bump)"),
		"dir":        str("diretório do repositório com o Dockerfile (absoluto)"),
		"notes":      str("notas da release"),
		"install":    map[string]any{"type": "boolean", "description": "instalar na app e esperar healthy"},
		"prerelease": map[string]any{"type": "boolean"},
		"skip_hooks": map[string]any{"type": "boolean", "description": "NUNCA use sem o usuário pedir explicitamente: pula os hooks pre-install/post-install do dokk.yml (ex. o backup antes do install)."},
	}, "app", "dir")},
	{"name": "install_release", "description": "Instala uma tag da imagem da app e espera ficar healthy. Roda os hooks do dokk.yml (pre-install/post-install) nesta máquina; um pre-install com erro aborta sem tocar na app.", "inputSchema": schema(map[string]any{"app": str("nome da app"), "tag": str("tag da release ou da imagem, ex. v1.4.0, back@1.17.1 ou 1.17.1"), "dir": str("diretório do repositório, para achar o dokk.yml com os hooks (absoluto; padrão: o cwd do servidor MCP)"), "skip_hooks": map[string]any{"type": "boolean", "description": "NUNCA use sem o usuário pedir explicitamente: pula os hooks pre-install/post-install do dokk.yml (ex. o backup antes do install)."}}, "app", "tag")},
	{"name": "rollback_release", "description": "Volta a app para uma versão anterior: a pedida ou, sem versão, a maior versão estável do registry menor que a atual. Passa pelos hooks do dokk.yml com ACTION=rollback.", "inputSchema": schema(map[string]any{"app": str("nome da app"), "version": str("versão/tag de destino (opcional)"), "dir": str("diretório do repositório, para achar o dokk.yml com os hooks (absoluto; padrão: o cwd do servidor MCP)"), "skip_hooks": map[string]any{"type": "boolean", "description": "NUNCA use sem o usuário pedir explicitamente: pula os hooks pre-install/post-install do dokk.yml (ex. o backup antes do install)."}}, "app")},
	{"name": "build_log", "description": "Log de um build/deploy da app.", "inputSchema": schema(map[string]any{"app": str("nome da app"), "id": str("id do build")}, "app", "id")},
	{"name": "app_logs", "description": "Últimas linhas do log de runtime da app.", "inputSchema": schema(map[string]any{"app": str("nome da app"), "lines": map[string]any{"type": "integer", "description": "quantas linhas (padrão 200)"}}, "app")},
	{"name": "start_app", "description": "Inicia a app e espera ficar healthy.", "inputSchema": schema(map[string]any{"app": str("nome da app")}, "app")},
	{"name": "stop_app", "description": "Para a app e espera a ação terminar.", "inputSchema": schema(map[string]any{"app": str("nome da app")}, "app")},
	{"name": "restart_app", "description": "Reinicia a app e espera ficar healthy.", "inputSchema": schema(map[string]any{"app": str("nome da app")}, "app")},
	{"name": "exec", "description": "Roda um comando DENTRO DO CONTAINER da app (não no servidor/host). mode \"enter\" (padrão) entra num container em execução; \"run\" sobe um container novo e descartável da imagem da app. Uma string é executada com sh -c; uma lista vira o argv direto.", "inputSchema": schema(map[string]any{
		"app":     str("nome da app"),
		"command": map[string]any{"description": "comando: string (via sh -c) ou lista de argumentos", "anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
		"mode":    map[string]any{"type": "string", "enum": []string{"enter", "run"}, "description": "enter (padrão) ou run"},
		"process": str("tipo de processo, ex. web (padrão: o do servidor)"),
	}, "app", "command")},
	{"name": "login", "description": "Faz login num painel dokk pelo navegador (OAuth) e grava a credencial em ~/.config/dokk/credentials.json. Necessário antes das outras tools quando não há DOKK_TOKEN.", "inputSchema": schema(map[string]any{"url": str("URL do painel, ex. https://dokk.example.com")}, "url")},
}

type toolArgs struct {
	App        string          `json:"app"`
	Bump       string          `json:"bump"`
	Version    string          `json:"version"`
	Dir        string          `json:"dir"`
	Notes      string          `json:"notes"`
	Install    bool            `json:"install"`
	Prerelease bool            `json:"prerelease"`
	Tag        string          `json:"tag"`
	ID         string          `json:"id"`
	URL        string          `json:"url"`
	Lines      int             `json:"lines"`
	Command    json.RawMessage `json:"command"`
	Mode       string          `json:"mode"`
	Process    string          `json:"process"`
	SkipHooks  bool            `json:"skip_hooks"`
}

// commandArgv aceita "ls -la" (via sh -c) ou ["ls","-la"].
func commandArgv(raw json.RawMessage) ([]string, error) {
	var list []string
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return list, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
		return []string{"sh", "-c", s}, nil
	}
	return nil, fmt.Errorf("parâmetro obrigatório: command (string ou lista)")
}

// toolText é a resposta da tool: sempre texto limpo, sem ANSI.
func toolText(text string, isErr bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": client.StripANSI(text)}}, "isError": isErr}
}

// toolErr: uma frase com o erro e o que fazer a seguir.
func toolErr(what string, err error) map[string]any {
	msg := client.StripANSI(err.Error())
	if what != "" {
		msg = what + ": " + msg
	}
	msg = strings.TrimSuffix(msg, ".") + "."
	if h := mcpHint(err); h != "" {
		msg += " " + h
	}
	return toolText(msg, true)
}

func mcpHint(err error) string {
	switch client.Advise(err) {
	case client.AdviceLogin:
		return "Chame a tool login com a url do painel."
	case client.AdviceBadToken:
		return "O token foi recusado: chame a tool login de novo."
	case client.AdviceAppNotFound:
		return "Confira o nome da app com list_apps."
	case client.AdviceBusy:
		return "Já há uma ação em andamento; acompanhe com app_status e tente depois."
	case client.AdviceTagExists:
		return "Use outra versão (version) ou outro bump."
	case client.AdvicePush:
		return "Faça git push do commit e chame create_release de novo."
	case client.AdviceLinkRepo:
		return "Peça ao usuário para vincular o repositório GitHub à app no painel."
	case client.AdviceImage:
		return "Essa app não faz deploy por imagem; não dá para criar release por aqui."
	case client.AdviceUnreachable:
		return "O painel não respondeu; confira a URL ou se ele está no ar."
	}
	return ""
}

// progressWriter repassa cada linha (sem ANSI) para stderr e, com token,
// como notifications/progress.
type progressWriter struct {
	s     *mcpServer
	token json.RawMessage
	mu    sync.Mutex
	n     int
	buf   []byte
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(client.StripANSI(string(w.buf[:i])))
		w.buf = w.buf[i+1:]
		if line == "" {
			continue
		}
		fmt.Fprintln(w.s.log, line)
		if len(w.token) == 0 {
			continue
		}
		w.n++
		w.s.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{
			"progressToken": w.token, "progress": w.n, "message": line,
		}})
	}
	return len(p), nil
}

func (w *progressWriter) note(msg string) { fmt.Fprintln(w, msg) }

// plainTerm: as mesmas tabelas da CLI, sem cor nem limite de largura.
var plainTerm = term{}

func (s *mcpServer) call(ctx context.Context, raw json.RawMessage) map[string]any {
	var p struct {
		Name      string   `json:"name"`
		Arguments toolArgs `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return toolText("Argumentos inválidos: "+err.Error()+".", true)
	}
	a := p.Arguments
	pw := &progressWriter{s: s, token: p.Meta.ProgressToken}
	if p.Name == "login" {
		return s.login(ctx, a.URL, pw)
	}
	s.cmu.Lock()
	c, cerr := s.client, s.clientErr
	s.cmu.Unlock()
	if c == nil {
		if cerr == nil {
			cerr = client.ErrNotLoggedIn
		}
		return toolErr("", cerr)
	}
	need := func(fields ...string) map[string]any {
		vals := map[string]string{"app": a.App, "dir": a.Dir, "tag": a.Tag, "id": a.ID}
		for _, f := range fields {
			if vals[f] == "" {
				return toolText("Parâmetro obrigatório: "+f+".", true)
			}
		}
		return nil
	}
	var b strings.Builder
	switch p.Name {
	case "list_apps":
		apps, err := c.Apps(ctx)
		if err != nil {
			return toolErr("não consegui listar as apps", err)
		}
		if len(apps) == 0 {
			return toolText("Nenhuma app.", false)
		}
		sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
		plainTerm.table(&b, []string{"app", "status", "imagem", "url"}, appRows(plainTerm, apps))
		return toolText(b.String(), false)
	case "app_status":
		if r := need("app"); r != nil {
			return r
		}
		st, err := c.Status(ctx, a.App)
		if err != nil {
			return toolErr("não consegui ler "+a.App, err)
		}
		fmt.Fprintf(&b, "app: %s\n", st.App.Name)
		for _, r := range statusRows(plainTerm, st) {
			fmt.Fprintf(&b, "%s: %s\n", r[0], r[1])
		}
		return toolText(b.String(), false)
	case "list_releases":
		if r := need("app"); r != nil {
			return r
		}
		l, err := c.Releases(ctx, a.App)
		if err != nil {
			return toolErr("não consegui listar as releases de "+a.App, err)
		}
		if len(l.Releases) == 0 {
			return toolText("Nenhuma release de "+a.App+".", false)
		}
		plainTerm.table(&b, []string{"", "tag", "data", "commit", "url"}, releaseRows(plainTerm, l))
		if l.Current != "" {
			fmt.Fprintf(&b, "\n● = instalada (%s)\n", l.Current)
		}
		return toolText(b.String(), false)
	case "create_release":
		if r := need("app", "dir"); r != nil {
			return r
		}
		cfg, err := client.FindConfig(a.Dir)
		if err != nil {
			return toolErr("dokk.yml inválido", err)
		}
		res, err := c.Release(ctx, client.ReleaseOptions{
			App: a.App, Bump: a.Bump, Version: a.Version, Dir: a.Dir, Notes: a.Notes,
			Install: a.Install, Prerelease: a.Prerelease, Reporter: client.PlainReporter{W: pw}, Runner: s.runner,
			Config: cfg, SkipHooks: a.SkipHooks, Hooks: s.hooks,
		})
		if err != nil {
			return toolErr("release falhou", err)
		}
		fmt.Fprintf(&b, "Release %s criada.\nimagem: %s\ncommit: %s\nurl: %s\n", res.Tag, res.Image, res.SHA, res.URL)
		if res.Installed {
			fmt.Fprintf(&b, "Instalada em %s e healthy.\n", a.App)
		}
		return toolText(b.String(), false)
	case "install_release":
		if r := need("app", "tag"); r != nil {
			return r
		}
		o, err := s.installOptions(a, pw)
		if err != nil {
			return toolErr("dokk.yml inválido", err)
		}
		r, err := c.InstallTag(ctx, a.Tag, o)
		if err != nil {
			return toolErr("install falhou", err)
		}
		return toolText(installText(r), false)
	case "rollback_release":
		if r := need("app"); r != nil {
			return r
		}
		o, err := s.installOptions(a, pw)
		if err != nil {
			return toolErr("dokk.yml inválido", err)
		}
		r, err := c.Rollback(ctx, a.Version, o)
		if err != nil {
			return toolErr("rollback falhou", err)
		}
		return toolText(installText(r), false)
	case "build_log":
		if r := need("app", "id"); r != nil {
			return r
		}
		log, err := c.BuildLog(ctx, a.App, a.ID)
		if err != nil {
			return toolErr("não consegui ler o log do build "+a.ID, err)
		}
		return toolText(log, false)
	case "app_logs":
		if r := need("app"); r != nil {
			return r
		}
		logs, err := c.Logs(ctx, a.App, a.Lines)
		if err != nil {
			return toolErr("não consegui ler os logs de "+a.App, err)
		}
		if strings.TrimSpace(logs) == "" {
			return toolText("Sem linhas de log.", false)
		}
		return toolText(logs, false)
	case "start_app", "stop_app", "restart_app":
		if r := need("app"); r != nil {
			return r
		}
		action := strings.TrimSuffix(p.Name, "_app")
		if err := c.AppAction(ctx, a.App, action, 0, pw.note); err != nil {
			return toolErr(action+" de "+a.App+" falhou", err)
		}
		final := "healthy"
		if action == "stop" {
			final = "parada"
		}
		return toolText(fmt.Sprintf("%s de %s concluído (%s).", action, a.App, final), false)
	case "exec":
		if r := need("app"); r != nil {
			return r
		}
		argv, err := commandArgv(a.Command)
		if err != nil {
			return toolText(err.Error()+".", true)
		}
		res, err := c.Exec(ctx, a.App, client.ExecRequest{Cmd: argv, Mode: a.Mode, Process: a.Process})
		if err != nil {
			return toolErr("exec em "+a.App+" falhou", err)
		}
		return toolText(fmt.Sprintf("exit code %d\n%s", res.ExitCode, res.Output), false)
	}
	return toolText("Tool desconhecida: "+p.Name+". Veja tools/list.", true)
}

func (s *mcpServer) login(ctx context.Context, url string, pw *progressWriter) map[string]any {
	if url == "" {
		return toolText("Parâmetro obrigatório: url.", true)
	}
	c, err := client.LoginAndSave(ctx, client.LoginOptions{URL: url, Notify: func(u string) {
		fmt.Fprintf(pw, "Abra no navegador para autorizar: %s\n", u)
		if s.open != nil {
			s.open(u)
		}
	}})
	if err != nil {
		return toolErr("login falhou", err)
	}
	s.cmu.Lock()
	s.client, s.clientErr = c, nil
	s.cmu.Unlock()
	return toolText("Logado em "+c.URL+".", false)
}

// installOptions monta o install com os hooks do dokk.yml de a.Dir.
func (s *mcpServer) installOptions(a toolArgs, pw *progressWriter) (client.InstallOptions, error) {
	cfg, err := client.FindConfig(a.Dir)
	return client.InstallOptions{
		App: a.App, Config: cfg, SkipHooks: a.SkipHooks, Hooks: s.hooks,
		Reporter: client.PlainReporter{W: pw},
	}, err
}

func installText(r client.InstallResult) string {
	from := r.FromVersion
	if from == "" {
		from = r.FromImage
	}
	t := fmt.Sprintf("%s: %s → %s, healthy.", r.App, from, r.Image)
	if r.Action == "rollback" {
		t = "Rollback de " + t
	}
	if r.HooksRun {
		t += " Hooks do dokk.yml ok."
	}
	return t
}
