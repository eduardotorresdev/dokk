package client

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const pbsYAML = `# dokk.yml do pbs
apps:
  pbs-back:
    build: { context: ., dockerfile: api/Dockerfile }   # padrão: context = dir
    hooks:
      pre-install: ops/dokk/backup-pb.sh                 # relativo à raiz do repo
      post-install: ""                                   # opcional
  pbs-back-qa:
    build: { context: ., dockerfile: api/Dockerfile }
`

func TestParseYAMLSubset(t *testing.T) {
	v, err := ParseYAML(`
a: plain value with: colon
b: 'it''s'
c: "x\ty # not a comment"
d:
  - one
  - "two"
e: [x, 'y z', "w"]
f: { k: v, l: [1, 2] }
g:
h:
- same-indent list
url: http://x.dev/a#frag
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"a":   "plain value with: colon",
		"b":   "it's",
		"c":   "x\ty # not a comment",
		"d":   []any{"one", "two"},
		"e":   []any{"x", "y z", "w"},
		"f":   map[string]any{"k": "v", "l": []any{"1", "2"}},
		"g":   "",
		"h":   []any{"same-indent list"},
		"url": "http://x.dev/a#frag",
	}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("got  %#v\nwant %#v", v, want)
	}
}

func TestParseYAMLErrors(t *testing.T) {
	for _, src := range []string{
		"a: 1\n   b: 2\n",      // indentação inesperada
		"a: 1\na: 2\n",         // chave repetida
		"a: { k: v\n",          // flow sem fechar
		"a: \"open\n",          // aspas
		"just text\n",          // sem chave
		"a:\n\t- tab\n",        // tab
		"a:\n  - x\n  y: 1\n",  // mistura lista e mapa
		"a: [x, y] trailing\n", // sobra
	} {
		if _, err := ParseYAML(src); err == nil {
			t.Errorf("aceitou %q", src)
		}
	}
}

func TestConfigPBS(t *testing.T) {
	cfg, err := ParseConfig([]byte(pbsYAML), false)
	if err != nil {
		t.Fatal(err)
	}
	back := cfg.App("pbs-back")
	if back.Build.Dockerfile != "api/Dockerfile" || back.Build.Context != "." ||
		back.Hooks.PreInstall != "ops/dokk/backup-pb.sh" || back.Hooks.PostInstall != "" {
		t.Fatalf("pbs-back = %+v", back)
	}
	if qa := cfg.App("pbs-back-qa"); qa.Hooks != (Hooks{}) || qa.Build.Dockerfile != "api/Dockerfile" {
		t.Fatalf("pbs-back-qa = %+v", qa)
	}
	if !reflect.DeepEqual(cfg.App("outra"), AppConfig{}) {
		t.Fatal("app ausente deveria ser zero")
	}
	var nilCfg *Config
	if !reflect.DeepEqual(nilCfg.App("x"), AppConfig{}) {
		t.Fatal("config nil")
	}

	j, err := ParseConfig([]byte(`{"apps":{"pbs-back":{"hooks":{"pre-install":"b.sh"}}}}`), true)
	if err != nil || j.App("pbs-back").Hooks.PreInstall != "b.sh" {
		t.Fatalf("json = %+v %v", j, err)
	}
}

func TestFindConfigWalksUpToGitRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "api", "src")
	os.MkdirAll(sub, 0o755)
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	if cfg, err := FindConfig(sub); err != nil || cfg != nil {
		t.Fatalf("sem arquivo: %v %v", cfg, err)
	}
	os.WriteFile(filepath.Join(root, "dokk.yml"), []byte(pbsYAML), 0o644)
	cfg, err := FindConfig(sub)
	if err != nil || cfg == nil || cfg.Root != root {
		t.Fatalf("cfg = %+v %v", cfg, err)
	}
	ctx, df := cfg.BuildPaths("pbs-back", sub)
	if ctx != root || df != filepath.Join(root, "api", "Dockerfile") {
		t.Fatalf("build = %s %s", ctx, df)
	}
	// Sem build na config: contexto = dir, Dockerfile = <contexto>/Dockerfile.
	ctx, df = cfg.BuildPaths("outra", sub)
	if ctx != sub || df != filepath.Join(sub, "Dockerfile") {
		t.Fatalf("padrão = %s %s", ctx, df)
	}

	// Um dokk.yml acima da raiz do git não vale.
	outer := t.TempDir()
	repo := filepath.Join(outer, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(outer, "dokk.yml"), []byte(pbsYAML), 0o644)
	if cfg, _ := FindConfig(repo); cfg != nil {
		t.Fatal("subiu além da raiz do git")
	}

	os.WriteFile(filepath.Join(root, "dokk.yml"), []byte("apps: [x\n"), 0o644)
	if _, err := FindConfig(sub); err == nil {
		t.Fatal("aceitou dokk.yml inválido")
	}
}
