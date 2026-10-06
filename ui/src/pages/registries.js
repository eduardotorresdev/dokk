import { t, store } from "shablon"
import { api, closeModal, confirmDialog, skeleton } from "../shared.js"
import { register, tr } from "../i18n.js"
import en from "../locales/en/registries.js"
import es from "../locales/es/registries.js"
import ptBR from "../locales/pt-BR/registries.js"

register({ en, es, "pt-BR": ptBR })

// Provedores conhecidos: só preenchem o servidor e as dicas do formulário.
// label/user/pass são getters traduzidos: leia-os dentro de um binding.
export const PROVIDERS = [
    { key: "ghcr", get label() { return "GitHub (ghcr.io)" }, server: "ghcr.io", get user() { return tr("registries.provider.ghcr.user") }, get pass() { return tr("registries.provider.ghcr.pass") } },
    { key: "hub", get label() { return "Docker Hub" }, server: "docker.io", get user() { return "" }, get pass() { return tr("registries.provider.hub.pass") } },
    { key: "gitlab", get label() { return "GitLab" }, server: "registry.gitlab.com", get user() { return "" }, get pass() { return tr("registries.provider.gitlab.pass") } },
    { key: "other", get label() { return tr("registries.provider.other.label") }, server: "", get user() { return "" }, get pass() { return "" } },
]

// Ícone (traços SVG em viewBox 24) por provedor; os desconhecidos usam a caixa.
const PROVIDER_ICONS = {
    "ghcr.io": '<path d="M9 19c-4 1.5-4-2-6-2.5M15 21v-3.5c0-1 .1-1.4-.5-2 2.8-.3 5.5-1.4 5.5-6a4.6 4.6 0 0 0-1.3-3.2 4.2 4.2 0 0 0-.1-3.2s-1.1-.3-3.5 1.3a12 12 0 0 0-6.2 0C6.5 2.8 5.4 3.1 5.4 3.1a4.2 4.2 0 0 0-.1 3.2A4.6 4.6 0 0 0 4 9.5c0 4.6 2.7 5.7 5.5 6-.6.6-.6 1.2-.5 2V21"/>',
    "docker.io": '<path d="M3 13h16.5c.5-1 .5-2.5 1.5-3 0 0-1.5-1-3 0-.5-1.5-1.5-2-1.5-2l-1 1.5V13"/><path d="M3 13c0 4 3 7 8 7 4.5 0 7.5-2.5 8.5-7"/><path d="M6 10h3v3H6zM9 10h3v3H9zM12 10h3v3h-3zM9 7h3v3H9z"/>',
    "registry.gitlab.com": '<path d="m12 21-9-7 2.5-10 3 7h7l3-7L21 14Z"/>',
}
const BOX_ICON = '<path d="M12 3 20 7.5v9L12 21l-8-4.5v-9Z"/><path d="M4 7.5 12 12l8-4.5M12 12v9"/>'

function svgIcon(className, paths) {
    const el = t.span({ className, "html-aria-hidden": "true" })
    el.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${paths}</svg>`
    return el
}

function registryCard(r, logout) {
    const known = PROVIDERS.find((p) => p.server && p.server === r.server)
    return t.li({ rid: `${r.server}-${r.username}-${!!r.helper}`, className: "registry-card" },
        svgIcon("registry-icon", PROVIDER_ICONS[r.server] || BOX_ICON),
        t.div({ className: "registry-main" },
            t.strong({ textContent: () => known ? known.label : r.server }),
            t.span({ className: "muted mono", textContent: r.server }),
            t.span({ className: "muted", textContent: () => r.helper ? tr("registries.row.helper") : tr("registries.row.user", { user: r.username }) }),
        ),
        t.span({ className: "registry-status" }, t.span({ className: "dot dot-running" }), t.span({ textContent: () => tr("registries.row.connected") })),
        t.button({ type: "button", className: "icon-btn icon-btn-danger", title: () => tr("registries.row.logout.title"), onclick: () => logout(r.server) },
            t.span({ className: "icon icon-logout" })),
    )
}

// Dialog de login num registry; resolve com a lista nova (ou null se cancelar).
function loginDialog() {
    return new Promise((resolve) => {
        const ui = store({ provider: "ghcr", server: "", username: "", password: "", busy: false, error: "" })
        const provider = () => PROVIDERS.find((p) => p.key === ui.provider)
        const submit = async (e) => {
            e.preventDefault()
            if (ui.busy) return
            ui.busy = true
            ui.error = ""
            try {
                const server = provider().server || ui.server.trim()
                const body = await api("POST", "/api/registries", { server, username: ui.username.trim(), password: ui.password })
                done(body.registries || [])
            } catch (err) {
                ui.error = err.message
            } finally {
                ui.busy = false
            }
        }
        const dialog = t.dialog({ className: "modal registry-dialog" },
            t.form({ onsubmit: submit },
                t.h2({ className: "modal-title", textContent: () => tr("registries.form.title") }),
                t.p({ className: "modal-text", textContent: () => tr("registries.form.text") }),
                t.label({ className: "field" },
                    t.span({ textContent: () => tr("registries.form.provider.label") }),
                    // Ícone do provedor como addon à esquerda do select.
                    t.div({ className: "input-addon" },
                        t.span({ className: "input-addon-icon" }, () => svgIcon("", PROVIDER_ICONS[provider().server] || BOX_ICON)),
                        t.select({ className: "input", onchange: (e) => { ui.provider = e.target.value; ui.error = "" } },
                            ...PROVIDERS.map((p) => t.option({ value: p.key, textContent: () => p.label, selected: p.key === ui.provider }))),
                    ),
                ),
                t.label({ className: "field", hidden: () => ui.provider !== "other" },
                    t.span({ textContent: () => tr("registries.form.server.label") }),
                    t.input({
                        className: "input mono", placeholder: () => tr("registries.form.server.placeholder"), autocomplete: "off", spellcheck: false,
                        required: () => ui.provider === "other",
                        value: () => ui.server, oninput: (e) => { ui.server = e.target.value; ui.error = "" },
                    }),
                ),
                t.label({ className: "field" },
                    t.span({ textContent: () => tr("registries.form.user.label") }),
                    t.input({
                        className: "input", autocomplete: "off", spellcheck: false, required: true,
                        placeholder: () => provider().user || tr("registries.form.user.placeholder"),
                        value: () => ui.username, oninput: (e) => { ui.username = e.target.value; ui.error = "" },
                    }),
                ),
                t.label({ className: "field" },
                    t.span({ textContent: () => tr("registries.form.pass.label") }),
                    t.input({
                        className: "input", type: "password", autocomplete: "new-password", required: true,
                        placeholder: () => provider().pass || "••••••••",
                        value: () => ui.password, oninput: (e) => { ui.password = e.target.value; ui.error = "" },
                    }),
                ),
                t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
                t.div({ className: "modal-actions" },
                    t.button({ type: "button", className: "btn", textContent: () => tr("common.cancel"), onclick: () => done(null) }),
                    t.button({ type: "submit", className: "btn btn-primary", disabled: () => ui.busy },
                        () => ui.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("registries.form.busy") })] : [t.span({ textContent: () => tr("registries.form.submit") })]),
                ),
            ),
        )
        let settled = false
        function done(value) {
            if (settled) return
            settled = true
            closeModal(dialog, { remove: true })
            resolve(value)
        }
        dialog.addEventListener("cancel", (e) => { e.preventDefault(); if (!ui.busy) done(null) })
        dialog.addEventListener("click", (e) => e.target === dialog && !ui.busy && done(null))
        document.body.append(dialog)
        dialog.showModal()
        dialog.querySelector("select").focus()
    })
}

// Página Registries: lista os logins globais do Dokku; adicionar é num dialog.
export function registries() {
    const ui = store({ list: [], loading: true, loadError: "" })

    fetch("/api/registries")
        .then(async (r) => {
            const body = await r.json().catch(() => ({}))
            if (!r.ok) throw new Error(body.error || r.statusText)
            ui.list = body.registries || []
        })
        .catch((err) => ui.loadError = err.message)
        .finally(() => ui.loading = false)

    async function add() {
        const list = await loginDialog()
        if (list) ui.list = list
    }

    async function logout(server) {
        if (!await confirmDialog({
            title: tr("registries.logout.confirm.title", { server }),
            text: tr("registries.logout.confirm.text"),
            confirm: tr("registries.logout.confirm.submit"),
            danger: true,
        })) return
        try {
            const body = await api("DELETE", `/api/registries?server=${encodeURIComponent(server)}`)
            ui.list = body.registries || []
        } catch (err) {
            ui.loadError = err.message
        }
    }

    const addButton = (className) => t.button({ type: "button", className, onclick: add },
        t.span({ className: "icon icon-plus" }),
        t.span({ textContent: () => tr("registries.add") }),
    )

    return t.main({ className: "page" },
        t.header({ className: "page-head" },
            t.div({ className: "title" },
                t.h1({ textContent: () => tr("registries.title") }),
            ),
            t.div({ hidden: () => ui.loading || !ui.list.length }, addButton("btn btn-primary")),
        ),
        t.div({}, () => {
            if (ui.loading) {
                return skeleton(t.ul({ className: "registry-list" }, registryCard({ server: "ghcr.io", username: "user" }, () => {})))
            }
            if (ui.loadError) return t.p({ rid: "err", className: "form-error", textContent: ui.loadError })
            if (!ui.list.length) {
                return t.div({ rid: "empty", className: "empty-state" },
                    svgIcon("empty-icon", '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3M12 15v2"/>'),
                    t.h2({ textContent: () => tr("registries.empty.title") }),
                    t.p({ className: "muted", textContent: () => tr("registries.empty.text") }),
                    t.div({ className: "empty-actions" }, addButton("btn btn-primary")),
                )
            }
            return t.ul({ className: "registry-list" }, ...ui.list.map((r) => registryCard(r, logout)))
        }),
    )
}
