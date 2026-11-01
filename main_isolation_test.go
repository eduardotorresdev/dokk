package main

import (
	"os"
	"testing"
)

// Os testes não leem a config nem o DOKK_BUILDER de quem roda.
func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "dokk-cli-test")
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Unsetenv("DOKK_BUILDER")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
