package dokku

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSetConfigs(t *testing.T) {
	r := &recRunner{}
	c := NewClient(r)
	must(t, c.SetConfigs(context.Background(), "app", map[string]string{"B": "dois", "A": "um"}, false))
	want := []string{"config:set", "--encoded", "--no-restart", "app", "A=" + b64("um"), "B=" + b64("dois")}
	if len(r.calls) != 1 || !reflect.DeepEqual(r.calls[0].args, want) {
		t.Fatalf("chamadas: %+v", r.calls)
	}
	r.err = errors.New("config:set app A=" + b64("um") + " falhou")
	err := c.SetConfigs(context.Background(), "app", map[string]string{"A": "um"}, true)
	if err == nil || strings.Contains(err.Error(), b64("um")) || strings.Contains(err.Error(), "um\"") {
		t.Fatalf("erro vazou o valor: %v", err)
	}
	if err := c.SetConfigs(context.Background(), "app", map[string]string{"X=Y": "1"}, false); err == nil {
		t.Fatal("chave inválida deveria falhar")
	}
}

func TestCreateAppInvalid(t *testing.T) {
	r := &recRunner{}
	if err := NewClient(r).CreateApp(context.Background(), "Minha App"); err == nil || len(r.calls) != 0 {
		t.Fatalf("nome inválido chegou no runner: %v %+v", err, r.calls)
	}
}
