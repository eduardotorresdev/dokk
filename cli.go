package main

// CLI cliente do dokk: camada fina sobre o pacote client (parse de args +
// formatação). Cada comando chama uma função do client; as mesmas funções
// servem as tools do MCP (mcp.go).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"dokk/client"
)

// cli guarda os destinos e dependências injetáveis (para testes).
type cli struct {
	stdout, stderr io.Writer
	stdin          io.Reader
	out, err       term // perfis de stdout e stderr
	open           func(string) error
	runner         client.Runner
	poll           time.Duration
	newClient      func() (*client.Client, error)
	hooks          client.HookRunner // nil = sh -c de verdade
}

type command struct {
	name, args, help string
	run              func(c *cli, ctx context.Context, args []string) int
}

var commands []command

func init() {
	commands = []command{
		{"login", "[url]", "entra num painel pelo navegador e grava a credencial", (*cli).login},
		{"logout", "[url]", "remove a credencial gravada", (*cli).logout},
		{"whoami", "[--json]", "mostra o painel e o usuário em uso", (*cli).whoami},
		{"apps", "[--json]", "lista as apps com status, imagem e URL", (*cli).apps},
		{"status", "<app> [--json]", "detalhe da app: imagem, versão, commit, processos, checks", (*cli).status},
		{"logs", "<app> [-n N] [--json]", "últimas linhas do log da app", (*cli).logs},
		{"start", "<app>", "inicia a app e espera ficar healthy", actionCmd("start")},
		{"stop", "<app>", "para a app", actionCmd("stop")},
		{"restart", "<app>", "reinicia a app e espera ficar healthy", actionCmd("restart")},
		{"exec", "<app> [--run] [--process web] -- <cmd…>", "roda um comando dentro do container da app", (*cli).exec},
		{"releases", "<app> [--json]", "releases do repositório da app (GitHub ou GitLab)", (*cli).releases},
		{"install", "<app> <tag> [--no-hooks] [--json]", "instala uma tag da imagem (com os hooks do dokk.yml) e espera ficar healthy", (*cli).install},
		{"rollback", "<app> [versão] [--no-hooks] [--json]", "volta para a versão pedida ou para a maior anterior à atual", (*cli).rollback},
		{"release", "--app <app> [--bump …|--version …] [--install]", "build + push + tag e release no repositório (+ install); fora da main, versão por hash", (*cli).release},
		{"config", "get|set|unset builder [valor] · list", "config desta máquina (~/.config/dokk/config.json): builder = auto|docker|podman|caminho", (*cli).config},
		{"build-log", "<app> <id> [--json]", "log de um build", (*cli).buildLog},
		{"mcp", "", "servidor MCP por stdio (para agentes)", nil},
		{"version", "", "versão deste binário", (*cli).version},
		{"help", "", "esta ajuda", (*cli).help},
	}
}

// isCLICommand diz se os.Args[1] é um subcomando cliente.
func isCLICommand(name string) bool {
	if name == "-h" || name == "--help" {
		return true
	}
	for _, c := range commands {
		if c.name == name {
			return true
		}
	}
	return false
}

func runCLI(args []string) int {
	if len(args) > 0 && args[0] == "mcp" {
		return runMCP(args[1:])
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c := &cli{
		stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin,
		out: detectTerm(os.Stdout, os.Getenv), err: detectTerm(os.Stderr, os.Getenv),
		open: openBrowser, newClient: client.FromEnv,
	}
	return c.main(ctx, args)
}

func (c *cli) main(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		return c.help(ctx, nil)
	}
	for _, cmd := range commands {
		if cmd.name == args[0] && cmd.run != nil {
			return cmd.run(c, ctx, args[1:])
		}
	}
	c.ui().Error("Comando desconhecido", fmt.Errorf("%q não é um comando do dokk", args[0]))
	return 2
}

func (c *cli) version(_ context.Context, _ []string) int {
	fmt.Fprintln(c.stdout, "dokk", version)
	return 0
}

func (c *cli) ui() *ui { return newUIWith(c.stderr, c.err) }

func (c *cli) help(_ context.Context, _ []string) int {
	t := c.out
	fmt.Fprintf(c.stdout, "\n %s  %s\n\n", t.S().Fg(cAccent).Bold().Render("◆ dokk"), t.muted("cliente do painel dokk · "+version))
	fmt.Fprintf(c.stdout, " %s\n", t.muted("USO"))
	fmt.Fprintf(c.stdout, "   dokk <comando> [args]      %s\n\n", t.muted("(sem comando: servidor do painel; dokk -h para as flags)"))
	rows := make([][]cell, 0, len(commands))
	for _, cmd := range commands {
		rows = append(rows, []cell{{text: cmd.name, style: t.accent}, {text: cmd.args, style: t.muted}, plain(cmd.help)})
	}
	t.table(c.stdout, []string{"comando", "args", ""}, rows)
	fmt.Fprintf(c.stdout, "\n %s\n", t.muted("AUTENTICAÇÃO"))
	fmt.Fprintf(c.stdout, "   dokk login <url> grava a credencial; em CI use DOKK_URL e DOKK_TOKEN.\n\n")
	return 0
}

// flags cria um FlagSet que escreve erros no stderr.
func (c *cli) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("dokk "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse aceita flags antes e depois dos posicionais. Depois de maxPos
// posicionais (ou de "--"), o resto vai cru em rest.
func parse(fs *flag.FlagSet, args []string, maxPos int) (pos, rest []string, err error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		if maxPos >= 0 && len(pos) == maxPos {
			rest = append(rest, args...)
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos, append(rest, tail...), nil
}

// usage imprime o erro de uso do comando.
func (c *cli) usage(name string, err error) int {
	for _, cmd := range commands {
		if cmd.name == name {
			msg := "uso: dokk " + name + " " + cmd.args
			if err != nil && !errors.Is(err, flag.ErrHelp) {
				msg = err.Error() + "\n   " + msg
			}
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprintf(c.stderr, "%s\n  %s\n", msg, cmd.help)
				return 0
			}
			c.ui().Error("Uso incorreto", errors.New(msg))
			return 2
		}
	}
	return 2
}

// connect devolve o client autenticado ou imprime o erro.
func (c *cli) connect() (*client.Client, bool) {
	cl, err := c.newClient()
	if err != nil {
		c.ui().Error("Sem login", err)
		return nil, false
	}
	return cl, true
}

func (c *cli) fail(title string, err error) int {
	c.ui().Error(title, err)
	return 1
}

func (c *cli) printJSON(v any) int {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(c.stdout, string(b))
	return 0
}

// ---- comandos ----

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func (c *cli) login(ctx context.Context, args []string) int {
	fs := c.flags("login")
	pos, _, err := parse(fs, args, 1)
	if err != nil {
		return c.usage("login", err)
	}
	u := c.ui()
	url := ""
	if len(pos) > 0 {
		url = pos[0]
	} else {
		fmt.Fprintf(c.stderr, " %s URL do painel: ", c.err.accent("?"))
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		url = strings.TrimSpace(line)
	}
	if url == "" {
		return c.usage("login", errors.New("URL obrigatória"))
	}
	u.Header("login", client.NormalizeURL(url))
	cl, err := client.LoginAndSave(ctx, client.LoginOptions{URL: url, Notify: func(s string) {
		if err := c.open(s); err != nil {
			u.Info("Não consegui abrir o navegador; abra este link:")
		} else {
			u.Info("Abrindo o navegador. Se não abrir, use este link:")
		}
		fmt.Fprintf(c.stderr, "   %s\n", c.err.muted(s))
		u.Info("Esperando a autorização %s", c.err.muted("(até 5 min)"))
	}})
	if err != nil {
		return c.fail("Login falhou", err)
	}
	who := ""
	if s, err := cl.Session(ctx); err == nil && s.Email != "" {
		who = " como " + c.err.accent(s.Email)
	}
	u.Success("Logado em %s%s", c.err.accent(cl.URL), who)
	return 0
}

func (c *cli) logout(_ context.Context, args []string) int {
	pos, _, err := parse(c.flags("logout"), args, 1)
	if err != nil {
		return c.usage("logout", err)
	}
	target := ""
	if len(pos) > 0 {
		target = pos[0]
	}
	removed, err := client.Logout(target)
	if err != nil {
		return c.fail("Logout falhou", err)
	}
	if removed == "" {
		c.ui().Info("Nenhuma credencial para remover.")
		return 0
	}
	c.ui().Success("Credencial de %s removida", c.err.accent(removed))
	return 0
}

func (c *cli) whoami(ctx context.Context, args []string) int {
	fs := c.flags("whoami")
	asJSON := fs.Bool("json", false, "")
	if _, _, err := parse(fs, args, 0); err != nil {
		return c.usage("whoami", err)
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	s, err := cl.Session(ctx)
	if err != nil {
		return c.fail("Não consegui confirmar o login em "+cl.URL, err)
	}
	if *asJSON {
		return c.printJSON(s)
	}
	t := c.out
	who := s.Email
	if who == "" {
		who = "(sem e-mail)"
	}
	fmt.Fprintf(c.stdout, " %s %s %s %s\n", t.accent("●"), t.bold(who), t.muted("em"), t.accent(s.URL))
	return 0
}

func (c *cli) apps(ctx context.Context, args []string) int {
	fs := c.flags("apps")
	asJSON := fs.Bool("json", false, "")
	if _, _, err := parse(fs, args, 0); err != nil {
		return c.usage("apps", err)
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	apps, err := cl.Apps(ctx)
	if err != nil {
		return c.fail("Não consegui listar as apps", err)
	}
	if *asJSON {
		return c.printJSON(apps)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	if len(apps) == 0 {
		fmt.Fprintln(c.stdout, c.out.muted(" nenhuma app"))
		return 0
	}
	c.out.table(c.stdout, []string{"app", "status", "imagem", "url"}, appRows(c.out, apps))
	return 0
}

// appRows é a tabela de apps (CLI colorida e MCP em texto puro).
func appRows(t term, apps []client.App) [][]cell {
	rows := make([][]cell, 0, len(apps))
	for _, a := range apps {
		st := a.Checks.Status
		if a.Action != "" {
			st = a.Action + "…"
		}
		img := a.Image()
		if img == "" {
			img = "—"
		}
		u := a.URL()
		if u == "" {
			u = "—"
		}
		rows = append(rows, []cell{{text: a.Name, style: t.bold}, t.statusCell(st), {text: img, style: t.muted}, {text: u, style: t.accent}})
	}
	return rows
}

// statusRows é o conteúdo do card de status.
func statusRows(t term, s client.Status) [][2]string {
	a := s.App
	dash := func(v string) string {
		if v == "" {
			return t.muted("—")
		}
		return v
	}
	st := a.Checks.Status
	if st == "" {
		st = "unknown"
	}
	checks := t.statusMark(st) + " " + st
	if a.Checks.LastCheckAt != nil {
		checks += t.muted(" · " + a.Checks.LastCheckAt.Local().Format("02/01 15:04:05"))
	}
	var procs []string
	names := make([]string, 0, len(a.Processes))
	for n := range a.Processes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := a.Processes[n]
		mark := "running"
		switch {
		case p.Total == 0:
			mark = "stopped"
		case p.Running < p.Total:
			mark = "degraded"
		}
		procs = append(procs, fmt.Sprintf("%s %s %d/%d", t.statusMark(mark), n, p.Running, p.Total))
	}
	commit := s.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	u := a.URL()
	if u != "" {
		u = t.accent(u)
	}
	rows := [][2]string{
		{"status", checks},
		{"imagem", dash(a.Image())},
		{"versão", dash(s.Version)},
		{"commit", dash(commit)},
		{"url", dash(u)},
		{"processos", dash(strings.Join(procs, "  "))},
	}
	if s.Release != "" {
		rows = append(rows, [2]string{"release", s.Release})
	}
	if s.Action != nil && s.Action.Running {
		rows = append(rows, [2]string{"ação", t.statusMark("starting") + " " + s.Action.Name + "…"})
	} else if s.Action != nil && s.Action.Error != "" {
		rows = append(rows, [2]string{"última ação", t.bad("✗ " + s.Action.Name + ": " + s.Action.Error)})
	}
	if a.LastDeployAt != nil {
		rows = append(rows, [2]string{"deploy", a.LastDeployAt.Local().Format("02/01/2006 15:04")})
	}
	return rows
}

func (c *cli) status(ctx context.Context, args []string) int {
	fs := c.flags("status")
	asJSON := fs.Bool("json", false, "")
	pos, _, err := parse(fs, args, 1)
	if err != nil || len(pos) != 1 {
		return c.usage("status", err)
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	s, err := cl.Status(ctx, pos[0])
	if err != nil {
		return c.fail("Não consegui ler "+pos[0], err)
	}
	if *asJSON {
		return c.printJSON(s)
	}
	c.out.box(c.stdout, s.App.Name, statusRows(c.out, s))
	return 0
}

func (c *cli) logs(ctx context.Context, args []string) int {
	fs := c.flags("logs")
	n := fs.Int("n", 200, "")
	asJSON := fs.Bool("json", false, "")
	pos, _, err := parse(fs, args, 1)
	if err != nil || len(pos) != 1 {
		return c.usage("logs", err)
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	out, err := cl.Logs(ctx, pos[0], *n)
	if err != nil {
		return c.fail("Não consegui ler os logs de "+pos[0], err)
	}
	if *asJSON {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if out == "" {
			lines = []string{}
		}
		return c.printJSON(map[string]any{"app": pos[0], "lines": lines})
	}
	fmt.Fprint(c.stdout, out)
	if out != "" && !strings.HasSuffix(out, "\n") {
		fmt.Fprintln(c.stdout)
	}
	return 0
}

var actionTitles = map[string]string{"start": "Iniciando", "stop": "Parando", "restart": "Reiniciando"}

func actionCmd(name string) func(*cli, context.Context, []string) int {
	return func(c *cli, ctx context.Context, args []string) int {
		pos, _, err := parse(c.flags(name), args, 1)
		if err != nil || len(pos) != 1 {
			return c.usage(name, err)
		}
		cl, ok := c.connect()
		if !ok {
			return 1
		}
		app := pos[0]
		u := c.ui()
		u.Header(name, app+" · "+cl.URL)
		u.Step(actionTitles[name] + " " + app)
		err = cl.AppAction(ctx, app, name, c.poll, u.Note)
		final := "healthy"
		if name == "stop" {
			final = "stopped"
		}
		u.Done(final, err)
		if err != nil {
			return c.fail(actionTitles[name]+" "+app+" falhou", err)
		}
		fmt.Fprintf(c.stderr, "\n %s %s %s\n\n", c.err.statusMark(final), c.err.bold(app), final)
		return 0
	}
}

func (c *cli) exec(ctx context.Context, args []string) int {
	fs := c.flags("exec")
	run := fs.Bool("run", false, "")
	process := fs.String("process", "", "")
	asJSON := fs.Bool("json", false, "")
	pos, rest, err := parse(fs, args, 1)
	if err != nil || len(pos) != 1 || len(rest) == 0 {
		if err == nil {
			err = errors.New("informe a app e o comando")
		}
		return c.usage("exec", err)
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	mode := "enter"
	if *run {
		mode = "run"
	}
	res, err := cl.Exec(ctx, pos[0], client.ExecRequest{Cmd: rest, Mode: mode, Process: *process})
	if err != nil {
		return c.fail("exec em "+pos[0]+" falhou", err)
	}
	if *asJSON {
		c.printJSON(res)
	} else {
		fmt.Fprint(c.stdout, res.Output)
	}
	return min(res.ExitCode, 255)
}

func (c *cli) releases(ctx context.Context, args []string) int {
	fs := c.flags("releases")
	asJSON := fs.Bool("json", false, "")
	pos, _, err := parse(fs, args, 1)
	if err != nil || len(pos) != 1 {
		return c.usage("releases", err)
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	l, err := cl.Releases(ctx, pos[0])
	if err != nil {
		return c.fail("Não consegui listar as releases de "+pos[0], err)
	}
	if *asJSON {
		return c.printJSON(l)
	}
	if len(l.Releases) == 0 {
		fmt.Fprintln(c.stdout, c.out.muted(" nenhuma release"))
		return 0
	}
	c.out.table(c.stdout, []string{"", "tag", "data", "commit", "url"}, releaseRows(c.out, l))
	return 0
}

func releaseRows(t term, l client.ReleaseList) [][]cell {
	rows := make([][]cell, 0, len(l.Releases))
	for _, r := range l.Releases {
		mark := cell{text: " "}
		if r.Tag == l.Current || client.ImageTag(r.Tag) == l.Current {
			mark = cell{pre: t.statusMark("running"), text: ""}
		}
		tag := cell{text: r.Tag, style: t.bold}
		if r.Prerelease {
			tag.text += " (pre)"
		}
		date := r.CreatedAt
		if tm, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
			date = tm.Local().Format("02/01/2006 15:04")
		}
		sha := r.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		rows = append(rows, []cell{mark, tag, {text: date, style: t.muted}, plain(sha), {text: r.URL, style: t.accent}})
	}
	return rows
}

// hookFlags são as flags comuns a todo install.
type hookFlags struct {
	dir     *string
	noHooks *bool
	yes     *bool
}

func addHookFlags(fs *flag.FlagSet) hookFlags {
	return hookFlags{fs.String("dir", ".", ""), fs.Bool("no-hooks", false, ""), fs.Bool("yes", false, "")}
}

// installOptions lê o dokk.yml (subindo de --dir) e confirma o --no-hooks:
// pular os hooks exige --yes ou digitar o nome da app.
func (c *cli) installOptions(app string, f hookFlags) (client.InstallOptions, bool) {
	o := client.InstallOptions{App: app, Hooks: c.hooks, PollInterval: c.poll}
	cfg, err := client.FindConfig(*f.dir)
	if err != nil {
		c.fail("dokk.yml inválido", err)
		return o, false
	}
	o.Config = cfg
	h := cfg.App(app).Hooks
	if *f.noHooks && (h.PreInstall != "" || h.PostInstall != "") {
		if !*f.yes {
			fmt.Fprintf(c.stderr, "\n %s --no-hooks pula os hooks de %s (pre-install: %q, post-install: %q).\n Digite o nome da app para confirmar: ",
				c.err.bad("!"), c.err.bold(app), h.PreInstall, h.PostInstall)
			line, _ := bufio.NewReader(c.stdin).ReadString('\n')
			if strings.TrimSpace(line) != app {
				fmt.Fprintln(c.stderr)
				c.fail("Install cancelado", errors.New("--no-hooks não confirmado (sem terminal, passe --yes junto)"))
				return o, false
			}
		}
		o.SkipHooks = true
	}
	return o, true
}

func (c *cli) installSummary(r client.InstallResult) {
	from := r.FromVersion
	if from == "" {
		from = r.FromImage
	}
	hooks := ""
	if r.HooksRun {
		hooks = " " + c.err.muted("· hooks ok")
	}
	fmt.Fprintf(c.stderr, "\n %s %s %s %s%s\n\n", c.err.statusMark("healthy"), c.err.bold(r.App),
		c.err.muted(from+" →"), c.err.accent(r.Image), hooks)
}

func (c *cli) install(ctx context.Context, args []string) int {
	fs := c.flags("install")
	asJSON := fs.Bool("json", false, "")
	hf := addHookFlags(fs)
	pos, _, err := parse(fs, args, 2)
	if err != nil || len(pos) != 2 {
		return c.usage("install", err)
	}
	app, tag := pos[0], pos[1]
	o, ok := c.installOptions(app, hf)
	if !ok {
		return 1
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	u := c.ui()
	u.Header("install", app+" · "+cl.URL)
	o.Reporter = u
	r, err := cl.InstallTag(ctx, tag, o)
	if err != nil {
		return c.fail("Install de "+tag+" falhou", err)
	}
	if *asJSON {
		return c.printJSON(r)
	}
	c.installSummary(r)
	return 0
}

func (c *cli) rollback(ctx context.Context, args []string) int {
	fs := c.flags("rollback")
	asJSON := fs.Bool("json", false, "")
	hf := addHookFlags(fs)
	pos, _, err := parse(fs, args, 2)
	if err != nil || len(pos) < 1 || len(pos) > 2 {
		return c.usage("rollback", err)
	}
	app, version := pos[0], ""
	if len(pos) == 2 {
		version = pos[1]
	}
	o, ok := c.installOptions(app, hf)
	if !ok {
		return 1
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	u := c.ui()
	u.Header("rollback", app+" · "+cl.URL)
	o.Reporter = u
	r, err := cl.Rollback(ctx, version, o)
	if err != nil {
		return c.fail("Rollback de "+app+" falhou", err)
	}
	if *asJSON {
		return c.printJSON(r)
	}
	c.installSummary(r)
	return 0
}

// config lê e grava a config desta máquina. O builder efetivo segue
// --builder > DOKK_BUILDER > config.json > auto.
func (c *cli) config(ctx context.Context, args []string) int {
	fs := c.flags("config")
	asJSON := fs.Bool("json", false, "")
	pos, _, err := parse(fs, args, 3)
	if err != nil || len(pos) == 0 {
		return c.usage("config", err)
	}
	uc, err := client.LoadUserConfig()
	if err != nil {
		return c.fail("Não consegui ler a config", err)
	}
	switch action := pos[0]; {
	case action == "list" && len(pos) == 1:
		value, source, err := client.BuilderSetting("")
		if err != nil {
			return c.fail("Não consegui ler a config", err)
		}
		b, derr := client.ResolveBuilder(ctx, c.runnerOrExec(), "")
		eff := b.Kind
		if derr != nil {
			eff = "nenhum (" + derr.Error() + ")"
		} else if b.Bin != b.Kind {
			eff += " (" + b.Bin + ")"
		}
		if *asJSON {
			return c.printJSON(map[string]any{"builder": map[string]string{"value": value, "source": source, "effective": eff}})
		}
		c.out.table(c.stdout, []string{"chave", "valor", "origem", "efetivo"}, [][]cell{{plain("builder"), {text: value, style: c.out.bold}, {text: source, style: c.out.muted}, plain(eff)}})
		return 0
	case action == "get" && len(pos) == 2:
		v, err := uc.Get(pos[1])
		if err != nil {
			return c.fail("Chave inválida", err)
		}
		if *asJSON {
			return c.printJSON(map[string]string{pos[1]: v})
		}
		fmt.Fprintln(c.stdout, v)
		return 0
	case action == "set" && len(pos) == 3:
		if err := uc.Set(pos[1], pos[2]); err != nil {
			return c.fail("Valor inválido", err)
		}
	case action == "unset" && len(pos) == 2:
		if err := uc.Set(pos[1], ""); err != nil {
			return c.fail("Chave inválida", err)
		}
	default:
		return c.usage("config", nil)
	}
	if err := client.SaveUserConfig(uc); err != nil {
		return c.fail("Não consegui gravar a config", err)
	}
	p, _ := client.UserConfigPath()
	fmt.Fprintf(c.stderr, " %s %s %s\n", c.err.statusMark("healthy"), pos[1], c.err.muted("gravado em "+p))
	return 0
}

func (c *cli) runnerOrExec() client.Runner {
	if c.runner != nil {
		return c.runner
	}
	return client.ExecRunner{}
}

func (c *cli) buildLog(ctx context.Context, args []string) int {
	fs := c.flags("build-log")
	asJSON := fs.Bool("json", false, "")
	pos, _, err := parse(fs, args, 2)
	if err != nil || len(pos) != 2 {
		return c.usage("build-log", err)
	}
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	log, err := cl.BuildLog(ctx, pos[0], pos[1])
	if err != nil {
		return c.fail("Não consegui ler o log do build "+pos[1], err)
	}
	if *asJSON {
		return c.printJSON(map[string]string{"app": pos[0], "id": pos[1], "log": log})
	}
	fmt.Fprint(c.stdout, log)
	return 0
}

func (c *cli) release(ctx context.Context, args []string) int {
	fs := c.flags("release")
	var o client.ReleaseOptions
	fs.StringVar(&o.App, "app", "", "")
	fs.StringVar(&o.Bump, "bump", "", "")
	fs.StringVar(&o.Version, "version", "", "")
	fs.StringVar(&o.Notes, "notes", "", "")
	fs.BoolVar(&o.Install, "install", false, "")
	fs.BoolVar(&o.Prerelease, "prerelease", false, "")
	fs.StringVar(&o.Image, "image", "", "")
	fs.StringVar(&o.Platform, "platform", "linux/amd64", "")
	fs.StringVar(&o.Builder, "builder", "", "")
	asJSON := fs.Bool("json", false, "")
	hf := addHookFlags(fs)
	pos, _, err := parse(fs, args, 1)
	if err == nil && o.App == "" && len(pos) == 1 {
		o.App, pos = pos[0], nil
	}
	if err != nil || o.App == "" || len(pos) > 0 {
		return c.usage("release", err)
	}
	o.Dir = *hf.dir
	iopts, ok := c.installOptions(o.App, hf)
	if !ok {
		return 1
	}
	o.Config, o.SkipHooks, o.Hooks = iopts.Config, iopts.SkipHooks, c.hooks
	cl, ok := c.connect()
	if !ok {
		return 1
	}
	u := c.ui()
	u.Header("release", o.App+" · "+cl.URL)
	o.Reporter, o.Runner, o.PollInterval = u, c.runner, c.poll
	res, err := cl.Release(ctx, o)
	if res.Tag != "" {
		status := c.err.muted("○") + " release criada (sem install)"
		switch {
		case err != nil:
			status = c.err.bad("✗ falhou")
		case res.Installed:
			status = c.err.statusMark("healthy") + " instalada · healthy"
		}
		rows := [][2]string{{"app", o.App}, {"versão", c.err.bold(res.Tag)}, {"imagem", res.Image}}
		if res.SHA != "" {
			rows = append(rows, [2]string{"commit", res.SHA})
		}
		if res.URL != "" {
			rows = append(rows, [2]string{"release", c.err.accent(res.URL)})
		}
		rows = append(rows, [2]string{"status", status})
		fmt.Fprintln(c.stderr)
		c.err.box(c.stderr, "release "+res.Tag, rows)
	}
	if err != nil {
		return c.fail("Release falhou", err)
	}
	if *asJSON {
		c.printJSON(res)
	}
	fmt.Fprintln(c.stderr)
	return 0
}
