package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCLIConfigBuilder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := runCmd(t, nil, false, "config", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "builder  auto   auto    docker") {
		t.Fatalf("list: %+v", r)
	}
	if r := runCmd(t, nil, false, "config", "set", "builder", "/opt/podman/bin/podman"); r.code != 0 {
		t.Fatalf("set: %+v", r)
	}
	if r := runCmd(t, nil, false, "config", "get", "builder"); r.stdout != "/opt/podman/bin/podman\n" {
		t.Fatalf("get: %+v", r)
	}
	r = runCmd(t, nil, false, "config", "list", "--json")
	var got map[string]map[string]string
	if json.Unmarshal([]byte(r.stdout), &got) != nil || got["builder"]["source"] != "config" || got["builder"]["effective"] != "podman (/opt/podman/bin/podman)" {
		t.Fatalf("list json: %s", r.stdout)
	}
	t.Setenv("DOKK_BUILDER", "docker")
	if r := runCmd(t, nil, false, "config", "list", "--json"); !strings.Contains(r.stdout, `"source": "env"`) {
		t.Fatalf("env: %s", r.stdout)
	}
	if r := runCmd(t, nil, false, "config", "set", "builder", "kaniko"); r.code == 0 {
		t.Fatal("aceitou builder inválido")
	}
	if r := runCmd(t, nil, false, "config", "unset", "builder"); r.code != 0 {
		t.Fatalf("unset: %+v", r)
	}
	if r := runCmd(t, nil, false, "config", "get", "builder"); r.stdout != "\n" {
		t.Fatalf("depois do unset: %q", r.stdout)
	}
}

func TestMCPConfigTool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := &mcpServer{runner: mcpFakeRunner{}} // sem login: config é local
	// As chamadas do MCP rodam em paralelo: uma por vez, para a ordem valer.
	var msgs []map[string]any
	for _, l := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"config","arguments":{"action":"set","key":"builder","value":"podman"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"config","arguments":{"action":"get","key":"builder"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"config","arguments":{"action":"set","key":"builder","value":"x"}}}`,
	} {
		msgs = append(msgs, mcpRun(t, s, l)...)
	}
	text := func(id float64) (string, bool) {
		r := byID(msgs, id)["result"].(map[string]any)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"] == true
	}
	if txt, isErr := text(1); isErr || !strings.Contains(txt, "builder gravado em") {
		t.Fatalf("set: %q", txt)
	}
	if txt, _ := text(2); txt != "builder: podman (origem: config; efetivo: podman (podman))" {
		t.Fatalf("get: %q", txt)
	}
	if _, isErr := text(3); !isErr {
		t.Fatal("aceitou builder inválido")
	}
}
