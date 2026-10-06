package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"dokk/dokku"
)

// Logins globais do dokku em registries de imagens (para git:from-image
// de imagens privadas e para listar as tags na aba Versões).
func registryRoutes(mux *http.ServeMux, client *dokku.Client, monitor *dokku.Monitor) {
	list := func(w http.ResponseWriter, r *http.Request) {
		regs, err := client.Registries()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"registries": regs})
	}
	mux.HandleFunc("GET /api/registries", func(w http.ResponseWriter, r *http.Request) { list(w, r) })

	// O corpo carrega a senha: nunca vai para log.
	mux.HandleFunc("POST /api/registries", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Server   string `json:"server"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body)
		server := dokku.NormalizeRegistry(body.Server)
		bad := func(key string) { writeError(w, r, http.StatusBadRequest, key) }
		switch {
		case !dokku.ValidRegistry(server):
			bad("err.invalidRegistry")
			return
		case !dokku.ValidRegistryUser(body.Username):
			bad("err.invalidRegistryUser")
			return
		case !dokku.ValidRegistryPassword(body.Password):
			bad("err.invalidRegistryPassword")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := client.RegistryLogin(ctx, server, body.Username, body.Password); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		monitor.Poke()
		list(w, r)
	})

	mux.HandleFunc("DELETE /api/registries", func(w http.ResponseWriter, r *http.Request) {
		server := dokku.NormalizeRegistry(r.URL.Query().Get("server"))
		regs, err := client.Registries()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		found := false
		for _, reg := range regs {
			found = found || reg.Server == server
		}
		if !found {
			writeError(w, r, http.StatusNotFound, "err.registryNotFound")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := client.RegistryLogout(ctx, server); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": errMsg(requestLang(r), err)})
			return
		}
		list(w, r)
	})
}
