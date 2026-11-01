package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config é o dokk.yml (ou dokk.json) do repositório: como buildar cada app
// e os hooks que rodam na máquina do cliente em volta de todo install.
//
//	apps:
//	  pbs-back:
//	    build: { context: ., dockerfile: api/Dockerfile }
//	    hooks:
//	      pre-install: ops/dokk/backup-pb.sh
//	      post-install: ""
type Config struct {
	Apps map[string]AppConfig `json:"apps"`

	// Root é a raiz do repositório (onde os caminhos são resolvidos); File
	// é o arquivo lido. Não vêm do arquivo.
	Root string `json:"-"`
	File string `json:"-"`
}

// AppConfig é a entrada de uma app no dokk.yml.
type AppConfig struct {
	Build struct {
		Context    string `json:"context"`
		Dockerfile string `json:"dockerfile"`
	} `json:"build"`
	Hooks Hooks `json:"hooks"`
	// Config são variáveis gravadas (sem restart) antes de cada install, para
	// a nova imagem já subir com elas. Placeholders: {version} {tag} {image} {sha}.
	Config map[string]string `json:"config"`
}

// Hooks são comandos `sh -c` rodados com cwd na raiz do repositório.
type Hooks struct {
	PreInstall  string `json:"pre-install"`
	PostInstall string `json:"post-install"`
}

// ConfigNames são os nomes procurados, nessa ordem, em cada diretório.
var ConfigNames = []string{"dokk.yml", "dokk.yaml", "dokk.json"}

// FindConfig procura o dokk.yml subindo de dir até a raiz do git (o
// diretório com .git). Sem arquivo devolve (nil, nil).
func FindConfig(dir string) (*Config, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for d := abs; ; {
		for _, n := range ConfigNames {
			p := filepath.Join(d, n)
			if _, err := os.Stat(p); err == nil {
				cfg, err := LoadConfig(p)
				if err != nil {
					return nil, err
				}
				cfg.Root = gitRoot(d)
				return cfg, nil
			}
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return nil, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil, nil
		}
		d = parent
	}
}

// gitRoot é o primeiro ancestral de dir (inclusive) com .git; sem git, dir.
func gitRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// LoadConfig lê um dokk.yml/dokk.yaml/dokk.json.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := ParseConfig(b, strings.HasSuffix(path, ".json"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	cfg.File, cfg.Root = path, filepath.Dir(path)
	return cfg, nil
}

// ParseConfig decodifica o conteúdo (YAML do subconjunto suportado ou JSON).
func ParseConfig(b []byte, isJSON bool) (*Config, error) {
	var raw any = map[string]any{}
	if isJSON {
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
	} else {
		v, err := ParseYAML(string(b))
		if err != nil {
			return nil, err
		}
		if v != nil {
			raw = v
		}
	}
	// YAML e JSON viram o mesmo struct passando por JSON.
	j, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(j, &cfg); err != nil {
		return nil, fmt.Errorf("formato inválido: %w", err)
	}
	return &cfg, nil
}

// App devolve a configuração da app (zero value sem entrada).
func (c *Config) App(name string) AppConfig {
	if c == nil {
		return AppConfig{}
	}
	return c.Apps[name]
}

// BuildPaths resolve o contexto e o Dockerfile de uma app: relativos à raiz
// do repositório; sem config, contexto = dir e Dockerfile = <contexto>/Dockerfile.
func (c *Config) BuildPaths(app, dir string) (context, dockerfile string) {
	context = dir
	ac := c.App(app)
	if c != nil && ac.Build.Context != "" {
		context = filepath.Join(c.Root, ac.Build.Context)
	}
	dockerfile = filepath.Join(context, "Dockerfile")
	if c != nil && ac.Build.Dockerfile != "" {
		dockerfile = filepath.Join(c.Root, ac.Build.Dockerfile)
	}
	return context, dockerfile
}

// ---- YAML mínimo -----------------------------------------------------------
//
// Cobre o que o dokk.yml usa: mapas (em bloco e { a: b }), strings (nuas,
// 'simples' e "duplas") e listas de strings (- item e [a, b]), com
// comentários #. Âncoras, multilinha, tipos e documentos múltiplos não.

type yamlLine struct {
	indent int
	text   string
	no     int
}

// ParseYAML devolve map[string]any, []any ou string.
func ParseYAML(src string) (any, error) {
	var lines []yamlLine
	for i, l := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "---") && strings.TrimSpace(l) == "---" {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(l, " "), "\t") {
			return nil, fmt.Errorf("linha %d: use espaços, não tab, na indentação", i+1)
		}
		t := stripComment(l)
		if strings.TrimSpace(t) == "" {
			continue
		}
		ind := len(t) - len(strings.TrimLeft(t, " "))
		lines = append(lines, yamlLine{ind, strings.TrimSpace(t), i + 1})
	}
	if len(lines) == 0 {
		return nil, nil
	}
	p := &yamlParser{lines: lines}
	v, err := p.block(lines[0].indent)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		return nil, fmt.Errorf("linha %d: indentação inesperada", p.lines[p.pos].no)
	}
	return v, nil
}

func stripComment(l string) string {
	var q byte
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		case c == '#' && (i == 0 || l[i-1] == ' '):
			return strings.TrimRight(l[:i], " ")
		}
	}
	return strings.TrimRight(l, " ")
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

func (p *yamlParser) block(indent int) (any, error) {
	if strings.HasPrefix(p.lines[p.pos].text, "- ") || p.lines[p.pos].text == "-" {
		return p.list(indent)
	}
	return p.mapping(indent)
}

func (p *yamlParser) list(indent int) (any, error) {
	out := []any{}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent == indent && !(strings.HasPrefix(l.text, "- ") || l.text == "-") {
			break // fim de uma lista na mesma indentação da chave
		}
		if l.indent > indent {
			return nil, fmt.Errorf("linha %d: item de lista esperado", l.no)
		}
		p.pos++
		item := strings.TrimSpace(strings.TrimPrefix(l.text, "-"))
		v, err := parseFlow(item)
		if err != nil {
			return nil, fmt.Errorf("linha %d: %w", l.no, err)
		}
		out = append(out, v)
	}
	return out, nil
}

func (p *yamlParser) mapping(indent int) (any, error) {
	out := map[string]any{}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, fmt.Errorf("linha %d: indentação inesperada", l.no)
		}
		key, rest, ok := splitKey(l.text)
		if !ok {
			return nil, fmt.Errorf("linha %d: esperava \"chave: valor\"", l.no)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("linha %d: chave %q repetida", l.no, key)
		}
		p.pos++
		if rest != "" {
			v, err := parseFlow(rest)
			if err != nil {
				return nil, fmt.Errorf("linha %d: %w", l.no, err)
			}
			out[key] = v
			continue
		}
		// Valor em bloco nas linhas seguintes, mais indentadas (ou, para
		// listas, na mesma indentação, como o YAML permite).
		if p.pos < len(p.lines) {
			n := p.lines[p.pos]
			if n.indent > indent || (n.indent == indent && strings.HasPrefix(n.text, "- ")) {
				v, err := p.block(n.indent)
				if err != nil {
					return nil, err
				}
				out[key] = v
				continue
			}
		}
		out[key] = ""
	}
	return out, nil
}

// splitKey separa "chave: resto" (a chave pode vir entre aspas).
func splitKey(s string) (string, string, bool) {
	if s != "" && (s[0] == '"' || s[0] == '\'') {
		end := strings.IndexByte(s[1:], s[0])
		if end < 0 {
			return "", "", false
		}
		key, rest := s[1:end+1], strings.TrimSpace(s[end+2:])
		if !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		return key, strings.TrimSpace(rest[1:]), true
	}
	i := strings.Index(s, ": ")
	if strings.HasSuffix(s, ":") && (i < 0 || i == len(s)-1) {
		i = len(s) - 1
	}
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
}

// parseFlow lê um valor numa linha: escalar, { ... } ou [ ... ].
func parseFlow(s string) (any, error) {
	if s == "" || !strings.ContainsRune("{['\"", rune(s[0])) {
		return s, nil // escalar nu: a linha inteira
	}
	f := &flow{s: s}
	v, err := f.value()
	if err != nil {
		return nil, err
	}
	f.space()
	if f.i != len(f.s) {
		return nil, fmt.Errorf("sobrou %q depois do valor", f.s[f.i:])
	}
	return v, nil
}

type flow struct {
	s string
	i int
}

func (f *flow) space() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

var errFlow = errors.New("valor malformado")

func (f *flow) value() (any, error) {
	f.space()
	if f.i >= len(f.s) {
		return "", nil
	}
	switch f.s[f.i] {
	case '{':
		f.i++
		out := map[string]any{}
		for {
			f.space()
			if f.i < len(f.s) && f.s[f.i] == '}' {
				f.i++
				return out, nil
			}
			k, err := f.scalar(":")
			if err != nil {
				return nil, err
			}
			if f.i >= len(f.s) || f.s[f.i] != ':' {
				return nil, fmt.Errorf("%w: faltou ':' depois de %q", errFlow, k)
			}
			f.i++
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			out[k] = v
			if err := f.sep('}'); err != nil {
				return nil, err
			}
		}
	case '[':
		f.i++
		out := []any{}
		for {
			f.space()
			if f.i < len(f.s) && f.s[f.i] == ']' {
				f.i++
				return out, nil
			}
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			if err := f.sep(']'); err != nil {
				return nil, err
			}
		}
	}
	return f.scalar("")
}

// sep consome "," ou deixa o fechamento para o laço.
func (f *flow) sep(closer byte) error {
	f.space()
	if f.i < len(f.s) && f.s[f.i] == ',' {
		f.i++
		return nil
	}
	if f.i < len(f.s) && f.s[f.i] == closer {
		return nil
	}
	return fmt.Errorf("%w: esperava ',' ou '%c'", errFlow, closer)
}

// scalar lê uma string nua (até , } ] ou stop) ou entre aspas.
func (f *flow) scalar(stop string) (string, error) {
	f.space()
	if f.i < len(f.s) && (f.s[f.i] == '"' || f.s[f.i] == '\'') {
		q := f.s[f.i]
		var b strings.Builder
		for f.i++; f.i < len(f.s); f.i++ {
			c := f.s[f.i]
			if q == '\'' && c == '\'' {
				if f.i+1 < len(f.s) && f.s[f.i+1] == '\'' { // '' escapa '
					b.WriteByte('\'')
					f.i++
					continue
				}
				f.i++
				return b.String(), nil
			}
			if q == '"' && c == '\\' && f.i+1 < len(f.s) {
				f.i++
				switch f.s[f.i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(f.s[f.i])
				}
				continue
			}
			if q == '"' && c == '"' {
				f.i++
				return b.String(), nil
			}
			b.WriteByte(c)
		}
		return "", fmt.Errorf("%w: aspas sem fechar", errFlow)
	}
	start := f.i
	for f.i < len(f.s) && !strings.ContainsRune(",}]"+stop, rune(f.s[f.i])) {
		f.i++
	}
	return strings.TrimSpace(f.s[start:f.i]), nil
}
