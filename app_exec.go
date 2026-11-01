package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dokk/dokku"
)

// Comando dentro do container da app (dokku enter / dokku run), sem TTY.
// Não é shell no host: o argv sempre começa por "dokku enter|run <app>".

const (
	execTimeout   = 5 * time.Minute
	maxExecArgs   = 256
	maxExecBody   = 64 << 10
	maxLogLines   = 10000
	defaultLogTop = 200
)

var processRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}(\.[0-9]{1,4})?$`)

type execFunc func(ctx context.Context, mode, app, process string, cmd []string) (dokku.ExecResult, error)

// parseCmd aceita lista de argumentos ou string (que vira sh -c).
func parseCmd(raw json.RawMessage) ([]string, bool) {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		if len(list) == 0 || len(list) > maxExecArgs || strings.TrimSpace(list[0]) == "" {
			return nil, false
		}
		return list, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
		return []string{"/bin/sh", "-c", s}, true
	}
	return nil, false
}

// who identifica quem fez a requisição, para o log ("token <id>" ou "sessão").
func (a *auth) who(r *http.Request) string {
	if tok, has := bearer(r); has {
		if id := a.tokens.idOf(tok); id != "" {
			return "token " + id
		}
		return "token ?"
	}
	return "sessão"
}

func execRoutes(mux *http.ServeMux, a *auth, appExists func(context.Context, string) (bool, error), run execFunc) {
	mux.HandleFunc("POST /api/apps/{name}/exec", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		ok, err := appExists(r.Context(), name)
		switch {
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		case !ok:
			writeError(w, r, http.StatusNotFound, "err.appNotFound")
			return
		}
		var body struct {
			Cmd     json.RawMessage `json:"cmd"`
			Mode    string          `json:"mode"`
			Process string          `json:"process"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxExecBody)).Decode(&body); err != nil {
			writeError(w, r, http.StatusBadRequest, "err.invalidBody")
			return
		}
		cmd, ok := parseCmd(body.Cmd)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "err.invalidCmd")
			return
		}
		if body.Mode == "" {
			body.Mode = "enter"
		}
		if body.Mode != "enter" && body.Mode != "run" {
			writeError(w, r, http.StatusBadRequest, "err.invalidExecMode")
			return
		}
		if body.Process == "" {
			body.Process = "web"
		}
		if !processRe.MatchString(body.Process) {
			writeError(w, r, http.StatusBadRequest, "err.invalidProcess")
			return
		}
		// Cancelado se o cliente desconectar (contexto da requisição).
		ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
		defer cancel()
		start := time.Now()
		log.Printf("exec: %s em %s (%s %s): %q", a.who(r), name, body.Mode, body.Process, cmd)
		res, err := run(ctx, body.Mode, name, body.Process, cmd)
		if err != nil {
			log.Printf("exec: %s em %s falhou após %v: %v", a.who(r), name, time.Since(start).Round(time.Millisecond), err)
			if ctx.Err() == context.DeadlineExceeded {
				writeError(w, r, http.StatusGatewayTimeout, "err.execTimeout")
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		log.Printf("exec: %s em %s terminou com %d em %v", a.who(r), name, res.ExitCode, time.Since(start).Round(time.Millisecond))
		writeJSON(w, http.StatusOK, map[string]any{"output": res.Output, "exitCode": res.ExitCode, "truncated": res.Truncated})
	})
}

// logsSnapshot: GET /api/apps/{name}/logs?lines=N sem streaming, em JSON.
// Sem ?lines o handler continua em SSE (o que a UI usa).
func logsSnapshot(w http.ResponseWriter, r *http.Request, logs func(context.Context, string, int) ([]string, error), app string) {
	n := defaultLogTop
	if v := r.URL.Query().Get("lines"); v != "" {
		x, err := strconv.Atoi(v)
		if err != nil || x < 1 {
			writeError(w, r, http.StatusBadRequest, "err.invalidLines")
			return
		}
		n = min(x, maxLogLines)
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	lines, err := logs(ctx, app, n)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}
