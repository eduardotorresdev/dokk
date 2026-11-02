import { t, store } from "shablon"
import { api, closeModal, confirmDialog, skeleton, toast } from "../shared.js"
import { fmtDate, register, tr } from "../i18n.js"
import en from "../locales/en/settings.js"
import es from "../locales/es/settings.js"
import ptBR from "../locales/pt-BR/settings.js"

register({ en, es, "pt-BR": ptBR })

const getJSON = async (path) => {
    const r = await fetch(path)
    const body = await r.json().catch(() => ({}))
    if (!r.ok) throw new Error(body.error || r.statusText)
    return body
}

// Envia o manifest para o GitHub num POST de form (o GitHub só aceita assim).
function postManifest(action, manifest) {
    const form = t.form({ method: "post", action, hidden: true },
        t.input({ type: "hidden", name: "manifest", value: manifest }))
    document.body.append(form)
    form.submit()
}

// Lê e limpa o ?github= que o servidor deixa ao voltar do GitHub. Guarda o
// valor por um instante: a página pode ser montada mais de uma vez na entrada.
let githubNotice = ""
function takeGithubNotice() {
    const q = new URLSearchParams(location.search)
    const v = q.get("github")
    if (v) {
        q.delete("github")
        history.replaceState(history.state, "", location.pathname + (q.size ? `?${q}` : ""))
        githubNotice = ["installed", "state", "error"].includes(v) ? v : ""
        setTimeout(() => githubNotice = "", 3000)
    }
    return githubNotice
}

// GitHub App: conectar pelo manifest flow (cria a App no GitHub com um
// clique); a configuração manual (App ID, slug, chave) fica em "Avançado".
function githubSection() {
    const ui = store({
        loading: true, configured: false, appId: "", slug: "", key: "", installUrl: "", busy: false, error: "",
        org: "", connecting: false, notice: takeGithubNotice(), repos: null, reposError: "",
    })
    const loadRepos = () => getJSON("/api/github/repos").then((l) => ui.repos = l).catch((e) => ui.reposError = e.message)
    const apply = (b) => {
        Object.assign(ui, { configured: !!b.configured, appId: b.appId ? String(b.appId) : "", slug: b.slug || "", installUrl: b.installUrl || "" })
        if (ui.configured && !ui.repos) loadRepos()
    }
    getJSON("/api/github").then(apply).catch((e) => ui.error = e.message).finally(() => ui.loading = false)
    if (ui.notice === "installed" && !takeGithubNotice.toasted) { takeGithubNotice.toasted = true; toast(tr("settings.github.notice.installed")); setTimeout(() => takeGithubNotice.toasted = false, 3000) }

    const connect = async (e) => {
        e.preventDefault()
        if (ui.connecting) return
        ui.connecting = true
        ui.error = ""
        try {
            const out = await api("POST", "/api/github/manifest", { org: ui.org.trim() })
            postManifest(out.action, out.manifest)
        } catch (err) {
            ui.error = err.message
            ui.connecting = false
        }
    }
    const save = async (e) => {
        e.preventDefault()
        if (ui.busy) return
        ui.busy = true
        ui.error = ""
        try {
            apply(await api("PUT", "/api/github", { appId: Number(ui.appId), slug: ui.slug.trim(), privateKey: ui.key }))
            ui.key = ""
            toast(tr("settings.github.saved"))
        } catch (err) {
            ui.error = err.message
        } finally { ui.busy = false }
    }
    const field = (label, input) => t.label({ className: "field" }, t.span({ textContent: label }), input)

    const connectForm = () => t.form({ rid: "connect", className: "settings-form", onsubmit: connect },
        field(() => tr("settings.github.org.label"), t.input({
            className: "input mono", autocomplete: "off", spellcheck: false, maxLength: 39,
            placeholder: () => tr("settings.github.org.placeholder"),
            value: () => ui.org, oninput: (e) => { ui.org = e.target.value; ui.error = "" },
        })),
        t.p({ className: "muted", textContent: () => tr("settings.github.org.hint") }),
        t.div({ className: "settings-actions" },
            t.button({ type: "submit", className: "btn btn-primary", disabled: () => ui.connecting || ui.loading },
                () => ui.connecting ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.github.connecting") })] : [t.span({ textContent: () => tr("settings.github.connect") })]),
        ),
    )
    const connected = () => t.div({ rid: "connected", className: "settings-form" },
        t.p({ className: "muted" },
            t.span({ textContent: () => tr("settings.github.connectedAs") + " " }),
            t.a({ className: "mono", href: `https://github.com/apps/${ui.slug}`, target: "_blank", rel: "noopener", textContent: ui.slug })),
        t.div({}, () => {
            if (ui.reposError) return t.p({ rid: "re", className: "form-error", textContent: ui.reposError })
            if (!ui.repos) return t.p({ rid: "rl", className: "muted", textContent: () => tr("settings.github.repos.loading") })
            if (!ui.repos.length) return t.p({ rid: "r0", className: "muted", textContent: () => tr("settings.github.repos.empty") })
            return t.div({ rid: "rs" },
                t.p({ className: "muted", textContent: () => tr("settings.github.repos.title", { n: ui.repos.length }) }),
                t.ul({ className: "gh-repo-list" }, ...ui.repos.map((r) => t.li({ className: "mono" },
                    t.span({ textContent: r.fullName }),
                    r.private ? t.span({ className: "chip", textContent: () => tr("settings.github.repos.private") }) : null))))
        }),
        t.div({ className: "settings-actions" },
            ui.installUrl ? t.a({ className: "btn", href: ui.installUrl, textContent: () => tr("settings.github.install") }) : null),
    )

    return t.section({ className: "settings-card" },
        t.div({ className: "settings-card-head" },
            t.h3({ textContent: () => tr("settings.github.title") }),
            t.span({ className: "registry-status", hidden: () => ui.loading },
                t.span({ className: () => `dot ${ui.configured ? "dot-running" : "dot-exited"}` }),
                t.span({ textContent: () => tr(ui.configured ? "settings.github.status.configured" : "settings.github.status.missing") })),
        ),
        t.p({ className: "muted", textContent: () => tr("settings.github.text") }),
        t.p({ className: "form-error", role: "alert", hidden: () => ui.notice !== "state" && ui.notice !== "error", textContent: () => ui.notice === "state" ? tr("settings.github.notice.state") : ui.notice === "error" ? tr("settings.github.notice.error") : "" }),
        t.div({}, () => ui.loading ? null : ui.configured ? connected() : connectForm()),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
        t.details({ className: "settings-advanced" },
            t.summary({ textContent: () => tr("settings.github.advanced") }),
            t.form({ className: "settings-form", onsubmit: save },
                field(() => tr("settings.github.appId.label"), t.input({
                    className: "input mono", inputMode: "numeric", required: true, autocomplete: "off",
                    value: () => ui.appId, oninput: (e) => { ui.appId = e.target.value.replace(/\D/g, ""); ui.error = "" },
                })),
                field(() => tr("settings.github.slug.label"), t.input({
                    className: "input mono", required: true, autocomplete: "off", spellcheck: false, title: () => tr("settings.github.slug.hint"),
                    value: () => ui.slug, oninput: (e) => { ui.slug = e.target.value; ui.error = "" },
                })),
                field(() => tr("settings.github.key.label"), t.textarea({
                    className: "input mono settings-key", rows: 4, spellcheck: false, autocomplete: "off",
                    required: () => !ui.configured,
                    placeholder: () => tr("settings.github.key.placeholder"),
                    value: () => ui.key, oninput: (e) => { ui.key = e.target.value; ui.error = "" },
                })),
                t.p({ className: "muted", hidden: () => !ui.configured, textContent: () => tr("settings.github.key.keep") }),
                t.div({ className: "settings-actions" },
                    t.button({ type: "submit", className: "btn", disabled: () => ui.busy || ui.loading },
                        () => ui.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.github.busy") })] : [t.span({ textContent: () => tr("settings.github.save") })]),
                ),
            ),
        ),
    )
}

// Lê e limpa o ?gitlab= que o servidor deixa ao voltar do GitLab (mesma
// lógica do ?github=).
let gitlabNotice = ""
function takeGitlabNotice() {
    const q = new URLSearchParams(location.search)
    const v = q.get("gitlab")
    if (v) {
        q.delete("gitlab")
        history.replaceState(history.state, "", location.pathname + (q.size ? `?${q}` : ""))
        gitlabNotice = ["connected", "state", "error"].includes(v) ? v : ""
        setTimeout(() => gitlabNotice = "", 3000)
    }
    return gitlabNotice
}

// Linha "rótulo: valor [Copiar]" do passo a passo do GitLab.
function copyRow(label, value) {
    const ui = store({ copied: false })
    return t.div({ className: "copy-row" },
        t.span({ className: "muted", textContent: label }),
        t.code({ className: "mono", textContent: value }),
        t.button({
            type: "button", className: "btn btn-sm", textContent: () => tr(ui.copied ? "settings.gitlab.copied" : "settings.gitlab.copy"),
            onclick: async () => {
                await navigator.clipboard?.writeText(typeof value === "function" ? value() : value).catch(() => {})
                ui.copied = true
                setTimeout(() => ui.copied = false, 1500)
            },
        }),
    )
}

// GitLab: não há App Manifest. O usuário cria uma OAuth Application na
// instância (passo a passo com os valores prontos) e cola ID + Secret; depois
// "Conectar ao GitLab" faz o OAuth com PKCE.
function gitlabSection() {
    const ui = store({
        loading: true, url: "https://gitlab.com", urlInput: "https://gitlab.com", group: "", clientId: "", secret: "",
        hasApp: false, connected: false, username: "", appName: "", redirectUri: "", scopes: "api",
        busy: false, connecting: false, error: "", notice: takeGitlabNotice(), repos: null, reposError: "",
    })
    const loadRepos = () => getJSON("/api/gitlab/repos").then((l) => ui.repos = l).catch((e) => ui.reposError = e.message)
    const apply = (b) => {
        Object.assign(ui, {
            url: b.url, urlInput: b.url, clientId: b.clientId || "", hasApp: !!b.hasApp, connected: !!b.connected,
            username: b.username || "", appName: b.appName || "", redirectUri: b.redirectUri || "", scopes: b.scopes || "api",
        })
        if (ui.connected && !ui.repos) loadRepos()
        if (!ui.connected) ui.repos = null
    }
    getJSON("/api/gitlab").then(apply).catch((e) => ui.error = e.message).finally(() => ui.loading = false)
    if (ui.notice === "connected" && !takeGitlabNotice.toasted) { takeGitlabNotice.toasted = true; toast(tr("settings.gitlab.notice.connected")); setTimeout(() => takeGitlabNotice.toasted = false, 3000) }

    const base = () => (ui.urlInput.trim() || "https://gitlab.com").replace(/\/+$/, "").replace(/^(?!https?:\/\/)/, "https://")
    const appsUrl = () => ui.group.trim()
        ? `${base()}/groups/${ui.group.trim().split("/").map(encodeURIComponent).join("/")}/-/settings/applications`
        : `${base()}/-/user_settings/applications`
    const run = async (fn) => {
        if (ui.busy) return
        ui.busy = true
        ui.error = ""
        try { await fn() } catch (err) { ui.error = err.message } finally { ui.busy = false }
    }
    const save = (e) => {
        e.preventDefault()
        run(async () => {
            apply(await api("PUT", "/api/gitlab", { url: ui.urlInput.trim(), clientId: ui.clientId.trim(), clientSecret: ui.secret.trim() }))
            ui.secret = ""
            toast(tr("settings.gitlab.saved"))
        })
    }
    const connect = async () => {
        if (ui.connecting) return
        ui.connecting = true
        ui.error = ""
        try {
            const out = await api("POST", "/api/gitlab/connect")
            location.href = out.url
        } catch (err) {
            ui.error = err.message
            ui.connecting = false
        }
    }
    const disconnect = async () => {
        if (!await confirmDialog({
            title: tr("settings.gitlab.disconnect.confirm.title"),
            text: tr("settings.gitlab.disconnect.confirm.text"),
            confirm: tr("settings.gitlab.disconnect"),
            danger: true,
        })) return
        run(async () => apply(await api("DELETE", "/api/gitlab/connection")))
    }
    const field = (label, input) => t.label({ className: "field" }, t.span({ textContent: label }), input)

    // Passo a passo + formulário da Application (também serve para trocar).
    const appForm = () => t.form({ rid: "app", className: "settings-form", onsubmit: save },
        t.ol({ className: "gitlab-steps" },
            t.li({},
                field(() => tr("settings.gitlab.url.label"), t.input({
                    className: "input mono", autocomplete: "off", spellcheck: false, placeholder: "https://gitlab.com",
                    value: () => ui.urlInput, oninput: (e) => { ui.urlInput = e.target.value; ui.error = "" },
                })),
                field(() => tr("settings.gitlab.group.label"), t.input({
                    className: "input mono", autocomplete: "off", spellcheck: false, placeholder: () => tr("settings.gitlab.group.placeholder"),
                    value: () => ui.group, oninput: (e) => { ui.group = e.target.value },
                })),
            ),
            t.li({},
                t.p({},
                    t.span({ textContent: () => tr("settings.gitlab.step.open") + " " }),
                    t.a({ href: appsUrl, target: "_blank", rel: "noopener", className: "mono", textContent: appsUrl })),
                t.div({ className: "copy-rows" },
                    copyRow(() => tr("settings.gitlab.step.name"), () => ui.appName),
                    copyRow(() => tr("settings.gitlab.step.redirect"), () => ui.redirectUri),
                    copyRow(() => tr("settings.gitlab.step.scopes"), () => ui.scopes),
                ),
                t.p({ className: "muted", textContent: () => tr("settings.gitlab.step.confidential") }),
            ),
            t.li({},
                t.p({ textContent: () => tr("settings.gitlab.step.paste") }),
                field(() => tr("settings.gitlab.clientId.label"), t.input({
                    className: "input mono", required: true, autocomplete: "off", spellcheck: false,
                    value: () => ui.clientId, oninput: (e) => { ui.clientId = e.target.value; ui.error = "" },
                })),
                field(() => tr("settings.gitlab.secret.label"), t.input({
                    className: "input mono", type: "password", autocomplete: "off", spellcheck: false,
                    required: () => !ui.hasApp,
                    value: () => ui.secret, oninput: (e) => { ui.secret = e.target.value; ui.error = "" },
                })),
                t.p({ className: "muted", hidden: () => !ui.hasApp, textContent: () => tr("settings.gitlab.secret.keep") }),
            ),
        ),
        t.div({ className: "settings-actions" },
            t.button({ type: "submit", className: () => ui.hasApp ? "btn" : "btn btn-primary", disabled: () => ui.busy || ui.loading || !ui.clientId.trim() },
                () => ui.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.gitlab.busy") })] : [t.span({ textContent: () => tr("settings.gitlab.save") })]),
        ),
    )
    const connectButton = () => t.div({ rid: "connect", className: "settings-actions" },
        t.button({ type: "button", className: "btn btn-primary", disabled: () => ui.connecting || ui.loading, onclick: connect },
            () => ui.connecting ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.gitlab.connecting") })] : [t.span({ textContent: () => tr("settings.gitlab.connect") })]))
    const connected = () => t.div({ rid: "connected", className: "settings-form" },
        t.p({ className: "muted" },
            t.span({ textContent: () => tr("settings.gitlab.connectedAs") + " " }),
            t.a({ className: "mono", href: `${ui.url}/${ui.username}`, target: "_blank", rel: "noopener", textContent: ui.username ? `@${ui.username}` : ui.url }),
            t.span({ textContent: ` · ${ui.url.replace(/^https?:\/\//, "")}` })),
        t.div({}, () => {
            if (ui.reposError) return t.p({ rid: "re", className: "form-error", textContent: ui.reposError })
            if (!ui.repos) return t.p({ rid: "rl", className: "muted", textContent: () => tr("settings.github.repos.loading") })
            if (!ui.repos.length) return t.p({ rid: "r0", className: "muted", textContent: () => tr("settings.gitlab.repos.empty") })
            return t.div({ rid: "rs" },
                t.p({ className: "muted", textContent: () => tr("settings.gitlab.repos.title", { n: ui.repos.length }) }),
                t.ul({ className: "gh-repo-list" }, ...ui.repos.map((r) => t.li({ className: "mono" },
                    t.span({ textContent: r.fullName }),
                    r.private ? t.span({ className: "chip", textContent: () => tr("settings.github.repos.private") }) : null))))
        }),
        t.div({ className: "settings-actions" },
            t.button({ type: "button", className: "btn", disabled: () => ui.busy, onclick: disconnect, textContent: () => tr("settings.gitlab.disconnect") })),
    )

    return t.section({ className: "settings-card" },
        t.div({ className: "settings-card-head" },
            t.h3({ textContent: () => tr("settings.gitlab.title") }),
            t.span({ className: "registry-status", hidden: () => ui.loading },
                t.span({ className: () => `dot ${ui.connected ? "dot-running" : "dot-exited"}` }),
                t.span({ textContent: () => tr(ui.connected ? "settings.gitlab.status.connected" : ui.hasApp ? "settings.gitlab.status.ready" : "settings.gitlab.status.missing") })),
        ),
        t.p({ className: "muted", textContent: () => tr("settings.gitlab.text") }),
        t.p({ className: "form-error", role: "alert", hidden: () => ui.notice !== "state" && ui.notice !== "error", textContent: () => ui.notice === "state" ? tr("settings.gitlab.notice.state") : ui.notice === "error" ? tr("settings.gitlab.notice.error") : "" }),
        t.div({}, () => {
            if (ui.loading) return null
            if (ui.connected) return connected()
            if (ui.hasApp) return connectButton()
            return appForm()
        }),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
        t.details({ className: "settings-advanced", hidden: () => ui.loading || !ui.hasApp },
            t.summary({ textContent: () => tr("settings.gitlab.advanced") }),
            t.div({}, () => ui.hasApp ? appForm() : null),
        ),
    )
}

// Mostra o token recém-criado uma única vez, com botão de copiar.
function newTokenDialog(token) {
    const ui = store({ copied: false })
    const dialog = t.dialog({ className: "modal" },
        t.h2({ className: "modal-title", textContent: () => tr("settings.tokens.new.title") }),
        t.p({ className: "modal-text", textContent: () => tr("settings.tokens.new.text") }),
        t.div({ className: "token-reveal" },
            t.input({ className: "input mono", readOnly: true, value: token, onfocus: (e) => e.target.select() }),
            t.button({
                type: "button", className: "btn", textContent: () => tr(ui.copied ? "settings.tokens.new.copied" : "settings.tokens.new.copy"),
                onclick: async () => { await navigator.clipboard?.writeText(token).catch(() => {}); ui.copied = true },
            }),
        ),
        t.div({ className: "modal-actions" },
            t.button({ type: "button", className: "btn btn-primary", textContent: () => tr("settings.tokens.new.done"), onclick: () => closeModal(dialog, { remove: true }) }),
        ),
    )
    dialog.addEventListener("cancel", (e) => { e.preventDefault(); closeModal(dialog, { remove: true }) })
    document.body.append(dialog)
    dialog.showModal()
}

function tokensSection() {
    const ui = store({ list: [], loading: true, name: "", busy: false, error: "" })
    const load = () => getJSON("/api/tokens").then((l) => ui.list = l).catch((e) => ui.error = e.message).finally(() => ui.loading = false)
    load()

    const create = async (e) => {
        e.preventDefault()
        if (ui.busy || !ui.name.trim()) return
        ui.busy = true
        ui.error = ""
        try {
            const out = await api("POST", "/api/tokens", { name: ui.name.trim() })
            ui.name = ""
            newTokenDialog(out.token)
            await load()
        } catch (err) {
            ui.error = err.message
        } finally { ui.busy = false }
    }
    const revoke = async (tok) => {
        if (!await confirmDialog({
            title: tr("settings.tokens.delete.confirm.title", { name: tok.name }),
            text: tr("settings.tokens.delete.confirm.text"),
            confirm: tr("settings.tokens.delete.submit"),
            danger: true,
        })) return
        try {
            await api("DELETE", `/api/tokens/${encodeURIComponent(tok.id)}`)
            await load()
        } catch (err) { ui.error = err.message }
    }
    const row = (tok) => t.li({ rid: tok.id, className: "token-row" },
        t.div({ className: "token-main" },
            t.strong({ textContent: tok.name }),
            t.span({
                className: "muted", textContent: () => [
                    tr("settings.tokens.createdAt", { date: fmtDate(new Date(tok.createdAt)) }),
                    tok.lastUsedAt ? tr("settings.tokens.lastUsed", { date: fmtDate(new Date(tok.lastUsedAt)) }) : tr("settings.tokens.neverUsed"),
                ].join(" · "),
            }),
        ),
        t.button({ type: "button", className: "icon-btn icon-btn-danger", title: () => tr("settings.tokens.delete.title"), onclick: () => revoke(tok) },
            t.span({ className: "icon icon-trash" })),
    )

    return t.section({ className: "settings-card" },
        t.h3({ textContent: () => tr("settings.tokens.title") }),
        t.p({ className: "muted", textContent: () => tr("settings.tokens.text") }),
        t.form({ className: "rename-form", onsubmit: create },
            t.input({
                className: "input", maxLength: 64, autocomplete: "off", "html-aria-label": () => tr("settings.tokens.name.label"),
                placeholder: () => tr("settings.tokens.name.placeholder"),
                value: () => ui.name, oninput: (e) => { ui.name = e.target.value; ui.error = "" },
            }),
            t.button({ type: "submit", className: "btn btn-primary", disabled: () => ui.busy || !ui.name.trim() },
                () => ui.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.tokens.busy") })] : [t.span({ textContent: () => tr("settings.tokens.create") })]),
        ),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
        t.div({}, () => {
            if (ui.loading) return skeleton(t.ul({ className: "token-list" }, row({ id: "x", name: "token", createdAt: new Date().toISOString() })))
            if (!ui.list.length) return t.p({ rid: "empty", className: "muted", textContent: () => tr("settings.tokens.empty") })
            return t.ul({ className: "token-list" }, ...ui.list.map(row))
        }),
    )
}

// Página Configurações: GitHub e GitLab lado a lado + tokens de API.
export function settings() {
    return t.main({ className: "page" },
        t.header({ className: "page-head" },
            t.div({ className: "title" }, t.h1({ textContent: () => tr("settings.title") })),
        ),
        t.div({ className: "settings" },
            t.div({ className: "settings-providers" }, githubSection(), gitlabSection()),
            tokensSection()),
    )
}

// Próxima versão (patch) a partir da maior estável da lista.
function nextVersion(versions) {
    let cur = [0, 0, 0]
    for (const v of versions) {
        const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(v || "")
        if (!m) continue
        const n = m.slice(1).map(Number)
        if (n[0] > cur[0] || (n[0] === cur[0] && (n[1] > cur[1] || (n[1] === cur[1] && n[2] > cur[2])))) cur = n
    }
    return `${cur[0]}.${cur[1]}.${cur[2] + 1}`
}

const PROVIDERS = [{ key: "github", label: "GitHub" }, { key: "gitlab", label: "GitLab" }]

// Cartão da aba Configurações da app: vincula a app a um repo do GitHub ou
// do GitLab (rotas neutras /repo).
export function appRepoCard(name) {
    const base = `/api/apps/${encodeURIComponent(name)}/repo`
    const ui = store({
        loading: true, provider: "github", linkedProvider: null, repo: null, installed: false, input: "", busy: false, error: "",
        repos: [], format: "v{version}", formatInput: "v{version}", versions: [],
        github: { configured: false, installUrl: "" }, gitlab: { connected: false },
    })
    const listId = `repos-${name}`
    const ready = (p) => p === "gitlab" ? ui.gitlab.connected : ui.github.configured
    Promise.all([getJSON(base), getJSON("/api/github").catch(() => ({})), getJSON("/api/gitlab").catch(() => ({}))])
        .then(([link, gh, gl]) => {
            ui.github = { configured: !!gh.configured, installUrl: gh.installUrl || "" }
            ui.gitlab = { connected: !!gl.connected }
            applyLink(link)
            // Sem vínculo: começa no provedor conectado (GitHub se os dois).
            if (!link.repo && !ui.github.configured && ui.gitlab.connected) ui.provider = "gitlab"
            if (ui.github.configured || ui.gitlab.connected) getJSON("/api/repos").then((l) => ui.repos = l).catch(() => {})
        })
        .catch((e) => ui.error = e.message)
        .finally(() => ui.loading = false)

    const applyLink = (link) => {
        Object.assign(ui, { repo: link.repo, installed: link.installed, input: link.repo || "", linkedProvider: link.provider || null })
        if (link.provider) ui.provider = link.provider
        ui.format = ui.formatInput = link.tagFormat || "v{version}"
        loadVersions()
    }
    const loadVersions = () => {
        ui.versions = []
        if (ui.repo && ui.installed) getJSON(`${base}/releases`).then((l) => ui.versions = l.map((r) => r.version)).catch(() => {})
    }
    const run = async (fn) => {
        if (ui.busy) return
        ui.busy = true
        ui.error = ""
        try {
            applyLink(await fn())
        } catch (err) {
            ui.error = err.message
        } finally { ui.busy = false }
    }
    const save = (e) => { e.preventDefault(); run(() => api("PUT", base, { provider: ui.provider, repo: ui.input.trim(), tagFormat: ui.formatInput.trim() })) }
    const formatOK = () => (ui.formatInput.trim().match(/\{version\}/g) || []).length === 1
    const dirty = () => ui.input.trim() !== (ui.repo || "") || ui.provider !== (ui.linkedProvider || ui.provider) || (!!ui.input.trim() && ui.formatInput.trim() !== ui.format)
    const unlink = () => run(() => api("DELETE", base))
    const reposOf = () => ui.repos.filter((r) => (r.provider || "github") === ui.provider)
    const label = (p) => PROVIDERS.find((x) => x.key === p)?.label || p

    return t.section({ className: "settings-card" },
        t.h3({ textContent: () => tr("settings.appRepo.title") }),
        t.p({ className: "muted", textContent: () => tr("settings.appRepo.text") }),
        t.form({ className: "rename-form", onsubmit: save },
            t.select({
                className: "input repo-provider", "html-aria-label": () => tr("settings.appRepo.provider.label"), disabled: () => ui.loading || ui.busy,
                onchange: (e) => { ui.provider = e.target.value; ui.error = ""; if (ui.provider !== ui.linkedProvider) ui.input = "" },
            }, ...PROVIDERS.map((p) => t.option({ value: p.key, textContent: p.label, selected: () => ui.provider === p.key }))),
            t.input({
                className: "input mono", autocomplete: "off", spellcheck: false, "html-aria-label": () => tr("settings.appRepo.repo.label"),
                "html-list": listId, type: "search",
                placeholder: () => reposOf().length ? tr("settings.appRepo.repo.pick") : ui.provider === "gitlab" ? "group/project" : "owner/repo",
                disabled: () => ui.loading || ui.busy,
                value: () => ui.input, oninput: (e) => { ui.input = e.target.value; ui.error = "" },
            }),
            t.button({ type: "submit", className: "btn", disabled: () => ui.busy || !ui.input.trim() || !formatOK() || !dirty() },
                () => ui.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.appRepo.busy") })] : [t.span({ textContent: () => tr("settings.appRepo.save") })]),
            t.datalist({ id: listId }, () => reposOf().map((r) => t.option({ rid: `${r.provider}:${r.fullName}`, value: r.fullName }))),
            () => ui.repo ? t.button({ rid: "unlink", type: "button", className: "btn", disabled: () => ui.busy, onclick: unlink, textContent: () => tr("settings.appRepo.unlink") }) : null,
        ),
        t.label({ className: "field tag-format" },
            t.span({ textContent: () => tr("settings.appRepo.format.label") }),
            t.input({
                className: "input mono", autocomplete: "off", spellcheck: false, maxLength: 100,
                placeholder: "v{version}", disabled: () => ui.loading || ui.busy,
                value: () => ui.formatInput, oninput: (e) => { ui.formatInput = e.target.value; ui.error = "" },
            }),
            t.span({
                className: () => formatOK() ? "muted" : "form-error",
                textContent: () => formatOK()
                    ? tr("settings.appRepo.format.next", { tag: ui.formatInput.trim().replace("{version}", nextVersion(ui.versions)) })
                    : tr("settings.appRepo.format.invalid"),
            }),
        ),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
        t.div({ className: "settings-status", hidden: () => ui.loading || !ui.repo }, () => {
            const p = ui.linkedProvider || "github"
            if (!ready(p)) {
                return t.p({ rid: `nc-${p}`, className: "muted" },
                    t.span({ textContent: () => tr(p === "gitlab" ? "settings.appRepo.gitlab.notConnected" : "settings.appRepo.github.notConfigured") + " " }),
                    t.a({ href: "/settings", textContent: () => tr("settings.appRepo.configure") }))
            }
            if (ui.installed) {
                return t.span({ rid: `ok-${p}`, className: "registry-status" }, t.span({ className: "dot dot-running" }),
                    t.span({ textContent: () => tr("settings.appRepo.ok", { provider: label(p) }) }))
            }
            if (p === "gitlab") {
                return t.p({ rid: "na-gitlab", className: "muted" },
                    t.span({ className: "registry-status" }, t.span({ className: "dot dot-exited" }), t.span({ textContent: () => tr("settings.appRepo.gitlab.noAccess") })))
            }
            return t.p({ rid: "ni", className: "muted" },
                t.span({ className: "registry-status" }, t.span({ className: "dot dot-exited" }), t.span({ textContent: () => tr("settings.appRepo.github.notInstalled") })),
                " ",
                t.a({ href: () => ui.github.installUrl, target: "_blank", rel: "noopener", textContent: () => tr("settings.appRepo.github.install") }))
        }),
    )
}
