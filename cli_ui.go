package main

// Mini "lipgloss" só com stdlib para a CLI do dokk: perfil de cor
// (truecolor → 256 → nenhum), estilos, largura visível (sem ANSI, com runas
// largas), tabelas, cards com borda arredondada, badges de status, blocos de
// erro e progresso por passos com spinner e janela rolante da saída.
// Em CI, sem TTY, com NO_COLOR ou TERM=dumb tudo vira texto puro com
// timestamps. Só a CLI usa isto; o MCP nunca escreve cor.

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"dokk/client"
)

// Paleta da UI (tema escuro de ui/src/style.css).
const (
	cAccent  = "#4d84ff"
	cInfo    = "#5b92ff"
	cOkSoft  = "#13254d"
	cWarn    = "#f5b13d"
	cBad     = "#ff5c61"
	cRestart = "#a685ff"
	cRemove  = "#d9601c"
	cMuted   = "#8d8a85"
)

type profile int

const (
	profNone profile = iota
	prof256
	profTrue
)

// term descreve um destino de saída: perfil de cor, se anima spinner e a
// largura (0 = sem limite).
type term struct {
	prof    profile
	spinner bool
	width   int
	now     func() time.Time
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// detectTerm decide o perfil de w a partir do ambiente.
func detectTerm(w io.Writer, getenv func(string) string) term {
	t := term{now: time.Now}
	ci := strings.ToLower(getenv("CI"))
	if !isTTY(w) || getenv("NO_COLOR") != "" || (ci != "" && ci != "false" && ci != "0") || getenv("TERM") == "dumb" {
		return t
	}
	t.spinner = true
	t.width = termWidth(w, getenv)
	switch ct := strings.ToLower(getenv("COLORTERM")); {
	case ct == "truecolor" || ct == "24bit":
		t.prof = profTrue
	default:
		t.prof = prof256
	}
	return t
}

func termWidth(w io.Writer, getenv func(string) string) int {
	if n, err := strconv.Atoi(getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	if f, ok := w.(*os.File); ok {
		if n := ttyWidth(f); n > 0 {
			return n
		}
	}
	return 80
}

func hexRGB(hex string) (int, int, int) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

func to256(r, g, b int) int {
	q := func(x int) int { return (x*5 + 127) / 255 }
	return 16 + 36*q(r) + 6*q(g) + q(b)
}

func (t term) color(hex string, bg bool) string {
	r, g, b := hexRGB(hex)
	base := 38
	if bg {
		base = 48
	}
	switch t.prof {
	case profTrue:
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", base, r, g, b)
	case prof256:
		return fmt.Sprintf("\x1b[%d;5;%dm", base, to256(r, g, b))
	}
	return ""
}

// style é um estilo encadeável à la lipgloss.
type style struct {
	t          term
	fg, bg     string
	bold, dim  bool
	italicFree bool
}

func (t term) S() style                  { return style{t: t} }
func (s style) Fg(hex string) style      { s.fg = hex; return s }
func (s style) Bg(hex string) style      { s.bg = hex; return s }
func (s style) Bold() style              { s.bold = true; return s }
func (s style) Dim() style               { s.dim = true; return s }
func (s style) Render(str string) string { return s.render(str) }

func (s style) render(str string) string {
	if s.t.prof == profNone || str == "" {
		return str
	}
	var b strings.Builder
	if s.bold {
		b.WriteString("\x1b[1m")
	}
	if s.dim {
		b.WriteString("\x1b[2m")
	}
	if s.fg != "" {
		b.WriteString(s.t.color(s.fg, false))
	}
	if s.bg != "" {
		b.WriteString(s.t.color(s.bg, true))
	}
	if b.Len() == 0 {
		return str
	}
	return b.String() + str + "\x1b[0m"
}

func (t term) accent(s string) string { return t.S().Fg(cAccent).Render(s) }
func (t term) muted(s string) string  { return t.S().Fg(cMuted).Render(s) }
func (t term) bad(s string) string    { return t.S().Fg(cBad).Render(s) }
func (t term) bold(s string) string   { return t.S().Bold().Render(s) }

// runeWidth: 2 para runas largas (CJK, emoji), 0 para combinantes.
func runeWidth(r rune) int {
	switch {
	case r == 0 || r < 32 || (r >= 0x7f && r < 0xa0):
		return 0
	case r >= 0x300 && r <= 0x36f, r >= 0x200b && r <= 0x200f, r == 0xfe0f:
		return 0
	case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe30 && r <= 0xfe4f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f900 && r <= 0x1f9ff) ||
		(r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

// width é a largura visível de s (sem ANSI).
func width(s string) int {
	n := 0
	for _, r := range client.StripANSI(s) {
		n += runeWidth(r)
	}
	return n
}

// truncate corta texto puro em w colunas, terminando com "…".
func truncate(s string, w int) string {
	if w <= 0 || width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	n := 0
	var b strings.Builder
	for _, r := range s {
		rw := runeWidth(r)
		if n+rw > w-1 {
			break
		}
		b.WriteRune(r)
		n += rw
	}
	return b.String() + "…"
}

func pad(s string, w int) string {
	if d := w - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// cell é um valor de tabela: texto puro + estilo aplicado depois do corte.
type cell struct {
	text  string
	style func(string) string
	pre   string // prefixo já estilizado (ex. símbolo de status), conta na largura
}

func plain(s string) cell { return cell{text: s} }

func (c cell) w() int { return width(c.pre) + width(c.text) }

func (c cell) render(maxw int) string {
	t := c.text
	if maxw > 0 {
		t = truncate(t, maxw-width(c.pre))
	}
	if c.style != nil {
		t = c.style(t)
	}
	return c.pre + t
}

// table renderiza colunas alinhadas; cabeçalho em muted e caixa-alta.
// Se passar da largura do terminal, encolhe as colunas mais largas.
func (t term) table(w io.Writer, header []string, rows [][]cell) {
	n := len(header)
	ws := make([]int, n)
	for i, h := range header {
		ws[i] = width(h)
	}
	for _, r := range rows {
		for i := 0; i < n && i < len(r); i++ {
			ws[i] = max(ws[i], r[i].w())
		}
	}
	const gap = 2
	if t.width > 0 {
		total := func() int {
			s := gap * (n - 1)
			for _, x := range ws {
				s += x
			}
			return s + 1
		}
		for total() > t.width {
			wi := 0
			for i := range ws {
				if ws[i] > ws[wi] {
					wi = i
				}
			}
			if ws[wi] <= 8 {
				break
			}
			ws[wi]--
		}
	}
	line := func(cells []string) {
		var b strings.Builder
		b.WriteString(" ")
		for i, c := range cells {
			if i == n-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(pad(c, ws[i]) + strings.Repeat(" ", gap))
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	hs := make([]string, n)
	for i, h := range header {
		hs[i] = t.muted(truncate(strings.ToUpper(h), ws[i]))
	}
	line(hs)
	for _, r := range rows {
		cs := make([]string, n)
		for i := 0; i < n; i++ {
			if i < len(r) {
				cs[i] = r[i].render(ws[i])
			}
		}
		line(cs)
	}
}

// statusMark é o símbolo/cor de um status (mesmo mapa em apps, status e
// no fim do install).
func (t term) statusMark(status string) string {
	sym, col := "○", cMuted
	st := strings.ToLower(strings.TrimSuffix(status, "…"))
	switch st { // ações em andamento ("restart…") viram o status equivalente
	case "restart":
		st = "restarting"
	case "start", "install", "deploy", "rebuild", "config", "rename":
		st = "starting"
	case "destroy":
		st = "removing"
	case "stop":
		st = "stopped"
	}
	switch st {
	case "healthy", "running":
		sym, col = "●", cAccent
	case "starting":
		sym, col = "◐", cInfo
	case "restarting":
		sym, col = "◐", cRestart
	case "removing":
		sym, col = "◐", cRemove
	case "degraded", "unhealthy":
		sym, col = "●", cWarn
	case "crashed", "failed", "error":
		sym, col = "✗", cBad
	case "stopped", "suspended", "paused":
		sym, col = "○", cMuted
	}
	return t.S().Fg(col).Render(sym)
}

func (t term) statusCell(status string) cell {
	if status == "" {
		status = "unknown"
	}
	return cell{pre: t.statusMark(status) + " ", text: status}
}

// box desenha uma caixa arredondada com título e linhas rótulo/valor.
func (t term) box(w io.Writer, title string, rows [][2]string) {
	lw := 0
	for _, r := range rows {
		lw = max(lw, width(r[0]))
	}
	inner := width(title) + 3
	for _, r := range rows {
		inner = max(inner, lw+2+width(r[1]))
	}
	if t.width > 0 {
		inner = min(inner, t.width-5)
	}
	b := func(s string) string { return t.accent(s) }
	head := " " + t.bold(title) + " "
	fmt.Fprintf(w, " %s%s%s\n", b("╭─"), head, b(strings.Repeat("─", max(0, inner+1-width(head)))+"╮"))
	for _, r := range rows {
		v := r[1]
		if width(v) > inner-lw-2 {
			v = truncate(client.StripANSI(v), inner-lw-2)
		}
		content := t.muted(pad(r[0], lw)) + "  " + v
		fmt.Fprintf(w, " %s %s %s\n", b("│"), pad(content, inner), b("│"))
	}
	fmt.Fprintf(w, " %s\n", b("╰"+strings.Repeat("─", inner+2)+"╯"))
}

// ui é a saída de progresso/mensagens da CLI (stderr). Implementa
// client.Reporter.
type ui struct {
	w io.Writer
	t term

	mu      sync.Mutex
	n       int
	title   string
	sub     string
	started time.Time
	window  []string // últimas linhas da saída (janela rolante)
	tail    []string // últimas 30 linhas (dump em falha)
	region  int      // linhas da região viva na tela
	frame   int
	stop    chan struct{}
	stopped chan struct{}
	partial []byte
}

const (
	windowLines = 6
	tailLines   = 30
)

func newUIWith(w io.Writer, t term) *ui {
	if t.now == nil {
		t.now = time.Now
	}
	return &ui{w: w, t: t}
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (u *ui) live() bool { return u.t.spinner }

func (u *ui) stamp() string { return u.t.now().Format("15:04:05") }

// Header: marca + comando + host.
func (u *ui) Header(cmd, host string) {
	if !u.live() && u.t.prof == profNone {
		fmt.Fprintf(u.w, "dokk %s %s\n", cmd, host)
		return
	}
	fmt.Fprintf(u.w, "\n %s  %s  %s\n\n", u.t.S().Fg(cAccent).Bold().Render("◆ dokk"), u.t.bold(cmd), u.t.muted(host))
}

func (u *ui) num(n int) string {
	if u.t.prof == profNone {
		return strconv.Itoa(n) + "."
	}
	return u.t.S().Fg(cAccent).Bg(cOkSoft).Bold().Render(" " + strconv.Itoa(n) + " ")
}

func (u *ui) erase() {
	if u.region == 0 {
		return
	}
	fmt.Fprint(u.w, "\r\x1b[2K")
	for i := 1; i < u.region; i++ {
		fmt.Fprint(u.w, "\x1b[1A\x1b[2K")
	}
	u.region = 0
}

func (u *ui) draw() {
	u.erase()
	el := fmtDur(u.t.now().Sub(u.started).Truncate(100 * time.Millisecond))
	head := fmt.Sprintf(" %s %s %s", u.t.accent(spinFrames[u.frame%len(spinFrames)]), u.num(u.n), u.title)
	if u.sub != "" {
		head += u.t.muted(" · " + u.sub)
	}
	head += "  " + u.t.muted(el)
	fmt.Fprint(u.w, head)
	u.region = 1
	maxw := 0
	if u.t.width > 0 {
		maxw = u.t.width - 8
	}
	for _, l := range u.window {
		fmt.Fprintf(u.w, "\n     %s %s", u.t.muted("│"), u.t.S().Dim().Render(truncate(l, maxw)))
		u.region++
	}
}

func (u *ui) Step(title string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.n++
	u.title, u.sub, u.started = title, "", u.t.now()
	u.window, u.tail = nil, nil
	if !u.live() {
		fmt.Fprintf(u.w, "%s %s %s…\n", u.t.muted(u.stamp()), u.num(u.n), title)
		return
	}
	u.draw()
	u.stop, u.stopped = make(chan struct{}), make(chan struct{})
	go func(stop, stopped chan struct{}) {
		defer close(stopped)
		tk := time.NewTicker(80 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				u.mu.Lock()
				u.frame++
				u.draw()
				u.mu.Unlock()
			}
		}
	}(u.stop, u.stopped)
}

// Note atualiza o subtítulo do passo (ex. "restarting…").
func (u *ui) Note(msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	msg = client.StripANSI(msg)
	if !u.live() {
		fmt.Fprintf(u.w, "%s    · %s\n", u.t.muted(u.stamp()), msg)
		return
	}
	u.sub = msg
	u.draw()
}

func (u *ui) addLine(l string) {
	l = strings.TrimRight(client.StripANSI(l), " \t")
	if strings.TrimSpace(l) == "" {
		return
	}
	u.tail = append(u.tail, l)
	if len(u.tail) > tailLines {
		u.tail = u.tail[len(u.tail)-tailLines:]
	}
	if !u.live() {
		fmt.Fprintf(u.w, "    %s\n", u.t.S().Dim().Render(l))
		return
	}
	u.window = append(u.window, l)
	if len(u.window) > windowLines {
		u.window = u.window[len(u.window)-windowLines:]
	}
	u.draw()
}

func (u *ui) Done(detail string, err error) {
	u.mu.Lock()
	stop, stopped := u.stop, u.stopped
	u.stop = nil
	u.mu.Unlock()
	if stop != nil {
		close(stop)
		<-stopped
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if l := string(u.partial); strings.TrimSpace(l) != "" {
		u.partial = nil
		u.addLine(l)
	}
	u.partial = nil
	u.erase()
	el := fmtDur(u.t.now().Sub(u.started))
	prefix := ""
	if !u.live() {
		prefix = u.t.muted(u.stamp()) + " "
	} else {
		prefix = " "
	}
	if err != nil {
		fmt.Fprintf(u.w, "%s%s %s %s  %s\n", prefix, u.t.bad("✗"), u.num(u.n), u.title, u.t.muted(el))
		if u.live() && len(u.tail) > 0 {
			fmt.Fprintf(u.w, "     %s\n", u.t.muted(fmt.Sprintf("últimas %d linhas:", len(u.tail))))
			for _, l := range u.tail {
				fmt.Fprintf(u.w, "     %s %s\n", u.t.muted("│"), u.t.S().Dim().Render(l))
			}
		}
		return
	}
	d := ""
	if detail != "" {
		d = "  " + u.t.S().Fg(cInfo).Render(detail)
	}
	fmt.Fprintf(u.w, "%s%s %s %s%s  %s\n", prefix, u.t.accent("✓"), u.num(u.n), u.title, d, u.t.muted(el))
}

// Output é o writer da saída de comandos (docker): janela rolante em TTY,
// linhas indentadas sem TTY.
func (u *ui) Output() io.Writer { return uiOutput{u} }

type uiOutput struct{ u *ui }

func (o uiOutput) Write(b []byte) (int, error) {
	u := o.u
	u.mu.Lock()
	defer u.mu.Unlock()
	u.partial = append(u.partial, b...)
	for {
		i := strings.IndexByte(string(u.partial), '\n')
		if i < 0 {
			break
		}
		l := string(u.partial[:i])
		u.partial = u.partial[i+1:]
		u.addLine(l)
	}
	return len(b), nil
}

// Error imprime o bloco "✗ Título / mensagem / → o que fazer".
func (u *ui) Error(title string, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fmt.Fprintf(u.w, "\n %s %s\n", u.t.bad("✗"), u.t.S().Fg(cBad).Bold().Render(title))
	fmt.Fprintf(u.w, "   %s\n", client.StripANSI(err.Error()))
	if h := cliHint(err); h != "" {
		fmt.Fprintf(u.w, "   %s %s\n", u.t.accent("→"), h)
	}
	fmt.Fprintln(u.w)
}

func (u *ui) Info(format string, a ...any) {
	fmt.Fprintf(u.w, " %s %s\n", u.t.accent("›"), fmt.Sprintf(format, a...))
}

func (u *ui) Success(format string, a ...any) {
	fmt.Fprintf(u.w, " %s %s\n", u.t.accent("✓"), fmt.Sprintf(format, a...))
}

// cliHint é o "o que fazer" para a CLI.
func cliHint(err error) string {
	switch client.Advise(err) {
	case client.AdviceLogin:
		return "rode `dokk login <url>` (ou defina DOKK_URL e DOKK_TOKEN)"
	case client.AdviceBadToken:
		return "o token foi recusado: rode `dokk login` de novo (ou confira DOKK_TOKEN)"
	case client.AdviceAppNotFound:
		return "confira o nome com `dokk apps`"
	case client.AdviceBusy:
		return "já há uma ação em andamento nessa app; espere terminar (`dokk status <app>`)"
	case client.AdviceTagExists:
		return "use outra versão (--version) ou outro --bump"
	case client.AdvicePush:
		return "faça git push do commit e rode de novo"
	case client.AdviceLinkRepo:
		return "vincule o repositório GitHub à app no painel"
	case client.AdviceImage:
		return "informe o repositório da imagem com --image"
	case client.AdviceUnreachable:
		return "confira se o painel está no ar e a URL (`dokk whoami`)"
	}
	return ""
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}
