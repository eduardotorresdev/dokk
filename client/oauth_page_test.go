package client

import (
	"strings"
	"testing"
)

func TestCallbackPage(t *testing.T) {
	ok := CallbackPage(true, "")
	if !strings.Contains(ok, `class="ok"`) || strings.Contains(ok, `class="detail"`) || !strings.Contains(ok, "#2f6bff") {
		t.Errorf("ok page sem a classe/cor esperada")
	}
	bad := CallbackPage(false, `access_denied <script>alert(1)</script>`)
	if !strings.Contains(bad, `class="err"`) || !strings.Contains(bad, "access_denied &lt;script&gt;") || strings.Contains(bad, "<script>alert") {
		t.Errorf("página de erro não escapou o detalhe:\n%s", bad)
	}
	// Autocontida: nenhum asset do servidor.
	if strings.Contains(ok, `src="/`) || strings.Contains(ok, `href="/`) {
		t.Error("página depende de asset do servidor")
	}
}
