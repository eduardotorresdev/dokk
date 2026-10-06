import { t, store, watch } from "shablon"
import { api, domains, navigate, langSelect, logo, setPageTitle, truncateMiddle } from "../shared.js"
import { ensureServerLang, errText, register, tr } from "../i18n.js"
import { PROVIDERS } from "./registries.js"
import en from "../locales/en/onboarding.js"
import es from "../locales/es/onboarding.js"
import ptBR from "../locales/pt-BR/onboarding.js"

register({ en, es, "pt-BR": ptBR })

const SAVE_KEY = "dokk:onboarding"
const INSTALL_TAG = "v0.38.31" // igual a dokku.InstallTag
const MAX_TERM_LINES = 2000
const RESUME_MS = 15 * 60 * 1000 // retoma o deploy salvo só até 15 min depois
const PREPARE_MS = 2500 // tempo mínimo da animação "Preparando o ambiente"

// Rótulos são getters: lidos dentro de bindings, seguem o idioma ativo.
const STEPS = [
    { key: "dokku", get label() { return tr("onboarding.steps.dokku") } },
    { key: "configure", get label() { return tr("onboarding.steps.configure") } },
    { key: "method", get label() { return tr("onboarding.steps.method") } },
    { key: "image", get label() { return tr("onboarding.steps.image") } },
    { key: "app", get label() { return tr("onboarding.steps.app") } },
    { key: "deploy", get label() { return tr("onboarding.steps.deploy") } },
]

// Métodos de deploy do Dokku; por enquanto só a imagem pronta funciona.
// icon: traços SVG (viewBox 24) desenhados em methodIcon.
const METHODS = [
    {
        key: "image", get title() { return tr("onboarding.method.image.title") }, get text() { return tr("onboarding.method.image.text") },
        icon: '<path d="M12 3 20 7.5v9L12 21l-8-4.5v-9Z"/><path d="M4 7.5 12 12l8-4.5M12 12v9"/>',
    },
    {
        key: "git-push", get title() { return tr("onboarding.method.gitPush.title") }, get text() { return tr("onboarding.method.gitPush.text") }, soon: true,
        icon: '<circle cx="6" cy="6" r="2.5"/><circle cx="6" cy="18" r="2.5"/><circle cx="18" cy="9" r="2.5"/><path d="M6 8.5v7M18 11.5c0 3-3 3.5-9.5 5"/>',
    },
    {
        key: "git-sync", get title() { return tr("onboarding.method.gitSync.title") }, get text() { return tr("onboarding.method.gitSync.text") }, soon: true,
        icon: '<path d="M5 4h11l3 3v13H5Z"/><path d="M9 4v5h6V4M9 14h6M9 17h4"/>',
    },
    {
        key: "dockerfile", get title() { return tr("onboarding.method.dockerfile.title") }, get text() { return tr("onboarding.method.dockerfile.text") }, soon: true,
        icon: '<path d="M4 20h16M6 20V10l6-5 6 5v10"/><path d="M10 20v-5h4v5"/>',
    },
    {
        key: "buildpacks", get title() { return tr("onboarding.method.buildpacks.title") }, get text() { return tr("onboarding.method.buildpacks.text") }, soon: true,
        icon: '<path d="m12 3 1.8 4.7L18.5 9.5l-4.7 1.8L12 16l-1.8-4.7L5.5 9.5l4.7-1.8Z"/><path d="M18 15l.9 2.1L21 18l-2.1.9L18 21l-.9-2.1L15 18l2.1-.9Z"/>',
    },
    {
        key: "tar", get title() { return tr("onboarding.method.tar.title") }, get text() { return tr("onboarding.method.tar.text") }, soon: true,
        icon: '<path d="M4 7h16v13H4ZM3 4h18v3H3Z"/><path d="M10 11h4"/>',
    },
]

function methodIcon(paths) {
    const el = t.span({ className: "method-icon", "html-aria-hidden": "true" })
    el.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${paths}</svg>`
    return el
}

// Fases do deploy mostradas no passo Deploy, na ordem; o ícone segue o
// mesmo traço SVG de methodIcon.
const DEPLOY_PHASES = [
    { key: "pull", get label() { return tr("onboarding.deploy.phase.pull") }, icon: '<path d="M12 4v11"/><path d="m7 10 5 5 5-5"/><path d="M5 20h14"/>' },
    { key: "build", get label() { return tr("onboarding.deploy.phase.build") }, icon: '<path d="M12 3 3 7.5 12 12l9-4.5Z"/><path d="m3 12 9 4.5 9-4.5"/><path d="m3 16.5 9 4.5 9-4.5"/>' },
    { key: "start", get label() { return tr("onboarding.deploy.phase.start") }, icon: '<path d="M4 6h16v12H4Z"/><path d="m10 9.5 4 2.5-4 2.5Z"/>' },
    { key: "checks", get label() { return tr("onboarding.deploy.phase.checks") }, icon: '<path d="M3 12h4l2-5 4 10 2-5h6"/>' },
    { key: "live", get label() { return tr("onboarding.deploy.phase.live") }, icon: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.5 2.5 3.8 5.5 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.5-3.8-9S9.5 5.5 12 3Z"/>' },
]
const PHASE_CHECK = '<path d="m5 12.5 4.5 4.5L19 7.5"/>'
const PHASE_FAIL = '<path d="M6 6l12 12M18 6 6 18"/>'

// Fase atual (índice em DEPLOY_PHASES) a partir das marcas do Dokku no log;
// a última marca encontrada vence.
function phaseFromLog(text) {
    const s = text.toLowerCase()
    const marks = [
        [4, ["application deployed"]],
        [3, ["running checks", "checks", "healthcheck"]],
        [2, ["releasing", "deploying", "starting container"]],
        [1, ["generating build context", "building", "preparing"]],
        [0, ["pulling image", "pulling"]],
    ]
    let best = 0
    let at = -1
    for (const [idx, words] of marks) {
        for (const w of words) {
            const i = s.lastIndexOf(w)
            if (i > at || (i === at && i >= 0 && idx > best)) {
                at = i
                best = idx
            }
        }
    }
    return best
}

const APP_NAME = /^[a-z0-9][a-z0-9-]*$/
const ENV_KEY = /^[A-Za-z_][A-Za-z0-9_]*$/

const enc = encodeURIComponent

// Passos das tarefas (Job.step) que o servidor manda como código.
const JOB_STEPS = ["fetch-latest", "docker", "install", "verify", "domain", "ssh-key", "letsencrypt-plugin", "letsencrypt"]
const jobStep = (code) => !code ? tr("onboarding.job.preparing") : JOB_STEPS.includes(code) ? tr(`onboarding.job.step.${code}`) : code

// Erros guardados para mostrar depois: texto do servidor (string, já
// traduzido ou cru do dokku) ou { key, params } gerado aqui, traduzido na
// hora de mostrar para seguir a troca de idioma.

// Progresso salvo na aba: recarregar a página retoma o acompanhamento do deploy.
function loadSaved() {
    try { return JSON.parse(sessionStorage.getItem(SAVE_KEY)) || null } catch { return null }
}
function save(v) {
    try { sessionStorage.setItem(SAVE_KEY, JSON.stringify(v)) } catch {}
}
function clearSaved() {
    try { sessionStorage.removeItem(SAVE_KEY) } catch {}
}

// Igual a api(), mas devolve o corpo do erro (o POST /api/apps manda
// "created" quando a app foi criada e só a configuração falhou).
async function call(method, path, body) {
    const res = await fetch(path, {
        method,
        headers: { "X-Dokk": "1", ...(body ? { "Content-Type": "application/json" } : {}) },
        body: body ? JSON.stringify(body) : undefined,
    })
    const out = await res.json().catch(() => ({}))
    return { ok: res.ok, status: res.status, body: out }
}

function elapsed(since) {
    const s = Math.max(0, Math.floor((Date.now() - new Date(since)) / 1000))
    return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`
}

// Host do registry na referência da imagem ("ghcr.io/a/b:1" → "ghcr.io");
// sem host (ou "docker.io/…") é o Docker Hub e volta "".
function imageHost(image) {
    const first = image.trim().split("/")[0]
    if (!image.includes("/") || !/[.:]/.test(first) && first !== "localhost") return ""
    return first === "docker.io" || first === "index.docker.io" ? "" : first
}

// Nome sugerido a partir da imagem: último trecho do repo, sem a tag.
function nameFromImage(image) {
    const repo = image.trim().split("@")[0].split("/").pop() || ""
    return repo.split(":")[0].toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/^-+/, "").slice(0, 63)
}

// Texto colado de um .env vira pares; aceita "export", aspas e comentários.
// Devolve null se não parecer um .env (aí o paste segue normal).
function pastedEnv(text) {
    if (!text.includes("=")) return null
    const rows = []
    for (const raw of text.split(/\r?\n/)) {
        const line = raw.trim().replace(/^export\s+/, "")
        if (!line || line.startsWith("#")) continue
        const eq = line.indexOf("=")
        if (eq <= 0) return null
        let value = line.slice(eq + 1).trim()
        if (/^(["']).*\1$/.test(value)) value = value.slice(1, -1)
        rows.push({ key: line.slice(0, eq).trim(), value })
    }
    return rows.length ? rows : null
}

// Editor de variáveis: uma linha chave/valor por variável. Colar um .env em
// qualquer campo preenche tudo de uma vez.
function envEditor(rows) {
    const add = (row = { key: "", value: "" }) => rows.list = [...rows.list, row]
    const set = (i, field, v) => rows.list = rows.list.map((r, j) => j === i ? { ...r, [field]: v } : r)
    const remove = (i) => rows.list = rows.list.filter((_, j) => j !== i)
    const paste = (i) => (e) => {
        const found = pastedEnv(e.clipboardData?.getData("text") || "")
        if (!found || (found.length === 1 && !e.clipboardData.getData("text").includes("\n") && e.target.value)) return
        e.preventDefault()
        // Linhas vazias somem; chaves repetidas ficam com o valor colado.
        const kept = rows.list.filter((r, j) => j !== i && (r.key || r.value) && !found.some((f) => f.key === r.key))
        rows.list = [...kept, ...found]
    }
    return t.div({ className: "env-editor" },
        t.div({ className: "env-rows" }, () => rows.list.map((r, i) => t.div({ rid: `${i}-${rows.list.length}`, className: "env-row" },
            t.input({
                className: "input mono", placeholder: () => tr("onboarding.env.key.placeholder"), spellcheck: false, autocomplete: "off", "html-aria-label": () => tr("onboarding.env.key.aria"),
                value: r.key, oninput: (e) => set(i, "key", e.target.value), onpaste: paste(i),
            }),
            t.input({
                className: "input mono", placeholder: () => tr("onboarding.env.value.placeholder"), spellcheck: false, autocomplete: "off", "html-aria-label": () => tr("onboarding.env.value.aria"),
                value: r.value, oninput: (e) => set(i, "value", e.target.value), onpaste: paste(i),
            }),
            t.button({ type: "button", className: "icon-btn icon-btn-danger", title: () => tr("common.remove"), onclick: () => remove(i) }, t.span({ className: "icon icon-trash" })),
        ))),
        t.div({ className: "env-foot" },
            t.button({ type: "button", className: "btn", onclick: () => add() },
                t.span({ className: "icon icon-plus" }), t.span({ textContent: () => tr("onboarding.env.add") })),
            t.small({ className: "muted", textContent: () => tr("onboarding.env.hint") }),
        ),
    )
}

// Primeira tela do assistente: servidor com ondas saindo enquanto o status
// do Dokku chega.
function preparing() {
    const icon = t.span({ className: "prepare-icon", "html-aria-hidden": "true" })
    icon.innerHTML = `<span class="prepare-ring"></span><span class="prepare-ring"></span>
        <svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
            <rect x="9" y="8" width="30" height="13" rx="3"/>
            <rect x="9" y="27" width="30" height="13" rx="3"/>
            <path class="prepare-led" d="M15 14.5h.01M15 33.5h.01"/>
            <path d="M22 14.5h11M22 33.5h11"/>
        </svg>`
    return t.div({ className: "wizard-card prepare-card", "html-aria-busy": "true", role: "status" },
        icon,
        t.h2({ textContent: () => tr("onboarding.prepare.title") }),
        t.p({ className: "muted", textContent: () => tr("onboarding.prepare.text") }),
        t.span({ className: "prepare-dots", "html-aria-hidden": "true" }, t.span(), t.span(), t.span()),
    )
}

// Terminal com o log de uma tarefa: linhas mais recentes no topo; só fica
// grudado no topo se o usuário não tiver rolado para baixo.
function terminal(head) {
    const body = t.pre({ className: "terminal-body" })
    const el = t.section({ className: "terminal" }, t.header({ className: "terminal-head" }, ...head), body)
    el.body = body
    el.append = (lines) => {
        if (!lines.length) return
        const atTop = body.scrollTop < 24
        body.prepend(...lines.toReversed().map((l) => document.createTextNode(l + "\n")))
        while (body.childNodes.length > MAX_TERM_LINES) body.lastChild.remove()
        if (atTop) body.scrollTop = 0
    }
    el.set = (text) => {
        const atTop = body.scrollTop < 24
        body.textContent = text.split("\n").reverse().join("\n")
        if (atTop) body.scrollTop = 0
    }
    el.clear = () => body.replaceChildren()
    return el
}

export function onboarding() {
    // Só retoma um deploy recente (recarregar a página no meio dele); um
    // salvo antigo não prende o "Nova app" na app anterior.
    let saved = loadSaved()
    if (saved && !(Date.now() - (saved.at || 0) < RESUME_MS)) {
        clearSaved()
        saved = null
    }
    const data = store({
        loading: true,
        loadError: "",
        status: null,
        fresh: false, // o Dokku foi instalado agora (mostra o passo Configuração)
        seen: null, // GET /api/onboarding/seen: null até responder
        skipDokku: false,
        methodChosen: false, // o passo Imagem Docker só entra no stepper depois do método // o Dokku já foi detectado/instalado antes: some o passo Dokku
        step: "dokku",
        mode: "", // "job": acompanhando install/configure
        job: null,
        jobError: "",
        method: "image",
        app: saved?.app || "",
        image: saved?.image || "",
        created: false, // a app já existe: o retry usa /deploy
        deploy: { phase: "waiting", error: "", detail: null },
        deployLog: "", // texto do log do build (deriva a fase atual)
        error: "",
        busy: false,
        tick: 0,
    })

    // Timers da página; todos param no destroy.
    // Depois do destroy não cria mais nenhum (um fetch em voo pode tentar).
    const timers = new Set()
    let destroyed = false
    const every = (fn, ms) => { if (destroyed) return () => {}; const id = setInterval(fn, ms); timers.add(id); return () => { clearInterval(id); timers.delete(id) } }
    const later = (fn, ms) => { if (destroyed) return () => {}; const id = setTimeout(fn, ms); timers.add(id); return () => { clearTimeout(id); timers.delete(id) } }
    every(() => data.tick++, 1000)

    const go = (step) => {
        data.error = ""
        data.mode = ""
        data.step = step
    }

    // Lembra no servidor que o Dokku já foi visto, para o próximo "Nova app"
    // começar direto no método de deploy.
    const markSeen = () => api("POST", "/api/onboarding/dokku-seen", {}).catch(() => {})

    async function loadStatus() {
        const res = await fetch("/api/onboarding", { cache: "no-store" })
        const body = await res.json().catch(() => ({}))
        if (!res.ok) throw new Error(body.error || res.statusText)
        data.status = body
        return body
    }

    // ---------- tarefas (install / configure) ----------

    let stopJobPoll = null
    let since = 0
    let term = null

    function watchJob(job) {
        data.job = job
        data.jobError = ""
        since = 0
        stopJobPoll?.()
        stopJobPoll = null
        // Entrando na tela de log, o watch do passo liga o polling depois de
        // montar o terminal; já nela (Tentar de novo), religa aqui.
        if (data.mode === "job") {
            stopJobPoll = every(pollJob, 1000)
            pollJob()
        } else data.mode = "job"
    }

    let polling = false
    async function pollJob() {
        if (polling) return
        polling = true
        try {
            const res = await fetch(`/api/onboarding/job?since=${since}`, { cache: "no-store" })
            if (!res.ok) return
            const body = await res.json()
            if (!body.job) return
            // Outra tarefa começou (ex.: "Tentar de novo"): recomeça o log.
            if (data.job?.id !== body.job.id) {
                since = 0
                term?.clear()
                data.job = body.job
                return
            }
            data.job = body.job
            term?.append(body.lines || [])
            since = body.next
            if (!body.job.running) {
                stopJobPoll?.()
                stopJobPoll = null
                if (body.job.error) {
                    data.jobError = body.job.error
                    return
                }
                await loadStatus().catch(() => {})
                if (body.job.kind === "install") {
                    markSeen()
                    data.fresh = true
                    go("configure")
                } else go("method")
            }
        } catch {
            // Rede instável (ou o dokk reiniciando): tenta no próximo tick.
        } finally {
            polling = false
        }
    }

    async function startJob(kind, body = {}) {
        data.busy = true
        data.error = ""
        try {
            const job = await api("POST", `/api/onboarding/${kind}`, body)
            term?.clear()
            watchJob(job)
        } catch (err) {
            data.error = err.message
            // A instalação falhou depois do pacote do dokku entrar: o retry
            // volta 409 "já está instalado". Segue para a configuração.
            if (kind === "install") {
                const s = await loadStatus().catch(() => null)
                if (s?.dokku?.installed) {
                    data.fresh = true
                    go("configure")
                }
            }
        } finally {
            data.busy = false
        }
    }

    function jobView() {
        term = terminal([
            t.span({ className: () => `dot ${data.job?.running ? "dot-running dot-pulse" : data.jobError ? "dot-build-failed" : "dot-exited"}` }),
            t.span({ className: "muted", textContent: () => jobStep(data.job?.step) }),
            t.div({ className: "spacer" }),
            t.span({ className: "muted mono", textContent: () => (data.tick, data.job ? elapsed(data.job["started-at"]) : "") }),
        ])
        // Reabrindo a tela: busca o log desde o começo.
        since = 0
        const kind = data.job?.kind
        const failed = () => kind === "install" ? tr("onboarding.job.install.failed") : tr("onboarding.job.configure.failed")
        return t.div({ className: "wizard-card" },
            t.h2({ textContent: () => kind === "install" ? tr("onboarding.job.install.title") : tr("onboarding.job.configure.title") }),
            t.p({ className: "muted", textContent: () => kind === "install"
                ? tr("onboarding.job.install.text")
                : tr("onboarding.job.configure.text") }),
            term,
            t.p({ className: "form-error", role: "alert", hidden: () => !data.jobError, textContent: () => `${failed()}: ${data.jobError}` }),
            t.p({ className: "form-error", role: "alert", hidden: () => !data.error, textContent: () => errText(data.error) }),
            t.div({ className: "wizard-actions", hidden: () => !data.jobError },
                t.button({ type: "button", className: "btn", textContent: () => tr("onboarding.job.fullLog"), onclick: () => term.body.scrollTop = term.body.scrollHeight }),
                kind === "configure"
                    ? t.button({ type: "button", className: "btn", textContent: () => tr("onboarding.skip"), onclick: () => go("method") })
                    : null,
                t.button({
                    type: "button", className: "btn btn-primary", disabled: () => data.busy, textContent: () => tr("common.retry"),
                    onclick: () => kind === "install" ? startJob("install") : (data.mode = "", data.jobError = ""),
                }),
            ),
        )
    }

    // ---------- passo: Dokku ----------

    function dokkuStep() {
        const s = data.status
        if (s.dokku.installed) markSeen()
        if (s.dokku.installed && !data.fresh) {
            // Detectado: segue sozinho em 1,5s, a menos que o usuário mexa.
            const cancel = later(() => data.step === "dokku" && go("method"), 1500)
            const card = t.div({ className: "wizard-card detect-card detect-ok", onpointerdown: cancel, onkeydown: cancel },
                t.div({ className: "detect-head" },
                    t.span({ className: "detect-icon icon icon-check" }),
                    t.h2({ textContent: () => tr("onboarding.dokku.detected.title") }),
                ),
                t.p({ textContent: () => tr("onboarding.dokku.detected.text", { version: s.dokku.version }) }),
                t.div({ className: "wizard-actions" },
                    t.button({ type: "button", className: "btn btn-primary", textContent: () => tr("common.continue"), onclick: () => { cancel(); go("method") } }),
                ),
            )
            return card
        }
        if (s.dokku.installed && data.fresh) {
            return t.div({ className: "wizard-card detect-card detect-ok" },
                t.h2({ textContent: () => tr("onboarding.dokku.installed.title") }),
                t.p({ textContent: () => tr("onboarding.dokku.installed.text", { version: s.dokku.version }) }),
                t.div({ className: "wizard-actions" },
                    t.button({ type: "button", className: "btn btn-primary", textContent: () => tr("common.continue"), onclick: () => go("configure") }),
                ),
            )
        }
        const host = s.host || {}
        if (!host["can-install"]) {
            // O código do motivo é traduzido aqui; sem ele, vale o texto do servidor.
            const code = host["reason-code"]
            const reason = () => ["os", "arch", "root"].includes(code)
                ? tr(`onboarding.dokku.reason.${code}`, { name: host["reason-arg"] || "" })
                : host.reason || tr("onboarding.dokku.cannotInstall")
            return t.div({ className: "wizard-card" },
                t.h2({ textContent: () => tr("onboarding.dokku.install.title") }),
                t.p({ className: "form-error", textContent: reason }),
                t.p({ className: "muted", textContent: () => tr("onboarding.dokku.manual") }),
                t.pre({ className: "code-block mono", textContent: `wget -NP . https://dokku.com/install/${INSTALL_TAG}/bootstrap.sh\nsudo DOKKU_TAG=${INSTALL_TAG} bash bootstrap.sh` }),
                t.div({ className: "wizard-actions" },
                    t.button({ type: "button", className: "btn btn-primary", textContent: () => tr("onboarding.dokku.recheck"), onclick: reload }),
                ),
            )
        }
        return t.div({ className: "wizard-card" },
            t.h2({ textContent: () => tr("onboarding.dokku.install.title") }),
            t.p({ textContent: () => tr(host.method === "docker" ? "onboarding.dokku.install.textDocker" : "onboarding.dokku.install.text") }),
            t.p({ className: "muted mono", textContent: `${host.os} ${host["os-version"]} · ${host.arch}` }),
            t.p({ className: "form-error", role: "alert", hidden: () => !data.error, textContent: () => errText(data.error) }),
            t.div({ className: "wizard-actions" },
                t.button({ type: "button", className: "btn btn-primary", disabled: () => data.busy, onclick: () => startJob("install") },
                    () => data.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("onboarding.dokku.install.busy") })] : [t.span({ className: "icon icon-install" }), t.span({ textContent: () => tr("onboarding.dokku.install.submit") })]),
            ),
        )
    }

    // ---------- passo: Configuração ----------

    function configureStep() {
        const s = data.status
        const ui = store({ domain: s["suggested-domain"] || s["global-domains"]?.[0] || "", email: "", key: "" })
        const submit = (e) => {
            e.preventDefault()
            const body = { domain: ui.domain.trim().toLowerCase(), "ssh-key": ui.key.trim(), "letsencrypt-email": ui.email.trim() }
            if (!body.domain && !body["ssh-key"] && !body["letsencrypt-email"]) return go("method")
            startJob("configure", body)
        }
        return t.form({ className: "wizard-card", onsubmit: submit },
            t.h2({ textContent: () => tr("onboarding.configure.title") }),
            t.label({ className: "field" },
                t.span({ textContent: () => tr("onboarding.configure.domain.label") }),
                t.input({ className: "input mono", placeholder: () => tr("onboarding.configure.domain.placeholder"), spellcheck: false, value: ui.domain, oninput: (e) => ui.domain = e.target.value }),
                t.small({ className: "muted", textContent: () => tr("onboarding.configure.domain.hint") }),
            ),
            t.label({ className: "field" },
                t.span({ textContent: () => tr("onboarding.configure.email.label") }),
                t.input({ className: "input", type: "email", placeholder: () => tr("onboarding.configure.email.placeholder"), value: ui.email, oninput: (e) => ui.email = e.target.value }),
                t.small({ className: "muted", textContent: () => tr("onboarding.configure.email.hint") }),
            ),
            t.details({ className: "wizard-details" },
                t.summary({ textContent: () => tr("onboarding.configure.ssh.title") }),
                t.label({ className: "field" },
                    t.span({ textContent: () => tr("onboarding.configure.ssh.label") }),
                    t.textarea({ className: "input mono", rows: 3, placeholder: () => tr("onboarding.configure.ssh.placeholder"), spellcheck: false, oninput: (e) => ui.key = e.target.value }),
                ),
            ),
            t.p({ className: "form-error", role: "alert", hidden: () => !data.error, textContent: () => errText(data.error) }),
            t.div({ className: "wizard-actions" },
                t.button({ type: "button", className: "btn", textContent: () => tr("onboarding.skip"), onclick: () => go("method") }),
                t.button({ type: "submit", className: "btn btn-primary", disabled: () => data.busy },
                    () => data.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("common.saving") })] : [t.span({ textContent: () => tr("onboarding.configure.submit") })]),
            ),
        )
    }

    // ---------- passo: Método ----------

    function methodStep() {
        return t.div({ className: "wizard-card" },
            t.h2({ textContent: () => tr("onboarding.method.title") }),
            t.p({ className: "muted", textContent: () => tr("onboarding.method.text") }),
            t.div({ className: "method-grid", role: "radiogroup", "html-aria-label": () => tr("onboarding.method.title") },
                ...METHODS.map((m) => t.button({
                    type: "button",
                    className: "method-card",
                    role: "radio",
                    "html-aria-checked": () => String(data.method === m.key),
                    "html-aria-disabled": m.soon ? "true" : "false",
                    onclick: () => { if (!m.soon) data.method = m.key },
                },
                    methodIcon(m.icon),
                    t.strong({ textContent: () => m.title }),
                    t.span({ className: "muted", textContent: () => m.text }),
                    m.soon ? t.span({ className: "soon", textContent: () => tr("onboarding.method.soon") }) : null,
                )),
            ),
            t.div({ className: "wizard-actions" },
                t.button({ type: "button", className: "btn btn-primary", textContent: () => tr("common.continue"), onclick: () => { data.methodChosen = true; go("image") } }),
            ),
        )
    }

    // ---------- passo: Registry ----------

    // Imagem + de onde ela vem: o provedor escolhido mostra os campos de
    // login logo abaixo. "Imagem pública" pula o login.
    function imageStep() {
        const ui = store({ image: data.image, provider: "public", touched: false, server: "", username: "", password: "", list: [], edit: false, error: "" })
        const provider = () => PROVIDERS.find((p) => p.key === ui.provider)
        const server = () => provider()?.server || ui.server.trim()
        // O provedor escolhido já tem login no servidor: só confirma.
        const connected = () => ui.provider !== "public" && ui.list.find((r) => r.server === server())
        const needsLogin = () => ui.provider !== "public" && (!connected() || ui.edit)
        const typed = needsLogin

        // Enquanto a pessoa não mexe no select, o host da imagem sugere o
        // provedor (ghcr.io/… → GitHub); sem host é Docker Hub/pública.
        const suggest = () => {
            if (ui.touched) return
            const host = imageHost(ui.image)
            if (!host) {
                ui.provider = ui.list.some((r) => r.server === "docker.io") ? "hub" : "public"
                return
            }
            const known = PROVIDERS.find((p) => p.server && p.server === host)
            ui.provider = known ? known.key : "other"
            if (!known) ui.server = host
        }

        fetch("/api/registries", { cache: "no-store" })
            .then((r) => r.ok ? r.json() : {})
            .then((body) => { ui.list = body.registries || []; if (ui.image) suggest() })
            .catch(() => {})

        const submit = async (e) => {
            e.preventDefault()
            if (data.busy) return
            data.image = ui.image.trim()
            if (!needsLogin()) return go("app")
            data.busy = true
            ui.error = ""
            try {
                await api("POST", "/api/registries", { server: server(), username: ui.username.trim(), password: ui.password })
                ui.password = ""
                go("app")
            } catch (err) {
                ui.error = err.message
            } finally {
                data.busy = false
            }
        }

        return t.form({ className: "wizard-card", onsubmit: submit },
            t.h2({ textContent: () => tr("onboarding.image.title") }),
            t.p({ className: "muted", textContent: () => tr("onboarding.image.text") }),
            t.label({ className: "field" },
                t.span({ textContent: () => tr("onboarding.image.image.label") }),
                t.input({
                    className: "input mono", placeholder: () => tr("onboarding.image.image.placeholder"), required: true, spellcheck: false, autocomplete: "off",
                    value: ui.image,
                    oninput: (e) => {
                        ui.image = e.target.value
                        suggest()
                    },
                }),
            ),
            t.label({ className: "field" },
                t.span({ textContent: () => tr("onboarding.image.provider.label") }),
                t.select({ className: "input", onchange: (e) => { ui.provider = e.target.value; ui.touched = true; ui.edit = false; ui.error = "" } },
                    t.option({ value: "public", textContent: () => tr("onboarding.image.provider.public"), selected: () => ui.provider === "public" }),
                    ...PROVIDERS.map((p) => t.option({ value: p.key, textContent: () => p.label, selected: () => ui.provider === p.key }))),
            ),
            t.p({ className: "registry-connected", hidden: () => !connected() || ui.edit },
                t.span({ className: "icon icon-check" }),
                t.span({ textContent: () => {
                    const r = connected()
                    if (!r) return ""
                    return r.username
                        ? tr("onboarding.image.connected.asUser", { server: r.server, user: r.username })
                        : tr("onboarding.image.connected.text", { server: r.server })
                } }),
                t.button({ type: "button", className: "link-btn", textContent: () => tr("onboarding.image.changeCredentials"), onclick: () => ui.edit = true }),
            ),
            t.label({ className: "field", hidden: () => !needsLogin() || ui.provider !== "other" },
                t.span({ textContent: () => tr("onboarding.image.server.label") }),
                t.input({
                    className: "input mono", placeholder: () => tr("onboarding.image.server.placeholder"), autocomplete: "off", spellcheck: false,
                    required: () => needsLogin() && ui.provider === "other",
                    value: () => ui.server, oninput: (e) => { ui.server = e.target.value; ui.error = "" },
                }),
            ),
            t.label({ className: "field", hidden: () => !typed() },
                t.span({ textContent: () => tr("onboarding.image.user.label") }),
                t.input({
                    className: "input", autocomplete: "off", spellcheck: false, required: typed,
                    placeholder: () => provider()?.user || tr("onboarding.image.user.placeholder"),
                    value: () => ui.username, oninput: (e) => { ui.username = e.target.value; ui.error = "" },
                }),
            ),
            t.label({ className: "field", hidden: () => !typed() },
                t.span({ textContent: () => tr("onboarding.image.password.label") }),
                t.input({
                    className: "input", type: "password", autocomplete: "new-password", required: typed,
                    placeholder: () => provider()?.pass || "••••••••",
                    value: () => ui.password, oninput: (e) => { ui.password = e.target.value; ui.error = "" },
                }),
                t.small({ className: "muted", textContent: () => tr("onboarding.image.password.hint") }),
            ),
            t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
            t.div({ className: "wizard-actions" },
                t.button({ type: "submit", className: "btn btn-primary", disabled: () => data.busy },
                    () => data.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("onboarding.image.busy") })] : [t.span({ textContent: () => typed() ? tr("onboarding.image.submit") : tr("common.continue") })]),
            ),
        )
    }

    // ---------- passo: App ----------

    function appStep() {
        const s = data.status
        const domain = s["global-domains"]?.[0] || ""
        // Sem e-mail global o letsencrypt:enable falha: só oferece com ele.
        const canSSL = !!domain
        const envRows = store({ list: [{ key: "", value: "" }] })
        const ui = store({ image: data.image, name: data.app || nameFromImage(data.image), nameEdited: !!data.app, port: "", ssl: true, nameError: null })

        const submit = async (e) => {
            e.preventDefault()
            if (data.busy) return
            data.error = ""
            const name = ui.name.trim()
            const image = ui.image.trim()
            if (!APP_NAME.test(name)) { ui.nameError = { key: "onboarding.app.name.error.invalid" }; return }
            let env
            env = {}
            for (const r of envRows.list) {
                const key = r.key.trim()
                if (!key && !r.value) continue
                if (!ENV_KEY.test(key)) { data.error = key ? { key: "onboarding.app.env.error.invalid", params: { name: key } } : { key: "onboarding.app.env.error.empty" }; return }
                env[key] = r.value
            }
            data.busy = true
            try {
                // App já criada (retry com outra imagem): só refaz o deploy.
                if (data.created) {
                    const res = await call("POST", `/api/apps/${enc(name)}/deploy`, { image })
                    if (!res.ok) throw new Error(res.body.error || tr("onboarding.deploy.error.failed"))
                    startDeploy(name, image, "")
                    return
                }
                const res = await call("POST", "/api/apps", { name, image, port: Number(ui.port) || 0, env, ssl: canSSL && ui.ssl })
                if (res.ok) {
                    data.created = true
                    startDeploy(name, image, "")
                    return
                }
                if (res.body.created) {
                    data.created = true
                    startDeploy(name, image, res.body.error || { key: "onboarding.app.error.configure" })
                    return
                }
                data.error = res.body.error || { key: "onboarding.app.error.create" }
            } catch (err) {
                data.error = err.message
            } finally {
                data.busy = false
            }
        }

        return t.form({ className: "wizard-card", onsubmit: submit },
            t.h2({ textContent: () => data.created ? tr("onboarding.app.changeImage") : tr("onboarding.app.title") }),
            // A imagem vem do passo anterior; aqui só para trocar no retry.
            !data.created ? null : t.label({ className: "field" },
                t.span({ textContent: () => tr("onboarding.image.image.label") }),
                t.input({
                    className: "input mono", placeholder: () => tr("onboarding.image.image.placeholder"), required: true, spellcheck: false, autocomplete: "off",
                    value: ui.image,
                    oninput: (e) => {
                        ui.image = e.target.value
                        if (!ui.nameEdited && !data.created) ui.name = nameFromImage(ui.image)
                    },
                }),
            ),
            t.label({ className: "field" },
                t.span({ textContent: () => tr("onboarding.app.name.label") }),
                t.input({
                    className: "input mono", placeholder: () => tr("onboarding.app.name.placeholder"), required: true, spellcheck: false, autocomplete: "off", maxLength: 63,
                    disabled: data.created,
                    value: () => ui.name,
                    oninput: (e) => { ui.name = e.target.value.toLowerCase(); ui.nameEdited = true; ui.nameError = null },
                }),
                t.small({
                    className: () => ui.nameError ? "form-error" : "muted",
                    hidden: () => !ui.nameError && !(domain && ui.name),
                    textContent: () => ui.nameError ? errText(ui.nameError) : tr("onboarding.app.address", { host: `${ui.name}.${domain}` }),
                }),
            ),
            data.created ? null : t.label({ className: "field" },
                t.span({ textContent: () => tr("onboarding.app.port.label") }),
                t.input({ className: "input mono", type: "number", min: 1, max: 65535, placeholder: "5000", value: ui.port, oninput: (e) => ui.port = e.target.value }),
            ),
            data.created ? null : t.div({ className: "field" },
                t.span({ textContent: () => tr("onboarding.app.env.label") }),
                envEditor(envRows),
            ),
            canSSL && !data.created ? t.label({ className: "check" },
                t.input({ type: "checkbox", checked: true, onchange: (e) => ui.ssl = e.target.checked }),
                t.span({ textContent: () => tr("onboarding.app.ssl.label") }),
            ) : null,
            t.p({ className: "form-error", role: "alert", hidden: () => !data.error, textContent: () => errText(data.error) }),
            t.div({ className: "wizard-actions" },
                t.button({ type: "submit", className: "btn btn-primary", disabled: () => data.busy },
                    () => data.busy
                        ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("onboarding.app.busy") })]
                        : [t.span({ textContent: () => data.created ? tr("onboarding.app.deploy.submit") : tr("onboarding.app.submit") })]),
            ),
        )
    }

    // ---------- passo: Deploy ----------

    let stopDeployPoll = null
    let stopLogPoll = null
    let loadLog = null // busca o log do build atual (a última leitura no erro)
    let deployStarted = 0
    let deployFrom = 0 // início mostrado no cronômetro (o "started-at" da ação, quando vem)
    let deployGen = 0 // muda a cada deploy: respostas de um deploy anterior são ignoradas
    let unhealthySince = 0
    let doneSent = false
    let reachedPhase = 0 // fase mais adiantada já vista (não volta atrás)

    function startDeploy(name, image, error) {
        data.app = name
        data.image = image
        save({ step: "deploy", app: name, image, at: Date.now() })
        // started: houve deploy de fato (sem ele, o erro é da criação e a lista de fases some).
        data.deploy = { phase: error ? "error" : image ? "waiting" : "created", error, detail: null, started: !error && !!image }
        data.deployLog = ""
        reachedPhase = 0
        deployGen++
        if (deployLog) {
            deployLog.clear()
            deployLog.hidden = true
        }
        go("deploy")
        stopDeployPoll?.()
        stopDeployPoll = null
        stopLogPoll?.()
        stopLogPoll = null
        loadLog = null
        if (error) return
        deployStarted = deployFrom = Date.now()
        unhealthySince = 0
        stopDeployPoll = every(pollDeploy, 2000)
        pollDeploy()
    }

    function finish(phase) {
        stopDeployPoll?.()
        stopDeployPoll = null
        stopLogPoll?.()
        stopLogPoll = null
        data.deploy = { ...data.deploy, phase }
        if (phase === "error") {
            // Uma última leitura traz as linhas finais (com o erro) para o log.
            loadLog?.()
            return
        }
        clearSaved()
        ensureServerLang()
        if (!doneSent) {
            doneSent = true
            api("POST", "/api/onboarding/done", {}).catch(() => {})
        }
    }

    let deployLog = null
    function showLog(text) {
        data.deployLog = text
        if (deployLog) {
            deployLog.hidden = false
            deployLog.set(text)
        }
    }

    async function pollDeploy() {
        const gen = deployGen
        try {
            const res = await fetch(`/api/apps/${enc(data.app)}`, { cache: "no-store" })
            // Deploy trocado ou já encerrado enquanto a resposta vinha.
            if (gen !== deployGen || !stopDeployPoll) return
            // O monitor ainda não listou a app nova: espera um pouco.
            if (res.status === 404) {
                if (Date.now() - deployStarted > 30_000) {
                    data.deploy = { ...data.deploy, error: { key: "onboarding.deploy.error.missing" } }
                    finish("error")
                }
                return
            }
            if (!res.ok) return
            // GET /api/apps/{name} devolve { app, detail, action }.
            const { app, detail, action } = await res.json()
            if (gen !== deployGen || !stopDeployPoll) return
            const running = action?.running
            if (action?.["started-at"]) deployFrom = new Date(action["started-at"]).getTime() || deployFrom
            data.deploy = { ...data.deploy, detail: app, action, phase: running ? "running" : data.deploy.phase }
            if (!data.image) return finish("created")
            if (running) {
                if (detail?.builds?.[0]?.status === "running" && !stopLogPoll) {
                    const id = detail.builds[0].id
                    loadLog = () => fetch(`/api/apps/${enc(data.app)}/builds/${id}/log`)
                        .then((r) => r.ok ? r.text() : "")
                        .then((text) => {
                            if (!text || gen !== deployGen) return
                            showLog(text.split("\n").slice(-200).join("\n"))
                        })
                        .catch(() => {})
                    stopLogPoll = every(loadLog, 3000)
                    loadLog()
                }
                return
            }
            if (action?.error) {
                data.deploy = { ...data.deploy, error: action.error }
                return finish("error")
            }
            // Ação terminou (ou o servidor já não a guarda): espera a app subir.
            if (!action && !app.deployed) return
            const status = app.checks?.status
            if (status === "healthy" || status === "starting") return finish("ok")
            if (status === "unhealthy" || status === "degraded") {
                unhealthySince ||= Date.now()
                if (Date.now() - unhealthySince > 90_000) {
                    data.deploy = { ...data.deploy, error: { key: "onboarding.deploy.error.unhealthy" } }
                    finish("error")
                }
            }
        } catch {
            // Tenta no próximo ciclo.
        }
    }

    async function retryDeploy() {
        data.busy = true
        data.error = ""
        try {
            await api("POST", `/api/apps/${enc(data.app)}/deploy`, { image: data.image })
            startDeploy(data.app, data.image, "")
        } catch (err) {
            data.error = err.message
        } finally {
            data.busy = false
        }
    }

    // Índice da fase atual: log do build + estado da app/checks.
    function currentPhase() {
        const d = data.deploy
        let idx = phaseFromLog(data.deployLog)
        if (!d.action?.running && d.detail?.deployed) idx = Math.max(idx, 3)
        if (d.phase === "ok") idx = DEPLOY_PHASES.length
        reachedPhase = Math.max(reachedPhase, idx)
        // App no ar mas sem responder: quem falhou foram os checks.
        if (d.phase === "error" && d.error?.key === "onboarding.deploy.error.unhealthy") return Math.min(reachedPhase, 3)
        return reachedPhase
    }

    function phaseState(i) {
        const cur = currentPhase()
        if (i < cur) return "done"
        if (i > cur) return "todo"
        return data.deploy.phase === "error" ? "failed" : "current"
    }

    // Lista vertical das fases; cada item reage sozinho à fase atual, então
    // as transições de CSS não são interrompidas por re-render.
    function phaseList() {
        return t.ol({ className: "deploy-phases", hidden: () => !data.deploy.started }, ...DEPLOY_PHASES.map((p, i) => {
            const icon = t.span({ className: "deploy-phase-icon", "html-aria-hidden": "true" })
            let last = ""
            const draw = () => {
                const st = phaseState(i)
                if (st !== last) {
                    last = st
                    const paths = st === "done" ? PHASE_CHECK : st === "failed" ? PHASE_FAIL : p.icon
                    icon.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${paths}</svg>`
                }
                return `deploy-phase is-${st}`
            }
            return t.li({ className: draw, "html-aria-current": () => phaseState(i) === "current" ? "step" : "false" },
                icon,
                t.span({ className: "deploy-phase-label", textContent: () => p.label }),
                t.span({ className: "deploy-phase-state muted", textContent: () => {
                    const st = phaseState(i)
                    return st === "current" ? tr("onboarding.deploy.phase.now") : st === "failed" ? tr("onboarding.deploy.phase.failed") : ""
                } }),
            )
        }))
    }

    function deployStep() {
        const href = `/apps/${enc(data.app)}`
        const isError = () => data.deploy.phase === "error"
        deployLog = terminal([
            t.span({ className: () => isError() ? "dot dot-build-failed" : "dot dot-running dot-pulse" }),
            t.span({ className: "muted", textContent: () => tr("onboarding.deploy.buildLog") }),
        ])
        deployLog.classList.add("deploy-log")
        deployLog.body.classList.add("deploy-log-body")
        deployLog.hidden = !data.deployLog
        if (data.deployLog) deployLog.set(data.deployLog)
        const links = () => t.div({ className: "wizard-actions" },
            t.a({ className: "btn", href: "/", textContent: () => tr("onboarding.deploy.goApps") }),
            t.a({ className: "btn btn-primary", href, textContent: () => tr("onboarding.deploy.openApp") }),
        )
        // Andamento e erro são o mesmo elemento, criado uma vez só: o poll
        // reescreve data.deploy a cada 2s e o card devolve sempre esta
        // instância (mesmo rid), então fases e log não são recriados nem
        // movidos e as transições seguem suaves.
        const progress = t.div({ className: "deploy-progress", rid: "deploy-progress" },
            t.div({ className: "deploy-head" },
                t.h2({ textContent: () => tr("onboarding.steps.deploy") }),
                t.span({ className: "deploy-timer mono", hidden: isError, title: () => tr("onboarding.deploy.elapsed"), textContent: () => (data.tick, elapsed(deployFrom || Date.now())) }),
            ),
            t.p({ className: "deploy-status muted", hidden: isError },
                t.span({ textContent: () => data.deploy.phase === "waiting" && !data.deploy.detail ? tr("onboarding.deploy.registering") : tr("onboarding.deploy.running", { image: truncateMiddle(data.image, 60) }) }),
            ),
            phaseList(),
            t.p({ className: "form-error", role: "alert", hidden: () => !isError(), textContent: () => isError() ? tr("onboarding.deploy.failed", { message: errText(data.deploy.error) }) : "" }),
            t.p({ className: "form-error", role: "alert", hidden: () => !isError() || !data.error, textContent: () => errText(data.error) }),
            t.div({ className: "wizard-actions", hidden: () => !isError() },
                t.a({ className: "btn", href, textContent: () => tr("onboarding.deploy.open"), onclick: clearSaved }),
                t.button({ type: "button", className: "btn", textContent: () => tr("onboarding.app.changeImage"), onclick: () => { data.created = true; go("app") } }),
                t.button({ type: "button", className: "btn btn-primary", disabled: () => data.busy, textContent: () => tr("common.retry"), onclick: retryDeploy }),
            ),
            deployLog,
        )
        return t.div({ className: "wizard-card" }, () => {
            const d = data.deploy
            if (d.phase === "ok") {
                return t.div({ className: "success-card" },
                    logo(56, { animated: true }),
                    t.h2({ textContent: () => tr("onboarding.deploy.ok.title") }),
                    d.detail ? t.div({ className: "card-domains" }, ...domains(d.detail)) : null,
                    links(),
                )
            }
            if (d.phase === "created") {
                return t.div({ className: "success-card" },
                    logo(56),
                    t.h2({ textContent: () => tr("onboarding.deploy.created.title") }),
                    t.p({ className: "muted mono", textContent: data.app }),
                    links(),
                )
            }
            return progress
        })
    }

    // ---------- moldura ----------

    const visibleSteps = () => STEPS.filter((s) =>
        (s.key !== "configure" || data.fresh) && (s.key !== "dokku" || !data.skipDokku) &&
        (s.key !== "image" || data.methodChosen || ["app", "deploy"].includes(data.step)))

    let stepperLive = false
    const stepper = t.ol({ className: "stepper", hidden: () => data.seen === null }, () => {
        // rid fixo por passo: só o passo que surge agora é criado (e anima);
        // os demais só trocam classe e número.
        const pos = (key) => {
            const steps = visibleSteps()
            return { i: steps.findIndex((s) => s.key === key), cur: steps.findIndex((s) => s.key === data.step) }
        }
        return visibleSteps().map((s) => {
            const enter = stepperLive ? " step-enter" : ""
            return t.li({
                rid: s.key,
                className: () => {
                    const { i, cur } = pos(s.key)
                    return `step${enter}${i < cur ? " step-done" : i === cur ? " step-current" : ""}`
                },
                "html-aria-current": () => pos(s.key).i === pos(s.key).cur ? "step" : false,
            },
                t.span({ className: "step-num", textContent: () => String(pos(s.key).i + 1) }),
                t.span({ className: "step-label", textContent: () => s.label }),
            )
        })
    })

    const skip = async () => {
        ensureServerLang()
        await api("POST", "/api/onboarding/done", {}).catch(() => {})
        clearSaved()
        navigate("/")
    }

    const body = t.div({ className: "wizard-body" })
    const render = () => {
        // Até o /seen responder (alguns ms) não desenha nada, para não piscar
        // um passo errado. Quem já viu o Dokku vai direto ao método, que não
        // depende do status; quem não viu espera o status com a animação.
        if (data.seen === null) return t.div({ className: "wizard-placeholder" })
        if (data.loading && !(data.seen && data.step === "method")) {
            return data.seen ? t.div({ className: "wizard-card skeleton", "html-aria-busy": "true" }) : preparing()
        }
        if (data.loadError) {
            return t.div({ className: "wizard-card" },
                t.p({ className: "form-error", textContent: () => tr("onboarding.loadError", { message: data.loadError }) }),
                t.div({ className: "wizard-actions" }, t.button({ type: "button", className: "btn btn-primary", textContent: () => tr("common.retry"), onclick: reload })),
            )
        }
        if (data.mode === "job") return jobView()
        return { dokku: dokkuStep, configure: configureStep, method: methodStep, image: imageStep, app: appStep, deploy: deployStep }[data.step]()
    }
    // Só troca o card quando muda de passo (os campos não são recriados a
    // cada tecla); o que muda dentro do passo é reativo no próprio card.
    const sub = watch(() => [data.seen, data.step, data.mode, data.loading, data.loadError], () => {
        body.replaceChildren(render())
        if (data.mode === "job" && !stopJobPoll) {
            stopJobPoll = every(pollJob, 1000)
            pollJob()
        }
    })

    // minMs segura a animação de "Preparando o ambiente" na primeira visita,
    // para a detecção não piscar na tela.
    async function reload(minMs = 0) {
        data.loading = true
        data.loadError = ""
        const started = Date.now()
        try {
            const s = await loadStatus()
            const wait = s["dokku-seen"] || data.seen ? 0 : minMs - (Date.now() - started)
            if (wait > 0) await new Promise((resolve) => later(resolve, wait))
        } catch (err) {
            data.loadError = err.message
        } finally {
            data.loading = false
        }
    }

    // Primeira carga: o /seen escolhe o primeiro passo na hora; o status
    // completo chega depois e pode retomar tarefa ou deploy salvo.
    fetch("/api/onboarding/seen", { cache: "no-store" })
        .then((r) => r.ok ? r.json() : {})
        .catch(() => ({}))
        .then((body) => {
            if (body["dokku-seen"] && data.step === "dokku") {
                data.skipDokku = true
                data.step = "method"
            }
            data.seen = !!body["dokku-seen"]
            // Os passos que já existem ao abrir não animam; só os que surgem
            // depois (ex.: Imagem Docker ao escolher o método).
            requestAnimationFrame(() => requestAnimationFrame(() => stepperLive = true))
        })
    reload(PREPARE_MS).then(() => {
        const s = data.status
        if (!s) return
        // Marcado como visto, mas o Dokku sumiu do servidor: volta ao passo.
        if (!s.dokku.installed) {
            data.skipDokku = false
            data.step = "dokku"
        }
        if (s.job?.running) {
            data.fresh = s.job.kind === "install" || !s.dokku.installed || data.fresh
            data.job = s.job
            data.step = s.job.kind === "install" ? "dokku" : "configure"
            data.mode = "job"
            return
        }
        if (s.dokku.installed && saved?.step === "deploy" && saved.app) {
            data.created = true
            startDeploy(saved.app, saved.image, "")
            return
        }
        // A instalação terminou com a aba fechada: segue para a configuração.
        if (s.job?.kind === "install" && !s.job.error && s.dokku.installed && !s.done) {
            data.fresh = true
            data.step = "configure"
            return
        }
        // O Dokku já foi visto antes: nem mostra o passo de detecção.
        if (s["dokku-seen"]) {
            data.skipDokku = true
            data.step = "method"
        }
    })

    const page = t.main({ className: "page page-onboarding" },
        t.header({ className: "page-head" },
            t.div({},
                // Primeiro onboarding configura o servidor; depois disso (o
                // "Nova app" do painel) é só criar uma app.
                t.h1({ textContent: () => data.skipDokku ? tr("onboarding.newApp.title") : tr("onboarding.title") }),
                t.p({ className: "muted page-sub", textContent: () => data.skipDokku ? tr("onboarding.newApp.subtitle") : tr("onboarding.subtitle") }),
            ),
            // Idioma escolhido aqui (sem recriar o passo: os textos são bindings);
            // no "Criar nova app" do painel ele já foi escolhido, então some.
            t.div({ className: "page-head-actions" },
                t.div({ hidden: () => data.skipDokku }, langSelect()),
                t.button({
                    type: "button", className: "ghost", textContent: () => data.skipDokku ? tr("common.cancel") : tr("onboarding.skipSetup"), onclick: skip,
                    hidden: () => !data.status?.dokku?.installed,
                }),
            ),
        ),
        stepper,
        body,
    )
    // No "Criar nova app" do painel a aba também diz isso, não "Configuração".
    const titleSub = watch(() => setPageTitle(data.skipDokku ? tr("common.page.newApp") : ""))
    page.destroy = () => {
        destroyed = true
        titleSub.unwatch()
        sub.unwatch()
        for (const id of timers) { clearInterval(id); clearTimeout(id) }
        timers.clear()
    }
    return page
}
