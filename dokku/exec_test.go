package dokku

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecArgs(t *testing.T) {
	if got := ExecArgs("enter", "web", "web", []string{"ls", "-la"}); !reflect.DeepEqual(got, []string{"dokku", "enter", "web", "web", "ls", "-la"}) {
		t.Errorf("enter = %v", got)
	}
	if got := ExecArgs("run", "web", "web", []string{"rake", "db:migrate"}); !reflect.DeepEqual(got, []string{"dokku", "run", "web", "rake", "db:migrate"}) {
		t.Errorf("run = %v", got)
	}
}

func TestRunCapped(t *testing.T) {
	ctx := context.Background()
	res, err := runCapped(ctx, "sh", []string{"-c", "echo oi; echo erro >&2; exit 3"})
	if err != nil || res.ExitCode != 3 || !strings.Contains(res.Output, "oi") || !strings.Contains(res.Output, "erro") {
		t.Fatalf("res = %+v, %v", res, err)
	}
	res, err = runCapped(ctx, "sh", []string{"-c", "head -c 2000000 /dev/zero"})
	if err != nil || len(res.Output) != ExecMaxOutput || !res.Truncated {
		t.Fatalf("limite: len=%d truncated=%v err=%v", len(res.Output), res.Truncated, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := runCapped(ctx, "sh", []string{"-c", "sleep 10; echo fim"}); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("cancelamento: err=%v em %v", err, time.Since(start))
	}
}

func TestLogs(t *testing.T) {
	c := newClient(t, fakeRunner{"dokku logs": "a\n\x1b[31mb\x1b[0m\n"})
	lines, err := c.Logs(context.Background(), "web", 2)
	if err != nil || !reflect.DeepEqual(lines, []string{"a", "b"}) {
		t.Fatalf("logs = %q, %v", lines, err)
	}
}
