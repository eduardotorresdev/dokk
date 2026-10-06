import { t, store, watch } from "shablon"
import { cached, domains, navigate, fakeApp, mutate, swr, heatmap, liveBadge, skeleton, statusBadge, subscribeApps, timeAgo, truncateMiddle, api, closeModal, confirmDialog, actionRunningLabel, toast } from "../shared.js"
import { errText, fmtDate, fmtDateTime, fmtTime, i18n, register, tr, trn } from "../i18n.js"
import en from "../locales/en/app.js"
import es from "../locales/es/app.js"
import ptBR from "../locales/pt-BR/app.js"

register({ en, es, "pt-BR": ptBR })

const MAX_LOG_LINES = 1000

// Rótulos traduzidos: chame dentro de um binding.
const BUILD_STATUS = {
    succeeded: () => tr("app.build.succeeded"),
    failed: () => tr("app.build.failed"),
    running: () => tr("app.build.running"),
}

const TABS = [
    { key: "history", get label() { return tr("app.tabs.history") }, icon: "icon-history" },
    { key: "versions", get label() { return tr("app.tabs.versions") }, icon: "icon-versions" },
    { key: "logs", get label() { return tr("app.tabs.logs") }, icon: "icon-logs" },
    { key: "vars", get label() { return tr("app.tabs.vars") }, icon: "icon-vars" },
    { key: "settings", get label() { return tr("app.tabs.settings") }, icon: "icon-settings" },
]

// SSL: selo com cadeado e validade (cor pelo tempo restante) e um botão
// ghost compacto para reemitir o certificado.
function sslBadge(name, data) {
    const ssl = data.detail?.ssl
    if (!ssl) return null
    const exp = ssl["expires-at"] ? new Date(ssl["expires-at"]) : null
    const days = exp ? Math.floor((exp - Date.now()) / 86400000) : null
    const state = !ssl.enabled ? "off" : days == null ? "ok" : days < 0 ? "bad" : days < 15 ? "warn" : "ok"
    const text = () => !ssl.enabled ? tr("app.ssl.off")
        : days == null ? tr("app.ssl.on")
        : days < 0 ? tr("app.ssl.expired")
        : days === 0 ? tr("app.ssl.today")
        : trn("app.ssl.days", days)
    const renew = async () => {
        if (!await confirmDialog({
            title: tr("app.ssl.renew.confirm.title", { name }),
            text: tr("app.ssl.renew.confirm.text"),
            confirm: tr("app.ssl.renew.submit"),
        })) return
        try {
            data.action = await api("POST", `/api/apps/${encodeURIComponent(name)}/ssl-renew`)
        } catch (err) {
            data.action = { name: "ssl-renew", running: false, error: err.message }
        }
    }
    return t.span({ className: "ssl-wrap" },
        t.span({
            className: `ssl-badge ssl-${state}`,
            title: () => exp ? `${tr("app.ssl.validUntil", { date: fmtDate(exp) })}${ssl.issuer ? ` · ${ssl.issuer}` : ""}` : "",
        }, t.span({ className: "icon icon-lock" }), t.span({ textContent: text })),
        t.button({
            type: "button", className: "ssl-renew", onclick: renew,
            disabled: () => !!data.action?.running,
            title: () => ssl.enabled ? tr("app.ssl.renew.title") : tr("app.ssl.issue.title"),
            "html-aria-label": () => ssl.enabled ? tr("app.ssl.renew.title") : tr("app.ssl.issue.title"),
        }, t.span({ className: "icon icon-refresh" }), t.span({ textContent: () => ssl.enabled ? tr("app.ssl.renew.submit") : tr("app.ssl.issue.submit") })),
        ssl.enabled ? autoRenewToggle() : null,
    )
}

// Auto-renovação do letsencrypt. O plugin só tem o cron global, então o
// toggle vale para todas as apps (o tooltip avisa).
const autoRenew = store({ enabled: null, busy: false, error: "" })
function autoRenewToggle() {
    if (autoRenew.enabled === null && !autoRenew.busy) {
        autoRenew.busy = true
        fetch("/api/ssl/autorenew").then((r) => r.ok ? r.json() : null)
            .then((b) => { if (b) autoRenew.enabled = b.enabled })
            .catch(() => {}).finally(() => { autoRenew.busy = false })
    }
    const toggle = async () => {
        const on = !autoRenew.enabled
        if (!on && !await confirmDialog({
            title: tr("app.ssl.auto.confirm.title"),
            text: tr("app.ssl.auto.confirm.text"),
            confirm: tr("app.ssl.auto.confirm.submit"),
            danger: true,
        })) return
        autoRenew.busy = true
        autoRenew.error = ""
        try {
            autoRenew.enabled = (await api("PUT", "/api/ssl/autorenew", { enabled: on })).enabled
        } catch (err) {
            autoRenew.error = err.message
        } finally { autoRenew.busy = false }
    }
    return t.button({
        type: "button",
        className: () => `ssl-auto${autoRenew.enabled ? " on" : ""}`,
        role: "switch",
        "html-aria-checked": () => String(!!autoRenew.enabled),
        disabled: () => autoRenew.busy || autoRenew.enabled === null,
        onclick: toggle,
        title: () => autoRenew.error || tr("app.ssl.auto.title"),
    }, t.span({ className: "ssl-auto-track" }, t.span({ className: "ssl-auto-thumb" })), t.span({ textContent: () => tr("app.ssl.auto.label") }))
}

// CTAs do cabeçalho: ligar quando suspensa; reiniciar/desligar quando no
// ar. Durante uma ação mostra o andamento no lugar dos botões.
function appActions(name, data) {
    const trigger = async (action) => {
        const prompts = {
            restart: { title: tr("app.actions.restart.confirm.title", { name }), text: tr("app.actions.restart.confirm.text"), confirm: tr("app.actions.restart.submit") },
            stop: { title: tr("app.actions.stop.confirm.title", { name }), text: tr("app.actions.stop.confirm.text"), confirm: tr("app.actions.stop.submit"), danger: true },
        }
        if (prompts[action] && !await confirmDialog(prompts[action])) return
        try {
            data.action = await api("POST", `/api/apps/${encodeURIComponent(name)}/${action}`)
        } catch (err) {
            data.action = { name: action, running: false, error: err.message }
        }
    }
    return t.div({ className: "app-actions" }, () => {
        const action = data.action
        if (!data.app) return [skeleton(t.button({ type: "button", className: "btn", textContent: () => tr("app.actions.restart.submit") }))]
        if (action?.running) {
            return [t.button({ type: "button", className: "btn", disabled: true },
                t.span({ className: "spinner" }), t.span({ textContent: () => actionRunningLabel(action.name) }))]
        }
        const off = data.app.checks.status === "suspended"
        return [
            action?.error ? t.span({ className: "action-error", title: action.error, textContent: () => tr("app.actions.failed") }) : null,
            off
                ? t.button({ type: "button", className: "btn btn-primary", onclick: () => trigger("start") },
                    t.span({ className: "icon icon-power" }), t.span({ textContent: () => tr("app.actions.start.submit") }))
                : [
                    t.button({ type: "button", className: "btn", onclick: () => trigger("restart") },
                        t.span({ className: "icon icon-restart" }), t.span({ textContent: () => tr("app.actions.restart.submit") })),
                    t.button({ type: "button", className: "btn btn-danger-ghost", onclick: () => trigger("stop") },
                        t.span({ className: "icon icon-power" }), t.span({ textContent: () => tr("app.actions.stop.submit") })),
                ],
        ].flat()
    })
}

const SVG = "http://www.w3.org/2000/svg"

// Status do build para exibição. exit_code -1 é um build que morreu no meio
// e só foi encerrado pelo dokku muito depois. Traduzido: use num binding.
function buildStatus(b) {
    if (b.exit_code === -1) return tr("app.build.interrupted")
    return BUILD_STATUS[b.status]?.() || b.status
}

function duration(b) {
    if (!b.started_at || !b.finished_at || b.exit_code === -1) return ""
    const s = Math.round((new Date(b.finished_at) - new Date(b.started_at)) / 1000)
    if (s < 0) return ""
    return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`
}

function appHref(name) {
    return `/apps/${encodeURIComponent(name)}`
}

// ---------- board ----------

// Um bloco do diagrama: ícone do tipo + título + linhas de detalhe. O
// data-wire marca de qual lado ele se liga às máquinas. title/meta podem ser
// funções (texto traduzido).
function block(kind, wire, { title, meta, href, external, led: ledState, extra } = {}) {
    const head = [
        t.span({ className: `block-icon icon-${kind}` }, t.span({ className: "icon" })),
        t.div({ className: "block-text" },
            t.strong({ className: "block-title", textContent: title, title }),
            meta ? t.span({ className: "block-meta", textContent: meta, title: meta }) : null,
        ),
        ledState ? led(ledState) : null,
    ]
    const attrs = { className: `block block-${kind}`, "html-data-wire": wire }
    if (href) {
        return t.a({ ...attrs, href, ...(external ? { target: "_blank", rel: "noopener" } : {}) },
            t.div({ className: "block-head" }, ...head), extra || null)
    }
    return t.div(attrs, t.div({ className: "block-head" }, ...head), extra || null)
}

function led(state) {
    return t.span({ className: `led led-${state}`, title: state })
}

function machine(c) {
    const cpu = parseFloat(c.cpu) || 0
    const mem = parseFloat(c["mem-perc"]) || 0
    const index = c.name.split(".")[2]
    return block("machine", c.retiring ? "" : "machine", {
        title: `${c.process || "?"}.${index ?? "?"}`,
        meta: c.retiring ? () => tr("app.board.retiring", { status: c.status }) : c.status,
        led: c.state,
        extra: t.div({ className: `block-body${c.retiring ? " retiring" : ""}` },
            c.cpu
                ? t.div({ className: "gauges" },
                    gauge("CPU", cpu, c.cpu),
                    gauge("MEM", mem, c.memory.split("/")[0].trim()),
                )
                : null,
            t.span({ className: "block-meta mono", textContent: truncateMiddle(c.image || "", 34), title: c.image }),
        ),
    })
}

function gauge(label, percent, text) {
    return t.div({ className: "gauge" },
        t.span({ className: "gauge-label", textContent: label }),
        t.span({ className: "gauge-track" },
            t.span({ className: "gauge-fill", style: `width: ${Math.min(100, Math.max(2, percent))}%` }),
        ),
        t.span({ className: "gauge-text mono", textContent: text }),
    )
}

function linkBlock(link, apps, wire) {
    const status = apps.find((a) => a.name === link.app)?.checks.status ?? "unknown"
    return block("service", wire, {
        title: link.app,
        meta: () => tr("app.board.via", { vars: link.vars.join(", ") }),
        href: appHref(link.app),
        led: status,
    })
}

function column(title, items) {
    return t.div({ className: "board-col" },
        t.span({ className: "board-col-title", textContent: title }),
        ...items,
    )
}

function empty(text) {
    return t.span({ className: "board-empty", textContent: text })
}

function board(app, detail, apps) {
    const live = detail.containers.filter((c) => !c.retiring)
    const retiring = detail.containers.filter((c) => c.retiring)
    // Os domínios ficam no cabeçalho; aqui entram só as portas expostas.
    const inputs = detail.ports.map((p) => {
        const [scheme, host, container] = p.split(":")
        return block("port", "in", { title: `:${host} → :${container}`, meta: scheme })
    })

    const outputs = [
        ...detail.services.map((l) => linkBlock(l, apps, "out")),
        ...detail.storage.map((m) => block("volume", "out", {
            title: m.container,
            meta: () => `${m.host.replace("/var/lib/dokku/data/storage/", "…/")}${m.readonly ? ` · ${tr("app.board.readonly")}` : ""}`,
        })),
    ]

    const wires = document.createElementNS(SVG, "svg")
    wires.setAttribute("class", "wires")

    const el = t.section({ className: "board" },
        wires,
        t.div({ className: "board-grid" },
            column(() => tr("app.board.ports"), inputs.length ? inputs : [empty(() => tr("app.board.ports.empty"))]),
            column(() => tr("app.board.machines"), [
                ...(live.length ? live.map(machine) : [empty(() => tr("app.board.machines.empty"))]),
                ...retiring.map(machine),
            ]),
            column(() => tr("app.board.outputs"), outputs.length ? outputs : [empty(() => tr("app.board.outputs.empty"))]),
        ),
        detail["used-by"].length
            ? t.div({ className: "board-usedby" },
                t.span({ className: "board-col-title", textContent: () => tr("app.board.usedBy") }),
                ...detail["used-by"].map((l) => linkBlock(l, apps, "")),
            )
            : null,
    )
    el.drawWires = () => drawWires(el, wires)
    return el
}

// Liga cada entrada às máquinas e cada máquina aos serviços/volumes com
// curvas.
function drawWires(boardEl, svg) {
    const base = boardEl.getBoundingClientRect()
    const rect = (el) => {
        const r = el.getBoundingClientRect()
        return { left: r.left - base.left, right: r.right - base.left, y: r.top - base.top + r.height / 2 }
    }
    const nodes = (wire) => [...boardEl.querySelectorAll(`[data-wire="${wire}"]`)].map(rect)
    const machines = nodes("machine")
    const paths = []
    const curve = (a, b) => {
        const mid = (a.x + b.x) / 2
        paths.push(`M${a.x} ${a.y} C${mid} ${a.y} ${mid} ${b.y} ${b.x} ${b.y}`)
    }
    // Em telas estreitas as colunas empilham; aí os fios não fazem sentido.
    if (machines.length && nodes("in")[0]?.right < machines[0].left) {
        for (const i of nodes("in")) for (const m of machines) curve({ x: i.right, y: i.y }, { x: m.left, y: m.y })
        for (const m of machines) for (const o of nodes("out")) curve({ x: m.right, y: m.y }, { x: o.left, y: o.y })
    } else if (machines.length && nodes("out")[0]?.left > machines[0].right) {
        for (const m of machines) for (const o of nodes("out")) curve({ x: m.right, y: m.y }, { x: o.left, y: o.y })
    }
    svg.setAttribute("viewBox", `0 0 ${base.width} ${base.height}`)
    svg.replaceChildren(...paths.map((d) => {
        const p = document.createElementNS(SVG, "path")
        p.setAttribute("d", d)
        return p
    }))
}

// ---------- abas ----------

// Descreve um registro de build no idioma ativo; nos deploys por imagem, diz
// qual imagem subiu (achada pelo commit do git:from-image feito durante o
// build).
function describeBuild(b, versions) {
    const start = new Date(b.started_at).getTime()
    const end = b.finished_at ? new Date(b.finished_at).getTime() : start
    const v = versions?.find((v) => {
        const at = new Date(v.date).getTime()
        return at >= start - 60_000 && at <= end + 60_000
    })
    switch (b.source) {
        case "git:from-image": return v?.image ? tr("app.build.source.image", { image: truncateMiddle(v.image, 56) }) : tr("app.build.source.imageAny")
        case "git-hook": return v ? tr("app.build.source.gitSha", { sha: v.sha.slice(0, 7) }) : tr("app.build.source.git")
        case "ps:rebuild": return tr("app.build.source.rebuild")
        case "ps:restart": return tr("app.build.source.restart")
        case "ps:start": return tr("app.build.source.start")
        case "deploy": return tr("app.build.source.deploy")
        default: return b.source || b.kind
    }
}

function history(app, detail, openLog, versions) {
    if (!detail.builds.length) return t.p({ className: "muted", textContent: () => tr("app.history.empty") })
    return t.ul({ className: "rows" },
        ...detail.builds.map((b) => t.li({},
            t.button({ type: "button", className: "row row-button", onclick: () => openLog(b) },
                t.span({ className: `dot dot-build-${b.exit_code === -1 ? "interrupted" : b.status}`, title: () => buildStatus(b) }),
                t.div({ className: "row-main" },
                    t.span({ className: "mono", title: b.source || b.kind, textContent: () => describeBuild(b, versions) }),
                    t.span({ className: "muted", textContent: () => `${buildStatus(b)} · ${timeAgo(b.started_at)} · ${fmtDateTime(new Date(b.started_at))}` }),
                ),
                t.span({ className: "row-side mono muted", textContent: duration(b) }),
            ),
        )),
    )
}

// Versões: as tags publicadas no registry da imagem da app, cada uma
// instalável (git:from-image). Busca ao abrir a aba.
function versionsPanel(name, data) {
    const base = `/api/apps/${encodeURIComponent(name)}`
    const ui = store({ rel: cached(`${base}/releases`) ?? null })
    const load = async () => {
        swr(`${base}/releases`).then((r) => ui.rel = r).catch((err) => ui.rel = { image: "?", releases: [], error: err.message })
    }
    const install = async (image) => {
        if (!await confirmDialog({
            title: tr("app.versions.install.confirm.title", { image: truncateMiddle(image, 60) }),
            text: tr("app.versions.install.confirm.text", { name }),
            confirm: tr("app.versions.install.submit"),
        })) return
        try {
            data.action = await api("POST", `${base}/install`, { image })
        } catch (err) {
            data.action = { name: "install", running: false, error: err.message }
        }
    }
    const busy = () => !!data.action?.running
    const installBtn = (image) => t.button({
        type: "button",
        className: "icon-btn icon-btn-install",
        title: () => tr("app.versions.install.submit"),
        "html-aria-label": () => tr("app.versions.install.submit"),
        disabled: busy,
        onclick: () => install(image),
    }, t.span({ className: "icon icon-install" }))
    const live = () => data.app?.["git-sha"]

    const release = (r) => {
        const current = r.image === live()
        return t.li({ rid: r.tag },
            t.div({ className: `row${current ? " version-current" : ""}` },
                t.span({ className: `dot ${current ? "dot-build-succeeded" : "dot-version"}` }),
                t.div({ className: "row-main" },
                    t.span({ className: "mono", textContent: r.tag }),
                    t.span({ className: "muted", textContent: () => r.updated ? tr("app.versions.published", { ago: timeAgo(r.updated), date: fmtDateTime(new Date(r.updated)) }) : truncateMiddle(r.image, 64) }),
                ),
                current ? t.span({ className: "tag tag-current", textContent: () => tr("app.versions.live") }) : installBtn(r.image),
            ),
        )
    }
    const skRows = (n) => skeleton(t.ul({ className: "rows" }, ...Array.from({ length: n }, (_, i) =>
        release({ tag: `${i + 1}.0.0`, image: "app/image:0.0.0", updated: new Date().toISOString() }))))

    const el = t.div({ className: "versions" },
        t.div({}, () => {
            const rel = ui.rel
            if (rel && !rel.image) return t.p({ className: "muted", textContent: () => tr("app.versions.notImage") })
            return t.section({ className: "versions-section" },
                t.h3({ className: "section-title" },
                    t.span({ textContent: () => tr("app.versions.title") }),
                    rel?.image ? t.span({ className: "muted mono", textContent: rel.image }) : null,
                ),
                !rel ? skRows(3)
                    : rel.error ? t.p({ className: "muted", textContent: () => tr("app.versions.error", { message: rel.error }) })
                    : !rel.releases.length ? t.p({ className: "muted", textContent: () => tr("app.versions.empty") })
                    : t.ul({ className: "rows rows-scroll" }, ...rel.releases.map(release)),
            )
        }),
    )
    el.start = load
    return el
}

// CRUD das variáveis. Os valores só são buscados quando a aba abre e ficam
// mascarados até clicar no olho. Salvar pode reiniciar a app (padrão) ou só
// gravar para o próximo deploy.
function secretsPanel(name, data, reload) {
    const ui = store({ values: null, shown: {}, editing: "", key: "", value: "", restart: true, busy: false, error: "" })
    const base = `/api/apps/${encodeURIComponent(name)}/config`

    const fetchValues = async () => {
        try { ui.values = await (await fetch(base, { cache: "no-store" })).json() } catch (err) { ui.error = err.message }
    }
    const edit = (key) => {
        ui.editing = key
        ui.key = key === "+" ? "" : key
        ui.value = key === "+" ? "" : ui.values?.[key] ?? ""
        ui.error = ""
        requestAnimationFrame(() => el.querySelector(".secret-form input:not([disabled])")?.focus())
    }
    const after = async (res) => {
        if (res?.running) data.action = res
        ui.editing = ""
        await Promise.all([fetchValues(), reload()])
    }
    const save = async (e) => {
        e.preventDefault()
        const key = ui.key.trim()
        if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) return ui.error = { key: "app.vars.error.invalidName" }
        if (ui.editing === "+" && ui.values && key in ui.values) return ui.error = { key: "app.vars.error.exists", params: { key } }
        ui.busy = true
        try {
            await after(await api("PUT", `${base}/${key}`, { value: ui.value, restart: ui.restart }))
        } catch (err) { ui.error = err.message } finally { ui.busy = false }
    }
    const remove = async (key) => {
        const restart = ui.restart
        if (!await confirmDialog({
            title: tr("app.vars.remove.confirm.title", { key }),
            text: restart ? tr("app.vars.remove.confirm.restart") : tr("app.vars.remove.confirm.noRestart"),
            confirm: tr("common.remove"),
            danger: true,
        })) return
        try {
            await after(await api("DELETE", `${base}/${key}${restart ? "?restart=1" : ""}`))
        } catch (err) { ui.error = err.message }
    }
    const copy = (key) => navigator.clipboard?.writeText(ui.values?.[key] ?? "")

    const form = () => t.form({ className: "secret-form", onsubmit: save },
        t.input({
            className: "input mono", placeholder: () => tr("app.vars.key.placeholder"), value: () => ui.key, disabled: ui.editing !== "+",
            autocomplete: "off", spellcheck: false, oninput: (e) => ui.key = e.target.value.toUpperCase(),
        }),
        t.textarea({
            className: "input mono", placeholder: () => tr("app.vars.value.placeholder"), rows: 1, value: () => ui.value, spellcheck: false,
            oninput: (e) => ui.value = e.target.value,
        }),
        t.div({ className: "secret-form-actions" },
            t.button({ type: "button", className: "btn", textContent: () => tr("common.cancel"), onclick: () => ui.editing = "" }),
            t.button({ type: "submit", className: "btn btn-primary", disabled: () => ui.busy, textContent: () => ui.busy ? tr("common.saving") : tr("common.save") }),
        ),
    )

    const el = t.div({ className: "secrets" },
        t.div({ className: "secrets-head" },
            t.label({ className: "check" },
                t.input({ type: "checkbox", checked: () => ui.restart, onchange: (e) => ui.restart = e.target.checked }),
                t.span({ textContent: () => tr("app.vars.restartOnSave") }),
            ),
            t.div({ className: "spacer" }),
            t.button({ type: "button", className: "btn btn-primary", onclick: () => edit("+"), disabled: () => !!data.action?.running },
                t.span({ className: "icon icon-plus" }), t.span({ textContent: () => tr("app.vars.new") })),
        ),
        t.p({ className: "form-error", hidden: () => !ui.error, textContent: () => errText(ui.error) }),
        t.div({}, () => ui.editing === "+" ? form() : null),
        t.ul({ className: "rows vars" }, () => {
            const detail = data.detail
            if (!detail) return skeleton(t.li({ className: "row" }, t.span({ className: "mono", textContent: "DATABASE_URL" })))
            const keys = detail["config-keys"]
            if (!keys.length) return t.li({ className: "muted empty-row", textContent: () => tr("app.vars.empty") })
            const linked = new Map(detail.services.flatMap((l) => l.vars.map((v) => [v, l.app])))
            return keys.map((k) => {
                if (ui.editing === k) return t.li({ className: "row row-editing" }, form())
                const shown = ui.shown[k] && ui.values
                return t.li({ className: "row secret-row" },
                    t.span({ className: "mono secret-key", textContent: k }),
                    linked.has(k) ? t.a({ className: "tag", href: appHref(linked.get(k)), textContent: `→ ${linked.get(k)}` }) : null,
                    t.span({ className: `mono secret-value${shown ? "" : " masked"}`, textContent: shown ? ui.values[k] ?? "" : "••••••••••••" }),
                    t.div({ className: "secret-tools" },
                        t.button({ type: "button", className: "icon-btn", title: () => shown ? tr("app.vars.hide.title") : tr("app.vars.show.title"), onclick: () => ui.shown = { ...ui.shown, [k]: !ui.shown[k] } },
                            t.span({ className: `icon ${shown ? "icon-eye-off" : "icon-eye"}` })),
                        t.button({ type: "button", className: "icon-btn", title: () => tr("app.vars.copy.title"), onclick: () => copy(k) }, t.span({ className: "icon icon-copy" })),
                        t.button({ type: "button", className: "icon-btn", title: () => tr("app.vars.edit.title"), onclick: () => edit(k) }, t.span({ className: "icon icon-edit" })),
                        t.button({ type: "button", className: "icon-btn icon-btn-danger", title: () => tr("common.remove"), onclick: () => remove(k) }, t.span({ className: "icon icon-trash" })),
                    ),
                )
            })
        }),
    )
    el.start = () => { if (!ui.values) fetchValues() }
    return el
}

// Remoção da app: o DELETE só agenda (202), então segura a tela com um modal
// de progresso e consulta a ação até terminar. Sucesso (ou a app sumir, 404)
// volta para a home com um aviso; erro aparece no próprio modal.
// Devolve detach(): ao sair da página o modal some, mas a consulta segue em
// segundo plano e o resultado vira só um aviso (sem mudar de página).
function removing(name, data) {
    const rm = store({ error: "", started: Date.now(), tick: 0 })
    let timer = null
    let poll = null
    let detached = false
    const stop = () => { clearInterval(timer); clearTimeout(poll); timer = poll = null }
    const hide = () => closeModal(dialog, { remove: true })
    const close = () => { stop(); hide() }
    const done = () => {
        close()
        if (detached) return toast(tr("app.remove.done", { name }))
        data.action = null
        navigate("/", { replace: true })
        toast(tr("app.remove.done", { name }))
    }
    const fail = (err) => {
        stop()
        if (detached) return toast(`${tr("app.remove.failed", { name })}: ${errText(err)}`, { kind: "error", ms: 8000 })
        rm.error = err
        data.action = { name: "destroy", running: false, error: errText(err) }
    }
    const check = async () => {
        poll = null
        try {
            const res = await fetch(`/api/apps/${encodeURIComponent(name)}`)
            if (res.status === 404) return done()
            const a = res.ok ? (await res.json()).action : null
            if (a?.name === "destroy" && !a.running) return a.error ? fail(a.error) : done()
        } catch { /* rede instável: tenta de novo */ }
        // Parado no meio da requisição (fechado/novo begin): não reagenda.
        if (timer) poll = setTimeout(check, 1500)
    }
    const begin = async () => {
        rm.error = ""
        rm.started = Date.now()
        stop()
        timer = setInterval(() => rm.tick++, 1000)
        try {
            data.action = await api("DELETE", `/api/apps/${encodeURIComponent(name)}`, { confirm: name })
            poll = setTimeout(check, 1000)
        } catch (err) {
            fail(err.message)
        }
    }
    const elapsed = () => {
        rm.tick
        const s = Math.max(0, Math.floor((Date.now() - rm.started) / 1000))
        return tr("app.remove.elapsed", { time: `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}` })
    }
    const dialog = t.dialog({ className: "modal remove-modal", "html-aria-busy": () => rm.error ? "false" : "true" },
        t.h2({ className: "modal-title" },
            () => rm.error ? null : t.span({ className: "spinner" }),
            t.span({ textContent: () => rm.error ? tr("app.remove.failed", { name }) : tr("app.remove.title", { name }) })),
        t.p({ className: () => rm.error ? "form-error" : "modal-text", textContent: () => rm.error ? errText(rm.error) : tr("app.remove.text") }),
        t.p({ className: "muted mono", hidden: () => !!rm.error, textContent: elapsed }),
        t.div({ className: "modal-actions", hidden: () => !rm.error },
            t.button({ type: "button", className: "btn", textContent: () => tr("common.close"), onclick: close }),
            t.button({ type: "button", className: "btn btn-danger", textContent: () => tr("app.remove.retry"), onclick: begin }),
        ),
    )
    // Esc não fecha enquanto a remoção roda.
    dialog.addEventListener("cancel", (e) => { e.preventDefault(); if (rm.error) close() })
    document.body.append(dialog)
    dialog.showModal()
    begin()
    return () => {
        detached = true
        hide()
        // Já terminou com erro: não há o que acompanhar.
        if (rm.error) stop()
    }
}

function settingsPanel(name, data) {
    let detach = null
    const destroy = async () => {
        if (!await confirmDialog({
            title: tr("app.settings.destroy.confirm.title", { name }),
            text: tr("app.settings.destroy.confirm.text"),
            confirm: tr("app.settings.destroy.submit"),
            danger: true,
            typed: name,
        })) return
        detach = removing(name, data)
    }
    const rn = store({ to: "", busy: false, error: "" })
    const rename = async (e) => {
        e.preventDefault()
        const to = rn.to.trim()
        if (!/^[a-z0-9][a-z0-9-]*$/.test(to)) return rn.error = { key: "app.settings.rename.error.invalid" }
        if (to === name) return rn.error = { key: "app.settings.rename.error.same" }
        if (!await confirmDialog({
            title: tr("app.settings.rename.confirm.title", { name, to }),
            text: tr("app.settings.rename.confirm.text"),
            confirm: tr("app.settings.rename.submit"),
        })) return
        rn.busy = true
        rn.error = ""
        try {
            await api("POST", `/api/apps/${encodeURIComponent(name)}/rename`, { to })
            // O rename roda em segundo plano: espera a app nova aparecer.
            for (let i = 0; i < 300; i++) {
                await new Promise((r) => setTimeout(r, 2000))
                if ((await fetch(`/api/apps/${encodeURIComponent(to)}`)).ok) {
                    navigate(`/apps/${encodeURIComponent(to)}`, { replace: true })
                    return
                }
                const old = await fetch(`/api/apps/${encodeURIComponent(name)}`).then((r) => r.ok ? r.json() : null).catch(() => null)
                if (old?.action?.name === "rename" && !old.action.running && old.action.error) throw new Error(old.action.error)
            }
            rn.error = { key: "app.settings.rename.error.timeout" }
        } catch (err) {
            rn.error = err.message
        } finally { rn.busy = false }
    }
    const el = t.div({ className: "settings" },
        t.section({ className: "settings-card" },
            t.h3({ textContent: () => tr("app.settings.rename.title") }),
            t.form({ className: "rename-form", onsubmit: rename },
                t.input({
                    className: "input mono",
                    placeholder: name,
                    value: () => rn.to,
                    oninput: (e) => { rn.to = e.target.value.toLowerCase(); rn.error = "" },
                    disabled: () => rn.busy,
                    "html-aria-label": () => tr("app.settings.rename.aria"),
                }),
                t.button({
                    type: "submit",
                    className: "btn",
                    disabled: () => rn.busy || !!data.action?.running || !rn.to.trim(),
                }, () => rn.busy ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("app.settings.rename.busy") })] : [t.span({ textContent: () => tr("app.settings.rename.submit") })]),
            ),
            t.p({ className: () => rn.error ? "form-error" : "muted", textContent: () => errText(rn.error) || tr("app.settings.rename.hint") }),
        ),
        t.section({ className: "danger-zone" },
            t.h3({ textContent: () => tr("app.settings.danger.title") }),
            t.div({ className: "danger-row" },
                t.div({},
                    t.strong({ textContent: () => tr("app.settings.destroy.title") }),
                    t.p({ className: "muted", textContent: () => tr("app.settings.destroy.text") }),
                ),
                t.button({ type: "button", className: "btn btn-danger", onclick: destroy, disabled: () => !!data.action?.running, textContent: () => tr("app.settings.destroy.submit") }),
            ),
        ),
    )
    // Saindo da página no meio da remoção: tira o modal, segue acompanhando.
    el.destroy = () => detach?.()
    return el
}

// Linha do `dokku logs`: "<timestamp> <app>[<processo>]: <mensagem>".
const LOG_LINE = /^(\d{4}-\d\d-\d\dT[\d:.]+Z)\s+[^\s\[]+\[([^\]]+)\]:\s?(.*)$/
const LOG_PAGE = 100

function parseLog(raw) {
    const m = LOG_LINE.exec(raw)
    const msg = m ? m[3] : raw
    let level = ""
    if (/\b(error|fatal|panic|exception|traceback)\b/i.test(msg) || /" 5\d\d /.test(msg)) level = "error"
    else if (/\bwarn(ing)?\b/i.test(msg) || /" 4\d\d /.test(msg)) level = "warn"
    if (raw.startsWith("[dokk]")) level = "system"
    return { time: m ? new Date(m[1]) : null, proc: m ? m[2] : "", msg, level }
}

function logRow(line) {
    return t.div({ className: `log-row${line.level ? ` log-${line.level}` : ""}` },
        t.time({
            className: "log-time",
            title: line.time ? fmtDateTime(line.time) : "",
            textContent: line.time ? fmtTime(line.time) : "",
        }),
        t.span({ className: "log-proc", textContent: line.proc }),
        t.span({ className: "log-msg", textContent: line.msg }),
    )
}

// Logs como lista plana paginada: guarda até MAX_LOG_LINES em memória, mas só
// põe no DOM as últimas `shown` (começa com uma página). Rolar até o topo
// carrega a página anterior; linhas novas entram animadas embaixo. Horários e
// textos são escritos direto no DOM: a troca de idioma reconstrói a janela
// visível (o buffer fica intacto).
function logsPanel(name, data) {
    let lines = []
    let shown = LOG_PAGE
    let pending = []
    let unseen = 0
    let events = null

    const list = t.div({ className: "log-list", role: "log" })
    const top = t.div({ className: "log-top" })
    const body = t.div({ className: "log-body" }, top, list)
    const jump = t.button({ type: "button", className: "log-jump", hidden: true, onclick: () => toBottom(true) })

    const atBottom = () => body.scrollHeight - body.scrollTop - body.clientHeight < 40
    const toBottom = (smooth) => {
        body.scrollTo({ top: body.scrollHeight, behavior: smooth ? "smooth" : "instant" })
        unseen = 0
        jump.hidden = true
    }
    const updateTop = () => {
        const older = lines.length - shown
        top.textContent = older > 0 ? trn("app.logs.older", older) : lines.length ? tr("app.logs.bufferStart") : ""
    }
    const updateJump = () => { jump.textContent = trn("app.logs.newLines", unseen) }

    // Reconstrói a lista inteira (abrir, limpar, reconectar).
    const render = () => {
        list.replaceChildren(...lines.slice(-shown).map(logRow))
        updateTop()
    }

    // Página anterior, mantendo o que está na tela no mesmo lugar.
    const loadOlder = () => {
        const visible = Math.min(shown, lines.length)
        if (visible >= lines.length) return
        const older = lines.slice(Math.max(0, lines.length - visible - LOG_PAGE), lines.length - visible)
        const height = body.scrollHeight
        list.prepend(...older.map(logRow))
        shown = visible + older.length
        body.scrollTop += body.scrollHeight - height
        updateTop()
    }
    new IntersectionObserver((entries) => entries[0].isIntersecting && lines.length && loadOlder(), { root: body }).observe(top)

    const flush = () => {
        if (!pending.length || data.paused) return
        const batch = pending.map(parseLog)
        pending = []
        const stick = atBottom()
        lines.push(...batch)
        if (lines.length > MAX_LOG_LINES) lines = lines.slice(-MAX_LOG_LINES)

        const fresh = batch.slice(-LOG_PAGE)
        const rows = fresh.map(logRow)
        list.append(...rows)
        if (stick) {
            // Grudado no fim: a janela continua com uma página só.
            shown = LOG_PAGE
            while (list.childElementCount > shown) list.firstElementChild.remove()
            toBottom(false)
        } else {
            shown = Math.min(shown + fresh.length, lines.length)
            unseen += batch.length
            updateJump()
            jump.hidden = false
        }
        // Só anima lotes pequenos (o tail inicial entra direto).
        if (rows.length <= 20 && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
            rows.forEach((row, i) => row.animate(
                [{ opacity: 0, transform: "translateY(6px)", background: "rgb(47 107 255 / .13)" }, { opacity: 1, transform: "none", background: "transparent" }],
                { duration: 450, delay: i * 25, easing: "cubic-bezier(.2, .8, .2, 1)", fill: "backwards" },
            ))
        }
        updateTop()
    }
    const timer = setInterval(flush, 150)
    body.addEventListener("scroll", () => { if (atBottom()) { unseen = 0; jump.hidden = true } }, { passive: true })

    const el = t.section({ className: "terminal" },
        t.header({ className: "terminal-head" },
            t.span({ className: () => `dot ${data.logsLive && !data.paused ? "dot-running dot-pulse" : "dot-exited"}` }),
            t.span({ className: "muted", textContent: () => data.paused ? tr("app.logs.paused") : data.logsLive ? tr("app.logs.live") : tr("app.logs.connecting") }),
            t.div({ className: "spacer" }),
            t.button({
                type: "button",
                className: "ghost",
                textContent: () => data.paused ? tr("app.logs.resume") : tr("app.logs.pause"),
                onclick: () => { data.paused = !data.paused; flush() },
            }),
            t.button({ type: "button", className: "ghost", textContent: () => tr("app.logs.clear"), onclick: () => { lines = []; shown = LOG_PAGE; render() } }),
        ),
        body,
        jump,
    )
    el.start = () => {
        if (events) return
        events = new EventSource(`/api/apps/${encodeURIComponent(name)}/logs`)
        events.onopen = () => {
            // Ao (re)conectar o servidor reenvia as últimas linhas.
            lines = []
            pending = []
            shown = LOG_PAGE
            render()
            data.logsLive = true
        }
        events.onerror = () => data.logsLive = false
        events.onmessage = (e) => pending.push(JSON.parse(e.data))
        events.addEventListener("failure", (e) => pending.push(`[dokk] ${JSON.parse(e.data)}`))
    }
    // Troca de idioma: refaz a janela visível e os textos imperativos.
    const langWatch = watch(() => i18n.lang, () => {
        if (lines.length) render()
        if (!jump.hidden) updateJump()
    })
    el.destroy = () => { events?.close(); clearInterval(timer); langWatch.unwatch() }
    return el
}

function buildLogDialog() {
    const body = t.pre({ className: "terminal-body" })
    const title = t.h2({ className: "panel-title" })
    // Build aberto e estado do corpo, para refazer os textos na troca de idioma.
    let current = null
    let state = ""
    const paint = () => {
        if (current) title.textContent = `${current.source || current.kind} · ${fmtDateTime(new Date(current.started_at))}`
        if (state === "loading") body.textContent = tr("common.loading")
        else if (state === "missing") body.textContent = tr("app.logDialog.notFound")
    }
    const langWatch = watch(() => i18n.lang, () => paint())
    const dialog = t.dialog({ className: "dialog" },
        t.header({ className: "terminal-head" },
            title,
            t.div({ className: "spacer" }),
            t.button({ type: "button", className: "ghost", textContent: () => tr("common.close"), onclick: () => closeModal(dialog) }),
        ),
        body,
    )
    // Clique no backdrop fecha.
    dialog.addEventListener("click", (e) => e.target === dialog && closeModal(dialog))
    dialog.addEventListener("cancel", (e) => { e.preventDefault(); closeModal(dialog) })

    dialog.openBuild = async (app, build) => {
        current = build
        state = "loading"
        paint()
        dialog.showModal()
        try {
            const res = await fetch(`/api/apps/${encodeURIComponent(app)}/builds/${build.id}/log`)
            state = res.ok ? "" : "missing"
            if (res.ok) body.textContent = await res.text()
            else paint()
            body.scrollTop = body.scrollHeight
        } catch (err) {
            state = ""
            body.textContent = err.message
        }
    }
    dialog.destroy = () => langWatch.unwatch()
    return dialog
}

// Dados de mentira com a forma do detalhe real, para o skeleton.
function fakeDetail() {
    const now = new Date().toISOString()
    const build = (source) => ({ id: source, source, status: "succeeded", started_at: now, finished_at: now, exit_code: 0 })
    return {
        containers: [{ name: "app.web.1", process: "web", state: "running", status: "Up 2 days", image: "dokku/app:latest", cpu: "0.01%", memory: "50MiB / 4GiB", "mem-perc": "1%" }],
        ports: ["http:80:80", "https:443:80"],
        storage: [{ host: "/var/lib/dokku/data/storage/app/data", container: "/data" }],
        ssl: { enabled: true, "expires-at": now },
        "config-keys": ["DATABASE_URL", "SECRET_KEY", "PORT"],
        builds: [build("git:from-image"), build("ps:rebuild"), build("ps:restart")],
        services: [{ app: "app-db", vars: ["DB_HOST"] }],
        "used-by": [],
    }
}

export function appDetail(route) {
    const name = route.params.name
    const data = store({ apps: [], allApps: [], app: null, detail: null, error: "", live: false, logsLive: false, paused: false, tab: "history", action: null, versions: null })

    // SWR: o que veio do preload/visita anterior aparece na hora; a busca
    // nova só atualiza por cima (requisições simultâneas são compartilhadas).
    const url = `/api/apps/${encodeURIComponent(name)}`
    const apply = (body) => {
        data.app = body.app
        data.apps = [body.app]
        data.detail = body.detail
        data.action = body.action
        data.error = ""
    }
    const hit = cached(url)
    if (hit) apply(hit)
    const all = cached("/api/apps")
    if (all) data.allApps = all

    data.versions = cached(`${url}/versions`) ?? null
    async function loadDetail() {
        swr(`${url}/versions`).then((v) => data.versions = v).catch(() => {})
        try {
            apply(await swr(url))
        } catch (err) {
            data.error = err.message
        }
    }
    loadDetail()

    // A cada coleta o resumo vem pelo stream e os detalhes são recarregados.
    const close = subscribeApps(data, (apps) => {
        mutate("/api/apps", apps)
        data.allApps = apps
        const app = apps.find((a) => a.name === name)
        if (app) {
            data.app = app
            // O badge "Em tempo real" aqui reflete só esta app.
            data.apps = [app]
        }
        loadDetail()
    })

    const logs = logsPanel(name, data)
    const dialog = buildLogDialog()
    const secrets = secretsPanel(name, data, () => loadDetail())
    const versions = versionsPanel(name, data)
    const settings = settingsPanel(name, data)
    // Troca de aba: o indicador desliza até a aba nova e o conteúdo entra
    // deslizando do lado em que a aba está (direita se ela vem depois).
    const tabIndex = (key) => TABS.findIndex((tab) => tab.key === key)
    const moveIndicator = () => {
        const active = tabs.querySelector(".tab.active")
        if (!active) return
        tabs.style.setProperty("--x", `${active.offsetLeft}px`)
        tabs.style.setProperty("--w", `${active.offsetWidth}px`)
    }
    const openTab = (key) => {
        if (data.tab === key) return
        const dir = Math.sign(tabIndex(key) - tabIndex(data.tab))
        data.tab = key
        if (key === "logs") logs.start()
        if (key === "vars") secrets.start()
        if (key === "versions") versions.start()
        requestAnimationFrame(() => {
            moveIndicator()
            if (matchMedia("(prefers-reduced-motion: reduce)").matches) return
            panels[key].animate(
                [{ opacity: 0, transform: `translateX(${dir * 24}px)` }, { opacity: 1, transform: "none" }],
                { duration: 280, easing: "cubic-bezier(.2, .8, .2, 1)" },
            )
        })
    }

    const tabs = t.nav({ className: "tabs", role: "tablist", onmount: () => requestAnimationFrame(moveIndicator) },
        ...TABS.map((tab) => t.button({
            type: "button",
            role: "tab",
            className: () => `tab${data.tab === tab.key ? " active" : ""}`,
            "html-aria-selected": () => String(data.tab === tab.key),
            onclick: () => openTab(tab.key),
        },
            t.span({ className: `tab-icon ${tab.icon}` }, t.span({ className: "icon" })),
            t.span({ textContent: () => tab.label }),
        )),
        t.span({ className: "tab-indicator" }),
    )
    const panels = {
        history: t.div({ className: "tab-panel", hidden: () => data.tab !== "history" }, () =>
            data.detail ? history(name, data.detail, (b) => dialog.openBuild(name, b), data.versions) : skeleton(history(name, fakeDetail(), () => {}))),
        versions: t.div({ className: "tab-panel", hidden: () => data.tab !== "versions" }, versions),
        logs: t.div({ className: "tab-panel", hidden: () => data.tab !== "logs" }, logs),
        vars: t.div({ className: "tab-panel", hidden: () => data.tab !== "vars" }, secrets),
        settings: t.div({ className: "tab-panel", hidden: () => data.tab !== "settings" }, settings),
    }

    // Redesenha os fios quando a board muda de tamanho.
    let currentBoard = null
    const resize = new ResizeObserver(() => { currentBoard?.drawWires(); moveIndicator() })

    const page = t.main({ className: "page page-app" },
        t.header({ className: "app-head" },
            t.a({ className: "back", href: "/", title: () => tr("app.back"), "html-aria-label": () => tr("app.back") },
                t.span({ className: "icon icon-back" })),
            t.h1({ className: "app-title", textContent: name }),
            t.span({ className: "app-head-badges" }, () => [data.app ? statusBadge(data.app.checks.status) : skeleton(statusBadge("healthy"))]),
            liveBadge(data),
            t.div({ className: "spacer" }),
            appActions(name, data),
        ),
        // Domínios agrupados logo abaixo do título (o primeiro + "+N").
        t.div({ className: "app-meta" }, () => {
            if (!data.app) return [skeleton(t.span({ className: "app-meta-sk" }, ...domains(fakeApp(0))))]
            const ssl = data.detail?.ssl
            return [
                ...domains(data.app),
                sslBadge(name, data),
            ]
        }),
        t.div({ className: "app-health" }, () => [data.app ? heatmap(data.app) : skeleton(heatmap(fakeApp()))]),
        t.div({}, () => {
            if (data.error && !data.detail) return t.p({ className: "muted", textContent: () => tr("common.error", { message: data.error }) })
            const loadingBoard = !data.app || !data.detail
            const b = loadingBoard ? board(fakeApp(), fakeDetail(), []) : board(data.app, data.detail, data.allApps)
            if (loadingBoard) skeleton(b)
            if (currentBoard) resize.unobserve(currentBoard)
            currentBoard = b
            resize.observe(b)
            requestAnimationFrame(() => b.drawWires())
            return b
        }),
        tabs,
        panels.history,
        panels.versions,
        panels.logs,
        panels.vars,
        panels.settings,
        dialog,
    )
    // Os rótulos das abas mudam de largura com o idioma: reposiciona o indicador.
    const langWatch = watch(() => i18n.lang, () => requestAnimationFrame(moveIndicator))
    page.destroy = () => { close(); logs.destroy(); dialog.destroy(); settings.destroy(); resize.disconnect(); langWatch.unwatch() }
    return page
}
