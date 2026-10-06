package dokku

import (
	"errors"
	"strings"
)

// Erros escritos pelo próprio dokk (não stderr do dokku). Ficam tipados e em
// texto técnico neutro para o pacote main traduzir pelo catálogo antes de
// mostrar ao usuário.

// ConfigSetError: config:set/unset falhou. O stderr é descartado de propósito
// porque repete os argumentos, que carregam o valor do secret.
type ConfigSetError struct {
	Keys []string
}

func (e *ConfigSetError) Error() string {
	return "config:set " + strings.Join(e.Keys, " ") + " failed"
}

// RegistryStatusError: a API do registry respondeu com status diferente de 200.
type RegistryStatusError struct {
	Status string
}

func (e *RegistryStatusError) Error() string { return "registry: HTTP " + e.Status }

// RegistryLoginError: registry:login falhou; Stderr é a saída do dokku (já
// sem a senha) e passa sem tradução.
type RegistryLoginError struct {
	Server, Stderr string
}

func (e *RegistryLoginError) Error() string {
	return "registry:login " + e.Server + ": " + e.Stderr
}

// DockerConfigError: o config.json do docker não pôde ser lido.
type DockerConfigError struct {
	Err error
}

func (e *DockerConfigError) Error() string { return "docker config.json: " + e.Err.Error() }
func (e *DockerConfigError) Unwrap() error { return e.Err }

// DownloadError: o download do bootstrap.sh falhou.
type DownloadError struct {
	Err error
}

func (e *DownloadError) Error() string { return "download bootstrap.sh: " + e.Err.Error() }
func (e *DownloadError) Unwrap() error { return e.Err }

// ErrNoVersion: `dokku version` rodou mas não devolveu uma versão.
var ErrNoVersion = errors.New("dokku version: no output")
