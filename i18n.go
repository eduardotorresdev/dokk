package main

// Mensagens que a API mostra ao usuário (o campo "error" do JSON) em três
// idiomas. A UI manda o idioma ativo no cabeçalho X-Dokk-Lang; sem ele vale o
// Accept-Language e, por fim, o inglês. Os logs continuam em pt-BR e o
// stderr do dokku passa sem tradução.

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"dokk/dokku"
)

const (
	langEN      = "en"
	langES      = "es"
	langPT      = "pt-BR"
	defaultLang = langEN
)

var langs = []string{langEN, langES, langPT}

// normalizeLang aplica as mesmas regras da UI (i18n.js): só o subtag
// primário conta, e qualquer português vira pt-BR.
func normalizeLang(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	primary, _, _ := strings.Cut(strings.ReplaceAll(tag, "_", "-"), "-")
	switch primary {
	case "pt":
		return langPT
	case "es":
		return langES
	case "en":
		return langEN
	}
	return ""
}

// acceptLanguage ordena por q (estável, mantendo a ordem do cabeçalho no
// empate) e devolve o primeiro idioma suportado; "" se nenhum.
func acceptLanguage(h string) string {
	type entry struct {
		tag string
		q   float64
	}
	var list []entry
	for _, part := range strings.Split(h, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.TrimSpace(k) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		if tag = strings.TrimSpace(tag); tag != "" && q > 0 {
			list = append(list, entry{tag, q})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].q > list[j].q })
	for _, e := range list {
		if l := normalizeLang(e.tag); l != "" {
			return l
		}
	}
	return ""
}

// requestLang: X-Dokk-Lang → Accept-Language → inglês.
func requestLang(r *http.Request) string {
	if r == nil {
		return defaultLang
	}
	if l := normalizeLang(r.Header.Get("X-Dokk-Lang")); l != "" {
		return l
	}
	if l := acceptLanguage(r.Header.Get("Accept-Language")); l != "" {
		return l
	}
	return defaultLang
}

// msgLang procura a chave no idioma, depois em inglês, e por fim devolve a
// própria chave. Só formata (Sprintf) quando há argumentos.
func msgLang(lang, key string, args ...any) string {
	s, ok := catalog[lang][key]
	if !ok {
		s, ok = catalog[defaultLang][key]
	}
	if !ok {
		s = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

func msg(r *http.Request, key string, args ...any) string {
	return msgLang(requestLang(r), key, args...)
}

// errMsg traduz os erros tipados que o próprio dokk escreve no pacote dokku;
// o resto (stderr do dokku) passa como veio.
func errMsg(lang string, err error) string {
	var (
		cfg   *dokku.ConfigSetError
		st    *dokku.RegistryStatusError
		login *dokku.RegistryLoginError
		dc    *dokku.DockerConfigError
		dl    *dokku.DownloadError
	)
	switch {
	case errors.As(err, &cfg):
		return msgLang(lang, "err.configSetFailed", strings.Join(cfg.Keys, " "))
	case errors.As(err, &st):
		return msgLang(lang, "err.registryStatus", st.Status)
	case errors.As(err, &login):
		return msgLang(lang, "err.registryLoginFailed", login.Server) + ": " + login.Stderr
	case errors.As(err, &dc):
		return msgLang(lang, "err.dockerConfig") + ": " + dc.Err.Error()
	case errors.As(err, &dl):
		return msgLang(lang, "job.downloadFailed") + ": " + dl.Err.Error()
	case errors.Is(err, dokku.ErrNoVersion):
		return msgLang(lang, "job.bootstrapNoResponse")
	}
	return err.Error()
}

// writeError responde {"error": <mensagem traduzida>}.
func writeError(w http.ResponseWriter, r *http.Request, status int, key string, args ...any) {
	writeJSON(w, status, map[string]string{"error": msg(r, key, args...)})
}

// hostReasonKey traduz o ReasonCode do dokku.HostInfo para a chave do catálogo.
func hostReasonKey(code string) string {
	switch code {
	case "os":
		return "host.unsupportedOS"
	case "arch":
		return "host.unsupportedArch"
	case "root":
		return "host.needsRoot"
	}
	return ""
}

var catalog = map[string]map[string]string{
	langEN: {
		"err.configSetFailed":         "could not save the variable(s) %s",
		"err.registryStatus":          "the registry answered %s",
		"err.registryLoginFailed":     "login to %s failed",
		"err.dockerConfig":            "couldn't read docker's config.json",
		"job.downloadFailed":          "couldn't download bootstrap.sh",
		"joblog.sshKeyReplace":        "The key %s already exists; replacing it",
		"joblog.sshKeyExists":         "The key was already registered; moving on",
		"err.invalidBody":             "invalid request body",
		"err.appNotFound":             "app not found",
		"err.actionRunning":           "another action is already running on this app",
		"err.invalidAppName":          "invalid name: use lowercase letters, numbers and hyphens",
		"err.invalidImage":            "invalid image",
		"err.invalidPort":             "invalid port",
		"err.invalidEnvKey":           "invalid variable: %s",
		"err.appExists":               "an app with this name already exists",
		"err.invalidConfig":           "invalid name or value",
		"err.installSameImage":        "only versions of the app's current image can be installed",
		"err.alreadyDeployed":         "the app is already deployed; change the version in the Versions tab",
		"err.unknownAction":           "unknown action",
		"err.confirmMismatch":         "confirmation does not match the app name",
		"err.logNotFound":             "log not found",
		"err.streamingUnsupported":    "streaming not supported",
		"err.unauthenticated":         "not authenticated",
		"err.setupRequired":           "create the superuser first",
		"err.superuserExists":         "the superuser has already been created",
		"err.invalidEmail":            "invalid email",
		"err.passwordTooShort":        "the password must be at least %d characters long",
		"err.tooManyAttempts":         "too many attempts; wait a few minutes",
		"err.badCredentials":          "incorrect email or password",
		"err.invalidLang":             "invalid language",
		"err.jobRunning":              "another task is already running",
		"err.dokkuInstalled":          "Dokku is already installed",
		"err.dokkuNotInstalled":       "Dokku is not installed",
		"err.invalidDomain":           "invalid domain",
		"err.invalidSSHKey":           "invalid SSH key",
		"err.nothingToConfigure":      "nothing to configure",
		"err.invalidRegistry":         "invalid registry",
		"err.invalidRegistryUser":     "invalid username",
		"err.invalidRegistryPassword": "invalid password or token",
		"err.registryNotFound":        "registry not found",
		"err.cookieRequired":          "sign in to the panel to manage API tokens",
		"err.invalidTokenName":        "invalid token name",
		"err.tooManyTokens":           "limit of %d tokens reached",
		"err.tokenNotFound":           "token not found",
		"err.invalidGitHubAppID":      "invalid App ID",
		"err.invalidGitHubSlug":       "invalid App slug",
		"err.invalidGitHubOrg":        "invalid GitHub organization",
		"err.invalidTagFormat":        "tag format must have exactly one {version} and form a valid tag",
		"err.tagFormatMismatch":       "tag %s does not match this app's format %s",
		"err.invalidGitHubKey":        "invalid private key (RSA PEM)",
		"err.githubNoRepo":            "this app has no linked GitHub repository",
		"err.githubNotConfigured":     "the GitHub App is not configured",
		"err.githubNotInstalled":      "the GitHub App is not installed on this repository",
		"err.invalidRepo":             "invalid repository: use owner/repo",
		"err.invalidTag":              "invalid tag",
		"err.shaNotFound":             "commit not found in the repository",
		"err.tagExists":               "the tag %s already exists",
		"err.oauthTitle":              "Authorization failed",
		"err.oauthHint":               "Go back to the app that opened this page and start the sign-in again.",
		"err.oauthClientID":           "Invalid or missing client_id.",
		"err.oauthRedirect":           "The redirect_uri must be a loopback address (http://127.0.0.1:<port>/… or http://localhost:<port>/…).",
		"err.oauthResponseType":       "Only response_type=code is supported.",
		"err.oauthChallenge":          "A PKCE code_challenge with code_challenge_method=S256 is required.",
		"err.oauthState":              "The state parameter is too long.",
		"err.invalidCmd":              "invalid command",
		"err.invalidExecMode":         "invalid mode: use enter or run",
		"err.invalidProcess":          "invalid process type",
		"err.execTimeout":             "the command took longer than 5 minutes and was stopped",
		"err.invalidLines":            "invalid number of lines",
		"host.unsupportedOS":          "Dokku only runs on Linux (%s).",
		"host.unsupportedArch":        "Unsupported architecture (%s).",
		"host.needsRoot":              "dokk must run as root to install Dokku.",
		"job.bootstrapNoResponse":     "bootstrap finished but dokku did not respond",
		"joblog.installing":           "Installing Dokku %s",
		"joblog.docker":               "Checking Docker",
		"joblog.installingDocker":     "Starting Dokku %s in a container (dokku/dokku)",
		"joblog.installed":            "Dokku %s installed",
		"joblog.domain":               "Global domain: %s",
		"joblog.sshKey":               "Adding the SSH key dokk-admin",
		"joblog.letsencryptPlugin":    "Installing the letsencrypt plugin",
		"joblog.letsencrypt":          "Let's Encrypt: %s",
		"joblog.done":                 "Configuration complete",
	},
	langES: {
		"err.configSetFailed":         "no se pudieron guardar las variables %s",
		"err.registryStatus":          "el registry respondió %s",
		"err.registryLoginFailed":     "falló el inicio de sesión en %s",
		"err.dockerConfig":            "no se pudo leer el config.json de docker",
		"job.downloadFailed":          "no se pudo descargar bootstrap.sh",
		"joblog.sshKeyReplace":        "La clave %s ya existe; reemplazándola",
		"joblog.sshKeyExists":         "La clave ya estaba registrada; continuando",
		"err.invalidBody":             "cuerpo de la solicitud inválido",
		"err.appNotFound":             "app no encontrada",
		"err.actionRunning":           "ya hay una acción en curso en esta app",
		"err.invalidAppName":          "nombre inválido: usa letras minúsculas, números y guiones",
		"err.invalidImage":            "imagen inválida",
		"err.invalidPort":             "puerto inválido",
		"err.invalidEnvKey":           "variable inválida: %s",
		"err.appExists":               "ya existe una app con ese nombre",
		"err.invalidConfig":           "nombre o valor inválido",
		"err.installSameImage":        "solo se pueden instalar versiones de la misma imagen de la app",
		"err.alreadyDeployed":         "la app ya tiene un deploy; cambia la versión en la pestaña Versiones",
		"err.unknownAction":           "acción desconocida",
		"err.confirmMismatch":         "la confirmación no coincide con el nombre de la app",
		"err.logNotFound":             "log no encontrado",
		"err.streamingUnsupported":    "streaming no soportado",
		"err.unauthenticated":         "no autenticado",
		"err.setupRequired":           "crea el superusuario primero",
		"err.superuserExists":         "el superusuario ya fue creado",
		"err.invalidEmail":            "correo electrónico inválido",
		"err.passwordTooShort":        "la contraseña debe tener al menos %d caracteres",
		"err.tooManyAttempts":         "demasiados intentos; espera unos minutos",
		"err.badCredentials":          "correo o contraseña incorrectos",
		"err.invalidLang":             "idioma inválido",
		"err.jobRunning":              "ya hay una tarea en curso",
		"err.dokkuInstalled":          "Dokku ya está instalado",
		"err.dokkuNotInstalled":       "Dokku no está instalado",
		"err.invalidDomain":           "dominio inválido",
		"err.invalidSSHKey":           "clave SSH inválida",
		"err.nothingToConfigure":      "nada que configurar",
		"err.invalidRegistry":         "registry inválido",
		"err.invalidRegistryUser":     "usuario inválido",
		"err.invalidRegistryPassword": "contraseña o token inválido",
		"err.registryNotFound":        "registry no encontrado",
		"err.cookieRequired":          "inicia sesión en el panel para gestionar los tokens de API",
		"err.invalidTokenName":        "nombre de token inválido",
		"err.tooManyTokens":           "se alcanzó el límite de %d tokens",
		"err.tokenNotFound":           "token no encontrado",
		"err.invalidGitHubAppID":      "App ID inválido",
		"err.invalidGitHubSlug":       "slug de la App inválido",
		"err.invalidGitHubOrg":        "organización de GitHub inválida",
		"err.invalidTagFormat":        "el formato de tag debe tener exactamente un {version} y formar una tag válida",
		"err.tagFormatMismatch":       "la tag %s no coincide con el formato de esta app, %s",
		"err.invalidGitHubKey":        "clave privada inválida (PEM RSA)",
		"err.githubNoRepo":            "esta app no tiene un repositorio de GitHub vinculado",
		"err.githubNotConfigured":     "la GitHub App no está configurada",
		"err.githubNotInstalled":      "la GitHub App no está instalada en este repositorio",
		"err.invalidRepo":             "repositorio inválido: usa owner/repo",
		"err.invalidTag":              "tag inválida",
		"err.shaNotFound":             "commit no encontrado en el repositorio",
		"err.tagExists":               "la tag %s ya existe",
		"err.oauthTitle":              "La autorización falló",
		"err.oauthHint":               "Vuelve a la aplicación que abrió esta página e inicia sesión de nuevo.",
		"err.oauthClientID":           "client_id inválido o ausente.",
		"err.oauthRedirect":           "El redirect_uri debe ser una dirección loopback (http://127.0.0.1:<puerto>/… o http://localhost:<puerto>/…).",
		"err.oauthResponseType":       "Solo se admite response_type=code.",
		"err.oauthChallenge":          "Se requiere un code_challenge PKCE con code_challenge_method=S256.",
		"err.oauthState":              "El parámetro state es demasiado largo.",
		"err.invalidCmd":              "comando inválido",
		"err.invalidExecMode":         "modo inválido: usa enter o run",
		"err.invalidProcess":          "tipo de proceso inválido",
		"err.execTimeout":             "el comando tardó más de 5 minutos y se detuvo",
		"err.invalidLines":            "número de líneas inválido",
		"host.unsupportedOS":          "Dokku solo funciona en Linux (%s).",
		"host.unsupportedArch":        "Arquitectura no soportada (%s).",
		"host.needsRoot":              "dokk necesita ejecutarse como root para instalar Dokku.",
		"job.bootstrapNoResponse":     "el bootstrap terminó pero dokku no respondió",
		"joblog.installing":           "Instalando Dokku %s",
		"joblog.docker":               "Comprobando Docker",
		"joblog.installingDocker":     "Iniciando Dokku %s en un contenedor (dokku/dokku)",
		"joblog.installed":            "Dokku %s instalado",
		"joblog.domain":               "Dominio global: %s",
		"joblog.sshKey":               "Registrando la clave SSH dokk-admin",
		"joblog.letsencryptPlugin":    "Instalando el plugin letsencrypt",
		"joblog.letsencrypt":          "Let's Encrypt: %s",
		"joblog.done":                 "Configuración completada",
	},
	langPT: {
		"err.configSetFailed":         "não foi possível salvar as variáveis %s",
		"err.registryStatus":          "o registry respondeu %s",
		"err.registryLoginFailed":     "o login em %s falhou",
		"err.dockerConfig":            "não foi possível ler o config.json do docker",
		"job.downloadFailed":          "não foi possível baixar o bootstrap.sh",
		"joblog.sshKeyReplace":        "A chave %s já existe; substituindo",
		"joblog.sshKeyExists":         "A chave já estava cadastrada; seguindo",
		"err.invalidBody":             "corpo inválido",
		"err.appNotFound":             "app não encontrada",
		"err.actionRunning":           "já existe uma ação em andamento nesta app",
		"err.invalidAppName":          "nome inválido: use letras minúsculas, números e hífen",
		"err.invalidImage":            "imagem inválida",
		"err.invalidPort":             "porta inválida",
		"err.invalidEnvKey":           "variável inválida: %s",
		"err.appExists":               "já existe uma app com esse nome",
		"err.invalidConfig":           "nome ou valor inválido",
		"err.installSameImage":        "só dá pra instalar versões da mesma imagem da app",
		"err.alreadyDeployed":         "a app já tem deploy; troque a versão pela aba Versões",
		"err.unknownAction":           "ação desconhecida",
		"err.confirmMismatch":         "confirmação não confere com o nome da app",
		"err.logNotFound":             "log não encontrado",
		"err.streamingUnsupported":    "streaming não suportado",
		"err.unauthenticated":         "não autenticado",
		"err.setupRequired":           "crie o superusuário primeiro",
		"err.superuserExists":         "o superusuário já foi criado",
		"err.invalidEmail":            "e-mail inválido",
		"err.passwordTooShort":        "a senha precisa ter pelo menos %d caracteres",
		"err.tooManyAttempts":         "muitas tentativas; espere alguns minutos",
		"err.badCredentials":          "e-mail ou senha incorretos",
		"err.invalidLang":             "idioma inválido",
		"err.jobRunning":              "já existe uma tarefa em andamento",
		"err.dokkuInstalled":          "o Dokku já está instalado",
		"err.dokkuNotInstalled":       "o Dokku não está instalado",
		"err.invalidDomain":           "domínio inválido",
		"err.invalidSSHKey":           "chave SSH inválida",
		"err.nothingToConfigure":      "nada para configurar",
		"err.invalidRegistry":         "registry inválido",
		"err.invalidRegistryUser":     "usuário inválido",
		"err.invalidRegistryPassword": "senha ou token inválido",
		"err.registryNotFound":        "registry não encontrado",
		"err.cookieRequired":          "entre no painel para gerenciar os tokens de API",
		"err.invalidTokenName":        "nome de token inválido",
		"err.tooManyTokens":           "limite de %d tokens atingido",
		"err.tokenNotFound":           "token não encontrado",
		"err.invalidGitHubAppID":      "App ID inválido",
		"err.invalidGitHubSlug":       "slug da App inválido",
		"err.invalidGitHubOrg":        "organização do GitHub inválida",
		"err.invalidTagFormat":        "o formato de tag precisa ter exatamente um {version} e formar uma tag válida",
		"err.tagFormatMismatch":       "a tag %s não casa com o formato desta app, %s",
		"err.invalidGitHubKey":        "chave privada inválida (PEM RSA)",
		"err.githubNoRepo":            "esta app não tem repositório do GitHub vinculado",
		"err.githubNotConfigured":     "a GitHub App não está configurada",
		"err.githubNotInstalled":      "a GitHub App não está instalada neste repositório",
		"err.invalidRepo":             "repositório inválido: use owner/repo",
		"err.invalidTag":              "tag inválida",
		"err.shaNotFound":             "commit não encontrado no repositório",
		"err.tagExists":               "a tag %s já existe",
		"err.oauthTitle":              "A autorização falhou",
		"err.oauthHint":               "Volte ao aplicativo que abriu esta página e comece o login de novo.",
		"err.oauthClientID":           "client_id inválido ou ausente.",
		"err.oauthRedirect":           "O redirect_uri precisa ser um endereço de loopback (http://127.0.0.1:<porta>/… ou http://localhost:<porta>/…).",
		"err.oauthResponseType":       "Só response_type=code é suportado.",
		"err.oauthChallenge":          "É preciso um code_challenge PKCE com code_challenge_method=S256.",
		"err.oauthState":              "O parâmetro state é longo demais.",
		"err.invalidCmd":              "comando inválido",
		"err.invalidExecMode":         "modo inválido: use enter ou run",
		"err.invalidProcess":          "tipo de processo inválido",
		"err.execTimeout":             "o comando passou de 5 minutos e foi interrompido",
		"err.invalidLines":            "número de linhas inválido",
		"host.unsupportedOS":          "O Dokku só roda em Linux (%s).",
		"host.unsupportedArch":        "Arquitetura não suportada (%s).",
		"host.needsRoot":              "O dokk precisa rodar como root para instalar o Dokku.",
		"job.bootstrapNoResponse":     "o bootstrap terminou mas o dokku não respondeu",
		"joblog.installing":           "Instalando Dokku %s",
		"joblog.docker":               "Verificando o Docker",
		"joblog.installingDocker":     "Subindo o Dokku %s num container (dokku/dokku)",
		"joblog.installed":            "Dokku %s instalado",
		"joblog.domain":               "Domínio global: %s",
		"joblog.sshKey":               "Cadastrando a chave SSH dokk-admin",
		"joblog.letsencryptPlugin":    "Instalando o plugin letsencrypt",
		"joblog.letsencrypt":          "Let's Encrypt: %s",
		"joblog.done":                 "Configuração concluída",
	},
}
