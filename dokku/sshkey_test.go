package dokku

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// keyRunner responde ao ssh-keys:list com list e falha o add com addErr.
type keyRunner struct {
	recRunner
	list   string
	addErr error
}

func (r *keyRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	r.recRunner.Run(ctx, name, args...)
	if args[0] == "ssh-keys:list" {
		return r.list, nil
	}
	return "", nil
}

func (r *keyRunner) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	r.recRunner.RunInput(ctx, stdin, name, args...)
	return "", r.addErr
}

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl a@b"

func (r *keyRunner) cmds() []string {
	var out []string
	for _, c := range r.calls {
		out = append(out, c.args[0])
	}
	return out
}

func TestEnsureSSHKey(t *testing.T) {
	// Primeira vez: lista vazia, só adiciona.
	r := &keyRunner{}
	if err := NewClient(r).EnsureSSHKey(context.Background(), "dokk-admin", testKey, func() {}, func() {}); err != nil {
		t.Fatal(err)
	}
	if got := r.cmds(); !reflect.DeepEqual(got, []string{"ssh-keys:list", "ssh-keys:add"}) {
		t.Fatalf("primeira vez: %v", got)
	}
	// Retry: o nome já existe, remove e adiciona de novo.
	r = &keyRunner{list: `SHA256:abc NAME="dokk-admin" SSHCOMMAND_ALLOWED_KEYS="none"` + "\n"}
	if err := NewClient(r).EnsureSSHKey(context.Background(), "dokk-admin", testKey, func() {}, func() {}); err != nil {
		t.Fatal(err)
	}
	if got := r.cmds(); !reflect.DeepEqual(got, []string{"ssh-keys:list", "ssh-keys:remove", "ssh-keys:add"}) {
		t.Fatalf("retry: %v", got)
	}
	// Mesma chave com outro nome: o sshcommand recusa como duplicada; segue.
	r = &keyRunner{addErr: errors.New("Duplicate ssh public key specified")}
	if err := NewClient(r).EnsureSSHKey(context.Background(), "dokk-admin", testKey, func() {}, func() {}); err != nil {
		t.Fatalf("duplicada deveria passar: %v", err)
	}
	// Outro erro continua erro.
	r = &keyRunner{addErr: errors.New("boom")}
	if err := NewClient(r).EnsureSSHKey(context.Background(), "dokk-admin", testKey, func() {}, func() {}); err == nil {
		t.Fatal("erro do add sumiu")
	}
}
