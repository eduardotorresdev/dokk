import { t, store } from "shablon"
import { LANGS, fmtRelative, i18n, setLang, tr } from "./i18n.js"

const STATUSES = ["healthy", "degraded", "unhealthy", "starting", "restarting", "removing", "suspended", "unknown"]

// Navega dentro da UI sem recarregar a página (URLs limpas, sem "#/"): usa
// a Navigation API, que o router intercepta; sem ela (navegador antigo e sem
// o shim de main.js), cai para uma navegação comum. `replace` troca a entrada
// atual do histórico em vez de empilhar uma nova (redirects).
export function navigate(path, { replace = false } = {}) {
    if (window.navigation) {
        navigation.navigate(path, { history: replace ? "replace" : "auto" })
    } else if (replace) {
        location.replace(path)
    } else {
        location.assign(path)
    }
}

// Rótulo do status no idioma ativo; leia dentro de um binding.
export function statusLabel(status) {
    return tr(`common.status.${status}`)
}

// Obsoleto: use statusLabel(). Mantido só até as páginas migrarem.
export const STATUS_LABEL = Object.defineProperties({}, Object.fromEntries(
    STATUSES.map((s) => [s, { enumerable: true, get: () => statusLabel(s) }])))

// Estados de transição (somados num só bloco no resumo da home).
export const TRANSITIONS = ["starting", "restarting", "removing"]

// "há 3 min" no idioma ativo; use dentro de um binding.
export function timeAgo(iso) {
    return fmtRelative(iso)
}

const MAX_VERSION = 22
export const HISTORY_SIZE = 24 // igual a dokku.HistorySize

// Encurta pelo meio para manter o começo (registry/repo) e o fim (tag).
export function truncateMiddle(text, max) {
    if (text.length <= max) return text
    const keep = max - 1
    return text.slice(0, Math.ceil(keep / 2)) + "…" + text.slice(-Math.floor(keep / 2))
}

// Rótulo de cada ação em segundo plano enquanto roda.
const ACTIONS = ["start", "stop", "restart", "config", "destroy", "rename", "install", "deploy", "ssl-renew"]

export function actionRunningLabel(name) {
    return ACTIONS.includes(name) ? tr(`common.action.running.${name}`) : tr("common.action.running.default")
}

// Obsoleto: use actionRunningLabel(). Mantido só até as páginas migrarem.
export const ACTION_RUNNING = Object.defineProperties({}, Object.fromEntries(
    ACTIONS.map((a) => [a, { enumerable: true, get: () => actionRunningLabel(a) }])))

// Chamada que muda estado: manda o cabeçalho que o servidor exige
// (proteção contra CSRF) e devolve o corpo ou lança o erro da API.
export async function api(method, path, body) {
    const res = await fetch(path, {
        method,
        headers: { "X-Dokk": "1", ...(body ? { "Content-Type": "application/json" } : {}) },
        body: body ? JSON.stringify(body) : undefined,
    })
    const out = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(out.error || res.statusText)
    return out
}

// Modal de confirmação. Com `typed`, o botão só libera quando o texto
// digitado for igual (usado para remover a app).
// Fecha um <dialog> com a animação de saída (.closing) antes do close();
// remove: tira do DOM depois. Sem animação, fecha na hora.
export function closeModal(dialog, { remove = false } = {}) {
    const end = () => {
        dialog.classList.remove("closing")
        if (dialog.open) dialog.close()
        if (remove) dialog.remove()
    }
    if (!dialog.open || matchMedia("(prefers-reduced-motion: reduce)").matches) return end()
    if (dialog.classList.contains("closing")) return
    dialog.classList.add("closing")
    let ended = false
    const once = () => { if (!ended) { ended = true; end() } }
    dialog.addEventListener("animationend", once, { once: true })
    setTimeout(once, 300) // sem animationend (aba em segundo plano): fecha igual
}

export function confirmDialog({ title, text, confirm, danger = false, typed = "" }) {
    return new Promise((resolve) => {
        const input = typed ? t.input({ className: "input mono", placeholder: typed, autocomplete: "off", spellcheck: false }) : null
        const ok = t.button({ type: "submit", className: danger ? "btn btn-danger" : "btn btn-primary", textContent: confirm, disabled: !!typed })
        input?.addEventListener("input", () => ok.disabled = input.value !== typed)
        const dialog = t.dialog({ className: "modal" },
            t.form({ method: "dialog", onsubmit: () => done(true) },
                t.h2({ className: "modal-title", textContent: title }),
                t.p({ className: "modal-text", textContent: text }),
                typed ? t.label({ className: "field" }, t.span({ textContent: tr("common.confirm.typed", { name: typed }) }), input) : null,
                t.div({ className: "modal-actions" },
                    t.button({ type: "button", className: "btn", textContent: tr("common.cancel"), onclick: () => done(false) }),
                    ok,
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
        dialog.addEventListener("cancel", (e) => { e.preventDefault(); done(false) })
        dialog.addEventListener("click", (e) => e.target === dialog && done(false))
        document.body.append(dialog)
        dialog.showModal()
        ;(input || ok).focus()
    })
}

// Aviso rápido no canto da tela. Fica no body, então sobrevive à troca de
// rota (ex.: "app removida" mostrado já na home).
export function toast(text, { kind = "ok", ms = 4000 } = {}) {
    let box = document.querySelector(".toasts")
    if (!box) {
        box = t.div({ className: "toasts", "html-role": "status", "html-aria-live": "polite" })
        document.body.append(box)
    }
    const el = t.div({ className: `toast toast-${kind}`, textContent: text })
    box.append(el)
    setTimeout(() => {
        el.classList.add("toast-out")
        setTimeout(() => el.remove(), 300)
    }, ms)
    return el
}

// Popover aberto no momento: só um por vez, fecha ao clicar fora ou no Esc.
let openPanel = null

function closePanel() {
    if (!openPanel) return
    openPanel.hidden = true
    openPanel = null
}

document.addEventListener("pointerdown", (e) => {
    if (openPanel && !openPanel.contains(e.target) && !openPanel.owner.contains(e.target)) closePanel()
})
document.addEventListener("keydown", (e) => e.key === "Escape" && closePanel())
window.addEventListener("scroll", closePanel, { passive: true })

// Botão que abre um painel flutuante logo abaixo dele.
export function popover(button, panel) {
    panel.hidden = true
    panel.owner = button
    button.addEventListener("click", () => {
        const wasOpen = openPanel === panel
        closePanel()
        if (wasOpen) return
        panel.hidden = false
        const r = button.getBoundingClientRect()
        panel.style.top = `${r.bottom + 6}px`
        panel.style.left = `${Math.max(16, Math.min(r.left, window.innerWidth - 16 - panel.offsetWidth))}px`
        openPanel = panel
    })
    return [button, panel]
}

export function domainLink(app, d) {
    return t.a({
        className: `domain ${app.ssl ? "domain-ssl" : "domain-insecure"}`,
        href: `${app.ssl ? "https" : "http"}://${d}`,
        target: "_blank",
        rel: "noopener",
        title: () => app.ssl ? "HTTPS" : tr("common.domain.noSsl"),
    },
        t.span({ className: "icon domain-icon" }),
        t.span({ className: "domain-name", textContent: d }),
        t.span({ className: "icon domain-arrow" }),
    )
}

export function domains(app) {
    if (!app.domains.length) return [t.span({ className: "domain domain-empty", textContent: () => tr("common.domain.none") })]
    const [first, ...rest] = app.domains
    if (!rest.length) return [domainLink(app, first)]
    return [
        domainLink(app, first),
        ...popover(
            t.button({ type: "button", className: "chip", textContent: `+${rest.length}`, title: () => tr("common.domain.all.title") }),
            t.div({ className: "pop pop-list" }, ...rest.map((d) => domainLink(app, d))),
        ),
    ]
}

export function version(text) {
    if (!text) return t.span({ textContent: "—" })
    if (text.length <= MAX_VERSION) return t.span({ textContent: text })
    return t.span({},
        ...popover(
            t.button({ type: "button", className: "version", textContent: truncateMiddle(text, MAX_VERSION), title: () => tr("common.version.full.title") }),
            t.div({ className: "pop pop-version" }, t.code({ textContent: text })),
        ),
    )
}

// Pior status entre as apps com deploy: é o que a onda representa.
export function overallHealth(apps) {
    const statuses = apps.map((a) => a.checks.status)
    for (const s of ["unhealthy", "degraded"]) {
        if (statuses.includes(s)) return s
    }
    if (statuses.some((s) => TRANSITIONS.includes(s))) return "transition"
    return statuses.includes("healthy") ? "healthy" : "unknown"
}

// Resumo da onda: common.wave.<estado> (healthy, degraded, unhealthy, transition, unknown).
const WAVE_LABEL = {
    get healthy() { return tr("common.wave.healthy") },
    get degraded() { return tr("common.wave.degraded") },
    get unhealthy() { return tr("common.wave.unhealthy") },
    get transition() { return tr("common.wave.transition") },
    get unknown() { return tr("common.wave.unknown") },
}

const SVG = "http://www.w3.org/2000/svg"

// Onda senoidal repetida duas vezes para o loop de translateX não ter emenda.
export function wave() {
    const svg = document.createElementNS(SVG, "svg")
    svg.setAttribute("viewBox", "0 0 24 16")
    svg.setAttribute("class", "wave")
    svg.setAttribute("aria-hidden", "true")
    const path = document.createElementNS(SVG, "path")
    path.setAttribute("d", "M0 8 Q6 2 12 8 T24 8 T36 8 T48 8 T60 8 T72 8 T84 8 T96 8")
    svg.append(path)
    return svg
}

export function liveBadge(data) {
    const state = () => data.live ? overallHealth(data.apps) : "offline"
    return t.span({
        className: () => `live live-${state()}`,
        title: () => data.live ? tr("common.live.title", { state: WAVE_LABEL[overallHealth(data.apps)] }) : tr("common.live.offline.title"),
    },
        wave(),
        t.span({ textContent: () => data.live ? tr("common.live.on") : tr("common.live.reconnecting") }),
    )
}

export function heatmap(app) {
    return t.div({ className: "heatmap", title: () => tr("common.heatmap.title", { count: HISTORY_SIZE }) },
        ...app.checks.history.map((h) => t.span({ className: `cell cell-${h}`, title: () => statusLabel(h) })),
        // Espaços ainda sem check, em cinza, para mostrar quanto falta preencher.
        ...Array.from({ length: Math.max(0, HISTORY_SIZE - app.checks.history.length) }, () =>
            t.span({ className: "cell cell-empty", title: () => tr("common.heatmap.waiting.title") })),
    )
}

export function statusBadge(status) {
    return t.span({ className: `badge badge-${status}`, textContent: () => statusLabel(status) })
}

// Assina o stream de apps do servidor (uma mensagem por coleta). Devolve a
// função que fecha a conexão.
export function subscribeApps(data, onApps) {
    const events = new EventSource("/api/events")
    events.onopen = () => data.live = true
    events.onerror = () => {
        data.live = false
        // Sessão expirada: o stream falha com 401; manda para o login.
        fetch("/api/session").then((r) => { if (r.status === 401) navigate("/login", { replace: true }) }).catch(() => {})
    }
    events.addEventListener("apps", (e) => {
        data.live = true
        onApps(JSON.parse(e.data))
    })
    events.addEventListener("failure", (e) => data.error = JSON.parse(e.data).error)
    return () => events.close()
}

// ---------- skeleton ----------

// Transforma um elemento renderizado com dados de mentira em skeleton: o
// layout é o do componente real (1:1) e o CSS troca o texto por shimmer.
export function skeleton(el) {
    el.classList.add("skeleton")
    el.setAttribute("aria-busy", "true")
    el.inert = true
    return el
}

const FAKE_NAMES = ["portile-app", "mdm-db", "pbs-front-qa", "nightwatcher", "edge", "pbs-back"]

export function fakeApp(i = 0) {
    return {
        name: FAKE_NAMES[i % FAKE_NAMES.length],
        deployed: true,
        locked: false,
        "git-sha": "ghcr.io/acme/app:1.0",
        "last-deploy-at": null,
        domains: i % 2 ? [] : ["app.portile.com.br"],
        ssl: true,
        processes: { web: { running: 1, total: 1 } },
        checks: { status: "healthy", history: [] },
    }
}

// ---------- SWR mínimo ----------

// Cache em memória das respostas GET da API (stale-while-revalidate): a tela
// mostra na hora o que já tem (cached) e pede a versão nova em paralelo
// (swr). Requisições iguais em andamento são compartilhadas.
const cache = new Map() // url → { data, at, promise }

export function cached(url) {
    return cache.get(url)?.data
}

// Grava um dado vindo de outro lugar (ex.: stream SSE) no cache.
export function mutate(url, data) {
    cache.set(url, { ...cache.get(url), data, at: Date.now() })
}

// Busca a URL. Com maxAge, reaproveita o cache se ele for mais novo que isso
// (o preload no hover usa para não repetir a mesma busca).
export function swr(url, { maxAge = 0 } = {}) {
    const entry = cache.get(url)
    if (entry?.promise) return entry.promise
    if (entry?.data !== undefined && Date.now() - entry.at < maxAge) return Promise.resolve(entry.data)
    const promise = fetch(url)
        .then(async (res) => {
            const body = await res.json()
            if (!res.ok) throw new Error(body.error || res.statusText)
            mutate(url, body)
            return body
        })
        .finally(() => {
            const e = cache.get(url)
            if (e) e.promise = null
        })
    cache.set(url, { ...entry, promise })
    return promise
}

export function prefetch(url) {
    swr(url, { maxAge: 10_000 }).catch(() => {})
}

// ---------- número em roleta ----------

// Número estilo odômetro: cada dígito é uma fita de 0 a 9 que rola na
// vertical até o valor novo, com um leve overshoot de mola (no CSS).
export function rollingNumber() {
    const el = document.createElement("span")
    el.className = "roll"
    let shape = ""
    let strips = []

    el.set = (text) => {
        text = String(text)
        // Dígitos viram fitas; o resto (vírgula, sinal) fica fixo. Se o
        // "formato" mudar (ex.: 9 → 10), remonta, partindo dos dígitos atuais.
        const nextShape = text.replace(/\d/g, "0")
        if (nextShape !== shape) {
            const old = strips.map((s) => s.digit)
            el.replaceChildren()
            strips = []
            for (const ch of text) {
                if (!/\d/.test(ch)) {
                    el.append(Object.assign(document.createElement("span"), { className: "roll-char", textContent: ch }))
                    continue
                }
                const box = Object.assign(document.createElement("span"), { className: "roll-digit" })
                const strip = Object.assign(document.createElement("span"), { className: "roll-strip" })
                for (let d = 0; d <= 9; d++) strip.append(Object.assign(document.createElement("span"), { textContent: String(d) }))
                box.append(strip)
                el.append(box)
                const from = old.shift() ?? 0
                strip.style.transition = "none"
                strip.style.transform = `translateY(${-from * 10}%)`
                strips.push({ strip, digit: from })
            }
            shape = nextShape
            el.setAttribute("aria-label", text)
            // Força o layout antes de ligar a transição de volta.
            void el.offsetWidth
            for (const s of strips) s.strip.style.transition = ""
        }
        let i = 0
        for (const ch of text) {
            if (!/\d/.test(ch)) continue
            const s = strips[i++]
            s.digit = Number(ch)
            s.strip.style.transform = `translateY(${-s.digit * 10}%)`
        }
        el.setAttribute("aria-label", text)
    }
    return el
}

// Troca o rótulo do título da aba da página atual ("" volta ao da rota).
export function setPageTitle(label) {
    i18n.pageTitle = label
}

// ---------- marca ----------

// Logo: uma doca. Um container pousado no píer, com estacas e água embaixo.
// Cada parte é um <g> com classe própria (logo-box, logo-deck, logo-posts,
// logo-water) para animar com CSS/motion sem mexer no desenho; a água tem o
// dobro da largura para poder deslizar em loop.
const LOGO_SVG = `<svg viewBox="0 0 40 40" aria-hidden="true">
<defs>
<linearGradient id="dokk-g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#6b9bff"/><stop offset="1" stop-color="#1f4fe0"/></linearGradient>
<clipPath id="dokk-clip"><rect width="40" height="40" rx="11"/></clipPath>
</defs>
<g clip-path="url(#dokk-clip)">
<rect class="logo-bg" width="40" height="40" fill="url(#dokk-g)"/>
<g class="logo-water" fill="none" stroke="#fff" stroke-width="1.6" stroke-linecap="round">
<path stroke-opacity=".75" d="M-40 32.5q2.5-2 5 0t5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0"/>
<path stroke-opacity=".4" d="M-37.5 36.5q2.5-2 5 0t5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0"/>
</g>
<g class="logo-posts" fill="#fff" fill-opacity=".7"><rect x="10" y="24" width="2.6" height="7.5" rx="1"/><rect x="27.4" y="24" width="2.6" height="7.5" rx="1"/></g>
</g>
<g class="logo-dock">
<rect class="logo-deck" x="6" y="21.5" width="28" height="3.4" rx="1.7" fill="#fff"/>
<g class="logo-box">
<rect x="11.5" y="9" width="17" height="11" rx="1.8" fill="#fff"/>
<path d="M16.2 11.5v6M20 11.5v6M23.8 11.5v6" stroke="#1f4fe0" stroke-opacity=".55" stroke-width="1.4" stroke-linecap="round"/>
</g>
</g>
</svg>`

// animated: o container desce até o píer e a água corre (ver .logo-animated).
export function logo(size = 44, { animated = false, loop = false } = {}) {
    const el = document.createElement("span")
    el.className = `logo${animated ? " logo-animated" : ""}${loop ? " logo-loop" : ""}`
    el.style.width = el.style.height = `${size}px`
    el.innerHTML = LOGO_SVG
    return el
}

// Marca completa: logo + "dokk".
export function brand(host = "", { animated = false, loop = false, extra = null } = {}) {
    return t.div({ className: "brand" },
        // "/" leva à home; deslogado, o guard do router manda para o login.
        t.a({ className: "brand-link", href: "/", "html-aria-label": () => tr("common.brand.home.aria") },
            logo(44, { animated, loop }),
            t.span({ className: "brand-name", textContent: "dokk" })),
        host ? t.span({ className: "brand-host", textContent: host }) : null,
        extra,
    )
}

// ---------- sessão ----------

// Usuário logado: avatar com a inicial do nome ("Administrador" por padrão);
// clicar abre um menu com nome, e-mail, idioma e "Sair".
export function userMenu() {
    // Sem nome salvo, o padrão segue o idioma ativo.
    const user = store({ name: "" })
    const display = () => user.name || tr("common.user.defaultName")
    const initial = t.span({ className: "avatar", textContent: () => display()[0].toUpperCase() })
    const name = t.strong({ className: "user-name", textContent: display })
    const email = t.span({ className: "user-email mono", textContent: "" })
    fetch("/api/session").then((r) => r.ok ? r.json() : null).then((s) => {
        if (!s) return
        user.name = s.name || ""
        email.textContent = s.email || ""
    }).catch(() => {})
    const button = t.button({ type: "button", className: "user-button", title: display, "html-aria-label": () => tr("common.user.account.aria") },
        initial,
        t.span({ className: "icon user-caret" }),
    )
    const panel = t.div({ className: "pop user-pop" },
        t.div({ className: "user-head" }, name, email),
        t.div({ className: "user-item user-lang" }, langSelect()),
        t.a({ className: "user-item user-item-link", href: "/onboarding", onclick: closePanel },
            t.span({ className: "icon icon-settings" }),
            t.span({ textContent: () => tr("common.user.wizard") }),
        ),
        t.button({ type: "button", className: "user-item", onclick: logout },
            t.span({ className: "icon icon-logout" }),
            t.span({ textContent: () => tr("common.user.logout") }),
        ),
    )
    const [b, p] = popover(button, panel)
    // Alinha o menu pela direita do avatar.
    button.addEventListener("click", () => {
        if (panel.hidden) return
        const r = button.getBoundingClientRect()
        panel.style.left = `${Math.max(16, r.right - panel.offsetWidth)}px`
    })
    return t.div({ className: "user-menu" }, b, p)
}

// Navegação do header. "Apps" fica ativo na home e nas páginas de app.
export function headerNav() {
    const items = [
        { href: "/", label: () => tr("common.nav.apps"), match: (p) => p === "/" || p.startsWith("/apps/") },
        { href: "/registries", label: () => tr("common.nav.registries"), match: (p) => p.startsWith("/registries") },
    ]
    const links = items.map((i) => t.a({ href: i.href, textContent: i.label }))
    const sync = () => items.forEach((i, n) => links[n].classList.toggle("active", i.match(location.pathname)))
    // Troca de rota (real ou pelo shim de main.js) dispara currententrychange.
    window.navigation?.addEventListener("currententrychange", sync)
    sync()
    return t.nav({ className: "header-nav" }, ...links)
}

// Seletor de idioma (select nativo). Os nomes dos idiomas ficam no próprio
// idioma; a troca vale na hora e tenta salvar no servidor.
export function langSelect() {
    return t.label({ className: "lang-select" },
        t.span({ className: "icon icon-globe", "html-aria-hidden": "true" }),
        t.select({ "html-aria-label": () => tr("common.lang.label"), onchange: (e) => setLang(e.target.value) },
            ...LANGS.map((l) => t.option({ value: l.code, textContent: l.label, selected: () => i18n.lang === l.code }))),
    )
}

// Sai da conta e volta para o login.
export async function logout() {
    await fetch("/api/logout", { method: "POST", headers: { "X-Dokk": "1" } }).catch(() => {})
    navigate("/login")
}
