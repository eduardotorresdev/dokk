package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"regexp"
	"slices"
	"testing"

	"dokk/dokku"
)

func TestNormalizeLang(t *testing.T) {
	for in, want := range map[string]string{
		"pt": "pt-BR", "pt-PT": "pt-BR", "PT_br": "pt-BR",
		"es-419": "es", " es ": "es",
		"en-GB": "en", "EN": "en",
		"fr": "", "": "",
	} {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q, quer %q", in, got, want)
		}
	}
}

func TestRequestLang(t *testing.T) {
	cases := []struct{ dokk, accept, want string }{
		{"es", "pt", "es"},
		{"xx", "pt-PT", "pt-BR"},
		{"", "fr-FR,fr;q=0.9,es;q=0.8", "es"},
		{"", "en;q=0.5, es;q=0.9", "es"},
		{"", "de", "en"},
		{"", "*", "en"},
		{"", "", "en"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.dokk != "" {
			r.Header.Set("X-Dokk-Lang", c.dokk)
		}
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if got := requestLang(r); got != c.want {
			t.Errorf("X-Dokk-Lang %q, Accept-Language %q = %q, quer %q", c.dokk, c.accept, got, c.want)
		}
	}
}

var verbRe = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)

// O catálogo tem as mesmas chaves nos três idiomas, sem valor vazio e com os
// mesmos verbos de formatação, na mesma ordem.
func TestCatalogComplete(t *testing.T) {
	if len(catalog) != len(langs) {
		t.Fatalf("catálogo com %d idiomas, quer %d", len(catalog), len(langs))
	}
	for _, l := range langs {
		if catalog[l] == nil {
			t.Fatalf("idioma %s ausente do catálogo", l)
		}
	}
	for key, en := range catalog[langEN] {
		want := verbRe.FindAllString(en, -1)
		for _, l := range langs {
			v, ok := catalog[l][key]
			switch {
			case !ok:
				t.Errorf("%s: falta %q", l, key)
			case v == "":
				t.Errorf("%s: %q vazio", l, key)
			case !slices.Equal(verbRe.FindAllString(v, -1), want):
				t.Errorf("%s: %q com verbos %v, en tem %v", l, key, verbRe.FindAllString(v, -1), want)
			}
		}
	}
	for _, l := range langs {
		for key := range catalog[l] {
			if _, ok := catalog[langEN][key]; !ok {
				t.Errorf("%s: %q não existe em en", l, key)
			}
		}
	}
}

func TestMsgFallback(t *testing.T) {
	if got := msgLang("xx", "err.appNotFound"); got != "app not found" {
		t.Errorf("idioma desconhecido = %q", got)
	}
	if got := msgLang(langPT, "nao.existe"); got != "nao.existe" {
		t.Errorf("chave desconhecida = %q", got)
	}
	if got := msgLang(langES, "err.invalidEnvKey", "X"); got != "variable inválida: X" {
		t.Errorf("com argumento = %q", got)
	}
}

func TestErrMsg(t *testing.T) {
	cases := []struct {
		lang string
		err  error
		want string
	}{
		{langPT, &dokku.ConfigSetError{Keys: []string{"A", "B"}}, "não foi possível salvar as variáveis A B"},
		{langEN, fmt.Errorf("x: %w", &dokku.RegistryStatusError{Status: "401 Unauthorized"}), "the registry answered 401 Unauthorized"},
		{langES, &dokku.RegistryLoginError{Server: "ghcr.io", Stderr: "denied"}, "falló el inicio de sesión en ghcr.io: denied"},
		{langEN, dokku.ErrNoVersion, "bootstrap finished but dokku did not respond"},
		{langEN, errors.New("stderr do dokku"), "stderr do dokku"},
	}
	for _, c := range cases {
		if got := errMsg(c.lang, c.err); got != c.want {
			t.Errorf("errMsg(%s, %v) = %q, want %q", c.lang, c.err, got, c.want)
		}
	}
}
