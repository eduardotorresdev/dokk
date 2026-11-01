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

// Config da GitHub App: App ID, slug e chave privada (que nunca volta do
// servidor; em branco mantém a atual).
function githubSection() {
    const ui = store({ loading: true, configured: false, appId: "", slug: "", key: "", installUrl: "", busy: false, error: "" })
    const apply = (b) => Object.assign(ui, { configured: !!b.configured, appId: b.appId ? String(b.appId) : "", slug: b.slug || "", installUrl: b.installUrl || "" })
    getJSON("/api/github").then(apply).catch((e) => ui.error = e.message).finally(() => ui.loading = false)

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

    return t.section({ className: "settings-card" },
        t.div({ className: "settings-card-head" },
            t.h3({ textContent: () => tr("settings.github.title") }),
            t.span({ className: "registry-status", hidden: () => ui.loading },
                t.span({ className: () => `dot ${ui.configured ? "dot-running" : "dot-exited"}` }),
                t.span({ textContent: () => tr(ui.configured ? "settings.github.status.configured" : "settings.github.status.missing") })),
        ),
        t.p({ className: "muted", textContent: () => tr("settings.github.text") }),
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
            t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
            t.div({ className: "settings-actions" },
                // .btn define display: o hidden não esconderia; renderiza só quando vale.
                () => ui.configured && ui.installUrl ? t.a({ rid: "install", className: "btn", href: ui.installUrl, target: "_blank", rel: "noopener", textContent: () => tr("settings.github.install") }) : null,
                t.button({ type: "submit", className: "btn btn-primary", disabled: () => ui.busy || ui.loading },
                    () => ui.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.github.busy") })] : [t.span({ textContent: () => tr("settings.github.save") })]),
            ),
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

// Página Configurações: GitHub App + tokens de API.
export function settings() {
    return t.main({ className: "page" },
        t.header({ className: "page-head" },
            t.div({ className: "title" }, t.h1({ textContent: () => tr("settings.title") })),
        ),
        t.div({ className: "settings" }, githubSection(), tokensSection()),
    )
}

// Cartão da aba Configurações da app: vincula a app a um repo do GitHub.
export function appGithubCard(name) {
    const base = `/api/apps/${encodeURIComponent(name)}/github`
    const ui = store({ loading: true, repo: null, installed: false, configured: false, installUrl: "", input: "", busy: false, error: "" })
    Promise.all([getJSON(base), getJSON("/api/github")])
        .then(([link, gh]) => {
            Object.assign(ui, { repo: link.repo, installed: link.installed, input: link.repo || "", configured: gh.configured, installUrl: gh.installUrl || "" })
        })
        .catch((e) => ui.error = e.message)
        .finally(() => ui.loading = false)

    const run = async (fn) => {
        if (ui.busy) return
        ui.busy = true
        ui.error = ""
        try {
            const link = await fn()
            Object.assign(ui, { repo: link.repo, installed: link.installed, input: link.repo || "" })
        } catch (err) {
            ui.error = err.message
        } finally { ui.busy = false }
    }
    const save = (e) => { e.preventDefault(); run(() => api("PUT", base, { repo: ui.input.trim() })) }
    const unlink = () => run(() => api("DELETE", base))

    return t.section({ className: "settings-card" },
        t.h3({ textContent: () => tr("settings.appGithub.title") }),
        t.p({ className: "muted", textContent: () => tr("settings.appGithub.text") }),
        t.form({ className: "rename-form", onsubmit: save },
            t.input({
                className: "input mono", autocomplete: "off", spellcheck: false, "html-aria-label": () => tr("settings.appGithub.repo.label"),
                placeholder: () => tr("settings.appGithub.repo.placeholder"), disabled: () => ui.loading || ui.busy,
                value: () => ui.input, oninput: (e) => { ui.input = e.target.value; ui.error = "" },
            }),
            t.button({ type: "submit", className: "btn", disabled: () => ui.busy || !ui.input.trim() || ui.input.trim() === ui.repo },
                () => ui.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.appGithub.busy") })] : [t.span({ textContent: () => tr("settings.appGithub.save") })]),
            () => ui.repo ? t.button({ rid: "unlink", type: "button", className: "btn", disabled: () => ui.busy, onclick: unlink, textContent: () => tr("settings.appGithub.unlink") }) : null,
        ),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
        t.div({ className: "settings-status", hidden: () => ui.loading || !ui.repo }, () => {
            if (!ui.configured) {
                return t.p({ rid: "nc", className: "muted" },
                    t.span({ textContent: () => tr("settings.appGithub.notConfigured") + " " }),
                    t.a({ href: "/settings", textContent: () => tr("settings.appGithub.configure") }))
            }
            if (ui.installed) {
                return t.span({ rid: "ok", className: "registry-status" }, t.span({ className: "dot dot-running" }), t.span({ textContent: () => tr("settings.appGithub.installed") }))
            }
            return t.p({ rid: "ni", className: "muted" },
                t.span({ className: "registry-status" }, t.span({ className: "dot dot-exited" }), t.span({ textContent: () => tr("settings.appGithub.notInstalled") })),
                " ",
                t.a({ href: () => ui.installUrl, target: "_blank", rel: "noopener", textContent: () => tr("settings.appGithub.install") }))
        }),
    )
}
