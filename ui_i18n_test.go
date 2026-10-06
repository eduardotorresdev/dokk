package main

// Confere os dicionários da UI (ui/src/locales/<idioma>/<ns>.js) no formato
// estrito da spec: uma entrada "chave": "valor", por linha. O mesmo conjunto
// de checagens roda em ui/scripts/check-i18n.mjs.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

var (
	localeEntryRe = regexp.MustCompile(`^\s*"([^"\\]+)"\s*:\s*("(?:[^"\\]|\\.)*")\s*,?\s*$`)
	localeKeyRe   = regexp.MustCompile(`^[a-z]+(\.[A-Za-z0-9_-]+)+$`)
	localeParamRe = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)
	trCallRe      = regexp.MustCompile("\\b(trn?)\\(\\s*[\"`]([^\"`$]+)[\"`]\\s*(\\+?)")
	// tr(`prefixo.${x}`): só o prefixo estático é conferido.
	trTemplateRe = regexp.MustCompile("\\b(trn?)\\(\\s*`([^`$]*)\\$\\{")
	// Erros guardados como { key: "..." } e resolvidos depois por errText.
	errKeyRe = regexp.MustCompile(`\bkey:\s*"([a-z]+\.[^"]+)"`)
	// Chaves montadas em tempo de execução ("prefixo" + código) só valem
	// para estes conjuntos fechados, que têm fallback na UI.
	builtKeyPrefixes = []string{"common.status.", "common.action.running.", "onboarding.job.step.", "onboarding.dokku.reason.", "errors."}
	uiNamespaces     = []string{"app", "auth", "common", "errors", "home", "onboarding", "registries"}
)

// parseLocaleFile lê um dicionário linha a linha; ns é o nome do arquivo.
func parseLocaleFile(path, ns string) (map[string]string, []string) {
	var problems []string
	bad := func(n int, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s:%d: %s", path, n, fmt.Sprintf(format, args...)))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, []string{err.Error()}
	}
	defer f.Close()
	dict := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		switch {
		case trim == "", strings.HasPrefix(trim, "//"), trim == "export default {", trim == "}":
			continue
		}
		m := localeEntryRe.FindStringSubmatch(line)
		if m == nil {
			bad(n, "formato inesperado: %s", trim)
			continue
		}
		key := m[1]
		var val string
		if err := json.Unmarshal([]byte(m[2]), &val); err != nil {
			bad(n, "formato inesperado (valor): %s", trim)
			continue
		}
		if !localeKeyRe.MatchString(key) || !strings.HasPrefix(key, ns+".") {
			bad(n, "chave %q fora do namespace %q", key, ns)
		}
		if _, dup := dict[key]; dup {
			bad(n, "chave duplicada %q", key)
		}
		if strings.TrimSpace(val) == "" {
			bad(n, "valor vazio em %q", key)
		}
		dict[key] = val
	}
	if err := sc.Err(); err != nil {
		problems = append(problems, path+": "+err.Error())
	}
	return dict, problems
}

func localeParams(s string) []string {
	var out []string
	for _, m := range localeParamRe.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// checkUILocales confere src/locales e as chaves estáticas usadas em src.
// need lista os namespaces obrigatórios (nil = nenhum).
func checkUILocales(src string, need []string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	root := filepath.Join(src, "locales")
	dicts := map[string]map[string]string{} // idioma → chave → valor
	files := map[string][]string{}          // idioma → namespaces
	for _, l := range langs {
		dicts[l] = map[string]string{}
		entries, err := os.ReadDir(filepath.Join(root, l))
		if err != nil {
			add("%s: %v", l, err)
			continue
		}
		for _, e := range entries {
			ns, ok := strings.CutSuffix(e.Name(), ".js")
			if e.IsDir() || !ok {
				add("%s/%s: arquivo inesperado", l, e.Name())
				continue
			}
			files[l] = append(files[l], ns)
			d, p := parseLocaleFile(filepath.Join(root, l, e.Name()), ns)
			problems = append(problems, p...)
			for k, v := range d {
				dicts[l][k] = v
			}
		}
	}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if !slices.Contains(langs, e.Name()) {
				add("locales/%s: idioma inesperado", e.Name())
			}
		}
	}
	for _, l := range langs {
		if !slices.Equal(files[l], files[langEN]) {
			add("%s: arquivos %v, en tem %v", l, files[l], files[langEN])
		}
		for _, ns := range need {
			if !slices.Contains(files[l], ns) {
				add("%s: falta o namespace %s", l, ns)
			}
		}
	}
	for _, l := range langs[1:] {
		for k := range dicts[langEN] {
			if _, ok := dicts[l][k]; !ok {
				add("%s: falta a chave %q", l, k)
			}
		}
		for k, v := range dicts[l] {
			en, ok := dicts[langEN][k]
			if !ok {
				add("%s: chave %q não existe em en", l, k)
				continue
			}
			if a, b := localeParams(v), localeParams(en); !slices.Equal(a, b) {
				add("%s: %q com parâmetros %v, en tem %v", l, k, a, b)
			}
		}
	}
	for k := range dicts[langEN] {
		base, cat, _ := cutLast(k)
		switch cat {
		case "one":
			if _, ok := dicts[langEN][base+".other"]; !ok {
				add("en: plural %q sem .other", base)
			}
		case "other":
			if _, ok := dicts[langEN][base+".one"]; !ok {
				add("en: plural %q sem .one", base)
			}
		}
	}

	// Chaves estáticas no código precisam existir em en.
	filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".js") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			add("%s: %v", path, err)
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		for _, m := range trTemplateRe.FindAllStringSubmatch(string(b), -1) {
			if !slices.ContainsFunc(builtKeyPrefixes, func(p string) bool { return strings.HasPrefix(m[2], p) }) {
				add("%s: %s(`%s${...}`) monta chave fora dos conjuntos permitidos", rel, m[1], m[2])
			}
		}
		for _, m := range errKeyRe.FindAllStringSubmatch(string(b), -1) {
			if _, ok := dicts[langEN][m[1]]; !ok {
				add("%s: { key: %q } não existe em en", rel, m[1])
			}
		}
		for _, m := range trCallRe.FindAllStringSubmatch(string(b), -1) {
			fn, key, built := m[1], m[2], m[3] != ""
			switch {
			case built:
				if !slices.ContainsFunc(builtKeyPrefixes, func(p string) bool { return strings.HasPrefix(key, p) }) {
					add("%s: %s(%q + ...) monta chave fora dos conjuntos permitidos", rel, fn, key)
				}
			case fn == "trn":
				for _, cat := range []string{".one", ".other"} {
					if _, ok := dicts[langEN][key+cat]; !ok {
						add("%s: trn(%q) sem %s em en", rel, key, cat)
					}
				}
			default:
				if _, ok := dicts[langEN][key]; !ok {
					add("%s: tr(%q) não existe em en", rel, key)
				}
			}
		}
		return nil
	})
	sort.Strings(problems)
	return problems
}

func cutLast(k string) (base, last string, ok bool) {
	i := strings.LastIndex(k, ".")
	if i < 0 {
		return k, "", false
	}
	return k[:i], k[i+1:], true
}

func TestUILocales(t *testing.T) {
	if _, err := os.Stat("ui/src/locales"); errors.Is(err, os.ErrNotExist) {
		t.Skip("ui/src/locales ainda não existe")
	}
	for _, p := range checkUILocales("ui/src", uiNamespaces) {
		t.Error(p)
	}
}

func TestCheckUILocalesFixtures(t *testing.T) {
	write := func(t *testing.T, dir, rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		fatalIf(t, os.MkdirAll(filepath.Dir(p), 0o755))
		fatalIf(t, os.WriteFile(p, []byte(content), 0o644))
	}
	good := map[string]string{
		"en":    "// Textos (en).\nexport default {\n    \"home.title\": \"Apps\",\n    \"home.hi\": \"Hi {name}\",\n    \"home.n.one\": \"{count} app\",\n    \"home.n.other\": \"{count} apps\",\n}\n",
		"es":    "export default {\n    \"home.title\": \"Apps\",\n    \"home.hi\": \"Hola {name}\",\n    \"home.n.one\": \"{count} app\",\n    \"home.n.other\": \"{count} apps\",\n}\n",
		"pt-BR": "export default {\n    \"home.title\": \"Apps\",\n    \"home.hi\": \"Oi {name}\",\n    \"home.n.one\": \"{count} app\",\n    \"home.n.other\": \"{count} apps\",\n}\n",
	}
	code := "const a = tr(\"home.title\"); trn(\"home.n\", 2)\n"
	setup := func(t *testing.T, override map[string]string, code string) string {
		dir := t.TempDir()
		for l, c := range good {
			if o, ok := override[l]; ok {
				c = o
			}
			write(t, dir, "locales/"+l+"/home.js", c)
		}
		write(t, dir, "pages/home.js", code)
		return dir
	}
	expect := func(t *testing.T, problems []string, want string) {
		t.Helper()
		for _, p := range problems {
			if strings.Contains(p, want) {
				return
			}
		}
		t.Errorf("esperava problema com %q, veio %v", want, problems)
	}

	if p := checkUILocales(setup(t, nil, code), []string{"home"}); len(p) != 0 {
		t.Fatalf("árvore boa com problemas: %v", p)
	}
	missing := strings.Replace(good["es"], "    \"home.title\": \"Apps\",\n", "", 1)
	expect(t, checkUILocales(setup(t, map[string]string{"es": missing}, code), nil), `es: falta a chave "home.title"`)
	extra := strings.Replace(good["pt-BR"], "}\n", "    \"home.extra\": \"x\",\n}\n", 1)
	expect(t, checkUILocales(setup(t, map[string]string{"pt-BR": extra}, code), nil), `pt-BR: chave "home.extra" não existe em en`)
	param := strings.Replace(good["es"], "Hola {name}", "Hola {nome}", 1)
	expect(t, checkUILocales(setup(t, map[string]string{"es": param}, code), nil), "parâmetros")
	badLine := strings.Replace(good["en"], "\"Apps\",", "'Apps',", 1)
	expect(t, checkUILocales(setup(t, map[string]string{"en": badLine}, code), nil), "formato inesperado")
	dup := strings.Replace(good["en"], "}\n", "    \"home.title\": \"Apps\",\n}\n", 1)
	expect(t, checkUILocales(setup(t, map[string]string{"en": dup}, code), nil), "duplicada")
	expect(t, checkUILocales(setup(t, nil, code+"tr(\"home.nope\")\n"), nil), `tr("home.nope")`)
	expect(t, checkUILocales(setup(t, nil, code), []string{"home", "app"}), "falta o namespace app")
	expect(t, checkUILocales(setup(t, nil, code+"trn(\"home.title\", 1)\n"), nil), "sem .one")
	expect(t, checkUILocales(setup(t, nil, code+"tr(\"home.\" + x)\n"), nil), "fora dos conjuntos")
	if p := checkUILocales(setup(t, nil, code+"tr(\"common.status.\" + s)\n"), nil); len(p) != 0 {
		t.Errorf("chave montada permitida: %v", p)
	}
}

func fatalIf(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
