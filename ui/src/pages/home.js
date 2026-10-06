import { t, store, watch } from "shablon"
import { statusLabel, rollingNumber, cached, domains, mutate, navigate, prefetch, swr, fakeApp, heatmap, liveBadge, skeleton, statusBadge, subscribeApps, timeAgo, version, actionRunningLabel } from "../shared.js"
import { fmtNumber, i18n, register, tr } from "../i18n.js"
import en from "../locales/en/home.js"
import es from "../locales/es/home.js"
import ptBR from "../locales/pt-BR/home.js"

register({ en, es, "pt-BR": ptBR })

// Ordem da lista: problemas primeiro, depois transições, pausadas e por fim
// as saudáveis. A grade preenche da esquerda pra direita, de cima pra baixo.
const STATUS_ORDER = ["unhealthy", "degraded", "removing", "restarting", "starting", "suspended", "healthy", "unknown"]

// Ordenações do select (a de status é a da própria lista).
const SORTS = {
    status: { get label() { return tr("home.sort.status") } },
    name: { get label() { return tr("home.sort.name") }, fn: (a, b) => a.name.localeCompare(b.name) },
    deploy: { get label() { return tr("home.sort.deploy") }, fn: (a, b) => new Date(b["last-deploy-at"] || 0) - new Date(a["last-deploy-at"] || 0) },
}

function sortApps(apps) {
    const rank = (a) => STATUS_ORDER.indexOf(a.checks.status)
    return [...apps].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
}

// FLIP: guarda onde cada card estava, deixa o shablon re-renderizar e anima
// cada card da posição antiga até a nova (como o animate:flip do Svelte).
function cardRects() {
    return new Map([...document.querySelectorAll(".card[data-app]:not(.skeleton)")].map((el) => [el.dataset.app, el.getBoundingClientRect()]))
}

function flip(before) {
    // Primeira renderização: os cards só aparecem, sem animar.
    if (!before.size || matchMedia("(prefers-reduced-motion: reduce)").matches) return
    for (const el of document.querySelectorAll(".card[data-app]:not(.skeleton)")) {
        const from = before.get(el.dataset.app)
        if (!from) {
            el.animate([{ opacity: 0, transform: "scale(.96)" }, { opacity: 1, transform: "none" }], { duration: 250, easing: "ease-out" })
            continue
        }
        const to = el.getBoundingClientRect()
        const dx = from.left - to.left
        const dy = from.top - to.top
        if (!dx && !dy) continue
        el.animate(
            [{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }],
            { duration: 450, easing: "cubic-bezier(.2, .8, .2, 1)" },
        )
    }
}

// Aplica uma mudança que mexe na lista (dados novos, filtro) animando os
// cards. O shablon re-renderiza num microtask; o requestAnimationFrame roda
// depois disso e antes do paint.
function withFlip(change) {
    const before = cardRects()
    change()
    requestAnimationFrame(() => flip(before))
}

function setApps(data, apps) {
    const sorted = sortApps(apps)
    // Durante o skeleton/entrada o FLIP mediria cards no meio da animação de
    // entrada e brigaria com ela; e se a ordem não mudou não há o que animar.
    const same = sorted.map((a) => a.name).join() === data.apps.map((a) => a.name).join()
    const apply = () => {
        if (data.loading || data.reveal || same) data.apps = sorted
        else withFlip(() => data.apps = sorted)
    }
    // App que estava sendo removida e sumiu da lista: o card sai animado
    // antes de os outros se reorganizarem.
    const names = new Set(sorted.map((a) => a.name))
    const gone = data.loading ? [] : [...document.querySelectorAll(".card-destroying[data-app]")].filter((el) => !names.has(el.dataset.app))
    if (gone.length) {
        Promise.all(gone.map((el) => el.animate(
            [{ opacity: 0.5, transform: "none" }, { opacity: 0, transform: "scale(.92)" }],
            { duration: 300, easing: "ease-in", fill: "forwards" },
        ).finished.catch(() => {}))).then(apply)
    } else apply()
    // Quantos cards o skeleton deve mostrar na próxima visita.
    try { localStorage.setItem("dokk:app-count", String(apps.length)) } catch {}
}

function skeletonCount() {
    try { return Number(localStorage.getItem("dokk:app-count")) || 6 } catch { return 6 }
}

// Blocos do resumo, um por status presente (o rótulo vem de statusLabel).
const SUMMARY = ["healthy", "degraded", "unhealthy", "starting", "restarting", "removing", "suspended", "unknown"]
    .map((key) => ({ key, get label() { return statusLabel(key) }, statuses: [key] }))

// Resumo: uma coluna por categoria presente, com o card (contagem + nome;
// clicar filtra a lista) e, embaixo, o trecho da barra. A largura da coluna é
// proporcional à contagem, então a barra fica alinhada com os cards.
function summary(data) {
    const counts = () => SUMMARY
        .map((s) => ({ ...s, n: data.apps.filter((a) => s.statuses.includes(a.checks.status)).length }))
        .filter((s) => s.n > 0)

    return t.div({ className: "summary", role: "group", "html-aria-label": () => tr("home.summary.aria") }, () => {
        const list = counts()
        // Com uma categoria só, filtrar não muda nada: não é clicável.
        const single = list.length === 1
        return list.map((s) => {
            const active = !single && data.status === s.key
            return t.div({
                rid: `${s.key}-${s.n}-${active}-${!!data.status}-${single}`,
                className: `category category-${s.key}${active ? " active" : ""}${!single && data.status && !active ? " dimmed" : ""}`,
                style: `flex-grow: ${s.n}`,
            },
                t.button({
                    type: "button",
                    className: "category-card",
                    title: () => single ? s.label : active ? tr("home.summary.showAll") : tr("home.summary.filter", { status: s.label }),
                    disabled: single,
                    onclick: () => withFlip(() => data.status = active ? "" : s.key),
                },
                    // Barra verde (cor do status) com bits claros e uma varredura.
                    t.span({ className: "category-bar" }),
                    t.span({ className: "category-text" },
                        t.strong({ textContent: String(s.n) }),
                        t.span({ className: "category-label", textContent: () => s.label }),
                    ),
                ),
            )
        })
    })
}

// Agrupa pelo prefixo do nome (o trecho antes do primeiro "-"): pbs-back e
// pbs-front viram "pbs". Prefixo com uma app só vai para "Outros", no fim.
// OTHERS é só a chave interna; o rótulo traduzido é home.groups.others.
// Os grupos seguem a ordem da lista (o pior status primeiro).
const OTHERS = "\u0000others"

// Grupo de cada app, calculado sobre a lista completa (assim uma busca que
// deixa só uma app do grupo não a joga em "Outros").
function groupKeys(all) {
    const prefix = (name) => name.split("-")[0]
    const count = new Map()
    for (const a of all) count.set(prefix(a.name), (count.get(prefix(a.name)) || 0) + 1)
    return new Map(all.map((a) => [a.name, count.get(prefix(a.name)) > 1 ? prefix(a.name) : OTHERS]))
}

function groupApps(list, keys) {
    const groups = new Map()
    const others = []
    for (const a of list) {
        const p = keys.get(a.name) ?? OTHERS
        if (p === OTHERS) {
            others.push(a)
            continue
        }
        if (!groups.has(p)) groups.set(p, [])
        groups.get(p).push(a)
    }
    const out = [...groups].map(([name, apps]) => ({ name, apps }))
    if (others.length) out.push({ name: OTHERS, apps: others, others: true })
    return out
}

function appGroup(g) {
    return t.section({ rid: `${g.name}:${g.apps.map((a) => a.name).join()}`, className: `group${g.others ? " group-others" : ""}` },
        t.h2({ className: "group-title" },
            t.span({ textContent: g.others ? () => tr("home.groups.others") : g.name }),
            t.span({ className: "group-count", textContent: String(g.apps.length) }),
        ),
        t.div({ className: "grid" }, ...g.apps.map(appCard)),
    )
}

// Quanto da máquina a app consome (soma dos containers), para achar quem
// mais pesa. A cor segue a fatia da máquina.
function usage(u) {
    if (!u) return null
    const pct = (v) => fmtNumber(v / 100, { style: "percent", maximumFractionDigits: v < 10 ? 1 : 0, minimumFractionDigits: v < 10 ? 1 : 0 })
    const lvl = (v) => v > 50 ? " hot" : v > 25 ? " warm" : ""
    return t.span({ className: "usage", title: () => tr("home.card.usage.title") },
        t.span({ className: `usage-item${lvl(u.cpu)}` }, t.span({ className: "icon icon-cpu" }), t.span({ textContent: () => pct(u.cpu) })),
        t.span({ className: `usage-item${lvl(u.mem)}` }, t.span({ className: "icon icon-mem" }), t.span({ textContent: () => pct(u.mem) })),
    )
}

function appCard(app) {
    const status = app.checks.status
    const procs = Object.entries(app.processes)

    // O card todo leva para a página da app, exceto cliques em links,
    // botões e popovers dentro dele.
    // Sendo removida: card apagado, sem link e sem clique.
    const removing = app.action === "destroy"
    const open = (e) => {
        if (removing || e.target.closest("a, button, .pop")) return
        navigate(`/apps/${encodeURIComponent(app.name)}`)
    }

    // Preload: ao passar o mouse (ou focar), os detalhes já vão sendo buscados.
    const preload = () => prefetch(`/api/apps/${encodeURIComponent(app.name)}`)

    return t.article({ rid: app, "html-data-app": app.name, className: `card card-${status}${removing ? " card-destroying" : ""}`, "html-aria-busy": removing ? "true" : "false", inert: removing, onclick: open, onpointerenter: preload, onfocusin: preload },
        t.header({ className: "card-head" },
            t.h2({ className: "card-title" }, removing ? t.span({ textContent: app.name }) : t.a({ href: `/apps/${encodeURIComponent(app.name)}`, textContent: app.name })),
            app.locked ? t.span({ className: "tag", textContent: () => tr("home.card.locked") }) : null,
            removing
                ? t.span({ className: "badge badge-destroying", title: () => tr("home.card.removing.title") }, t.span({ className: "spinner" }), t.span({ textContent: () => actionRunningLabel("destroy") }))
                : statusBadge(status),
        ),
        t.div({ className: "card-domains-row" },
            t.div({ className: "card-domains" }, ...domains(app)),
            usage(app.usage)),
        t.ul({ className: "procs" },
            ...(procs.length
                ? procs.map(([type, p]) => t.li({ className: p.running < p.total ? "proc proc-warn" : "proc" },
                    t.span({ textContent: type }),
                    t.span({ className: "proc-count", textContent: `${p.running}/${p.total}` }),
                ))
                : [t.li({ className: "muted", textContent: () => tr("home.card.processes.none") })]),
        ),
        heatmap(app),
        t.footer({ className: "card-foot muted" },
            t.span({ className: "card-version" }, t.span({ className: "icon icon-tag" }), version(app["git-sha"])),
            t.span({ textContent: () => timeAgo(app["last-deploy-at"]) }),
        ),
    )
}

const GB = 1024 ** 3
// Uma casa decimal abaixo de 100; formatado no idioma ativo.
const fmt1 = (v) => fmtNumber(v, { maximumFractionDigits: v >= 100 ? 0 : 1, minimumFractionDigits: v >= 100 ? 0 : 1 })
const fmtGB = (b) => fmt1(b / GB)

// Big numbers da máquina (CPU por núcleo, memória, disco), atualizados a
// cada 3s enquanto a home estiver aberta. Os dígitos rolam como roleta.
function hostStats() {
    const stat = (key, label) => {
        const value = t.span({ className: "stat-value" })
        const el = t.div({ className: `stat stat-${key}` },
            t.span({ className: "stat-label" }, t.span({ className: `icon stat-icon icon-${key}` }), t.span({ textContent: label })),
            value,
        )
        return { el, value }
    }
    const cpu = stat("cpu", () => tr("home.stats.cpu"))
    const mem = stat("mem", () => tr("home.stats.mem"))
    const disk = stat("disk", () => tr("home.stats.disk"))
    let cores = []

    // Valor com unidade pequena ao lado; a cor segue a ocupação.
    const part = (pct) => {
        const num = rollingNumber()
        const unit = t.small({})
        const el = t.span({ className: "stat-part" }, num, unit)
        el.update = (text, unitText, p = pct) => {
            num.set(text)
            unit.textContent = unitText
            el.classList.toggle("hot", p > 85)
            el.classList.toggle("warm", p > 65 && p <= 85)
        }
        return el
    }
    // Memória e disco: "usado/total"; no mobile o total desce (ver .stat-of).
    const memPart = part(0)
    const diskPart = part(0)
    memPart.classList.add("stat-of")
    diskPart.classList.add("stat-of")
    mem.value.append(memPart)
    const diskRing = t.span({ className: "disk-ring" })
    disk.value.append(diskRing, diskPart)

    const root = t.div({ className: "stats" }, cpu.el, mem.el, disk.el)
    skeleton(root)
    // Última coleta: repintada também na troca de idioma (números e títulos
    // são escritos direto no DOM).
    let last = null
    const load = () => fetch("/api/host").then((r) => r.ok ? r.json() : null).then((s) => {
        if (!s?.at || s.at.startsWith("0001")) return
        last = s
        paint(s)
    }).catch(() => {})
    const paint = (s) => {
        root.classList.remove("skeleton")
        root.removeAttribute("aria-busy")
        root.inert = false
        const per = s["per-core"]?.length ? s["per-core"] : [s.cpu]
        if (cores.length !== per.length) {
            // Um big number por núcleo, cada um com o próprio rótulo
            // (ícone | CPU N) e a barra vertical de carga ao lado do valor.
            cores = per.map((_, i) => {
                const c = stat("cpu", () => tr("home.stats.core", { n: i + 1 }))
                const num = part(0)
                const bar = t.span({ className: "core-bar" })
                c.value.append(t.span({ className: "core" }, bar, num))
                c.el.update = (p) => {
                    bar.style.setProperty("--p", `${Math.min(100, p)}%`)
                    num.update(String(Math.round(p)), "%", p)
                    c.el.querySelector(".core").className = `core${p > 85 ? " hot" : p > 65 ? " warm" : ""}`
                }
                return c.el
            })
            cpu.el.className = "stat-cores"
            cpu.el.replaceChildren(...cores)
        }
        per.forEach((p, i) => cores[i].update(p))
        cpu.el.title = tr("home.stats.cpu.title", {
            cores: per.map((p, i) => tr("home.stats.core.title", { n: i + 1, pct: Math.round(p) })).join(" · "),
            avg: Math.round(s.cpu),
        })
        const memPct = s["mem-total"] ? (100 * s["mem-used"]) / s["mem-total"] : 0
        const diskPct = s["disk-total"] ? (100 * s["disk-used"]) / s["disk-total"] : 0
        memPart.update(fmt1(s["mem-used"] / GB), tr("home.stats.gb", { n: fmtGB(s["mem-total"]) }), memPct)
        diskPart.update(fmt1(s["disk-used"] / GB), tr("home.stats.gb", { n: fmtGB(s["disk-total"]) }), diskPct)
        diskRing.style.setProperty("--p", `${diskPct}%`)
        diskRing.className = `disk-ring${diskPct > 85 ? " hot" : diskPct > 65 ? " warm" : ""}`
        mem.el.title = tr("home.stats.mem.title", { pct: memPct.toFixed(0) })
        disk.el.title = tr("home.stats.disk.title", { pct: diskPct.toFixed(0) })
    }
    // Placeholder do skeleton com o mesmo tamanho.
    const placeholder = () => {
        const n = (v) => fmtNumber(v, { minimumIntegerDigits: 2, maximumFractionDigits: 0 })
        const d = (v, i) => fmtNumber(v, { minimumIntegerDigits: i, minimumFractionDigits: 1, maximumFractionDigits: 1 })
        return { cpu: `${n(0)}% ${n(0)}%`, mem: d(0, 1), disk: d(0, 2) }
    }
    const cpuPlaceholder = t.span({ className: "stat-part", textContent: () => placeholder().cpu })
    cpu.value.append(cpuPlaceholder)
    const paintPlaceholder = () => {
        const ph = placeholder()
        memPart.update(ph.mem, tr("home.stats.gb", { n: ph.mem }))
        diskPart.update(ph.disk, tr("home.stats.gb", { n: ph.disk }))
    }
    paintPlaceholder()

    let timer
    let langWatch
    root.onmount = () => {
        if (!timer) { load(); timer = setInterval(load, 3000) }
        // Troca de idioma: repinta números e títulos escritos direto no DOM.
        langWatch ??= watch(() => i18n.lang, () => last ? paint(last) : paintPlaceholder())
    }
    root.onunmount = () => { clearInterval(timer); timer = null; langWatch?.unwatch(); langWatch = null }
    return root
}

// Ícone do empty state: um container (caixa) com um "+" de nova app.
function emptyIcon() {
    const el = t.span({ className: "empty-icon", "html-aria-hidden": "true" })
    el.innerHTML = `<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
        <path d="M24 6 40 15v18L24 42 8 33V15Z"/>
        <path d="M8 15l16 9 16-9M24 24v18"/>
        <path d="M38 4v8M34 8h8" stroke-width="2.5"/>
    </svg>`
    return el
}

export function home() {
    // Voltando de outra página, a lista em cache aparece na hora (sem skeleton)
    // e é revalidada por baixo.
    const initial = cached("/api/apps")
    const pref = (k, d) => { try { return localStorage.getItem(`dokk:${k}`) ?? d } catch { return d } }
    const save = (k, v) => { try { localStorage.setItem(`dokk:${k}`, v) } catch {} }
    const data = store({ query: "", status: "", group: pref("group", ""), sort: pref("sort", "status"), apps: initial ? sortApps(initial) : [], loading: !initial, reveal: false, error: "", live: false })

    // Saída do skeleton: liga o fade de entrada só na primeira vez, para as
    // atualizações seguintes (a cada coleta) não piscarem.
    function finishLoading() {
        if (!data.loading) return
        data.loading = false
        data.reveal = true
        setTimeout(() => data.reveal = false, 700)
    }

    // Primeira carga pelo fetch (pré-carregado no index.html); depois o
    // servidor empurra cada nova coleta via SSE.
    // Sem apps (ou sem Dokku, quando /api/apps falha), pergunta ao servidor
    // se o onboarding ainda é necessário e manda para lá.
    const checkOnboarding = () => fetch("/api/onboarding")
        .then((r) => r.ok ? r.json() : null)
        .then((body) => { if (body?.needed && location.pathname === "/") navigate("/onboarding", { replace: true }) })
        .catch(() => {})
    swr("/api/apps")
        .then((body) => { setApps(data, body); if (!body.length) checkOnboarding() })
        .catch((err) => { if (!data.apps.length) data.error = err.message; checkOnboarding() })
        .finally(finishLoading)

    const close = subscribeApps(data, (apps) => {
        mutate("/api/apps", apps)
        setApps(data, apps)
        data.error = ""
        finishLoading()
    })

    const statusFilter = () => SUMMARY.find((s) => s.key === data.status)?.statuses
    const keys = () => groupKeys(data.apps)
    const filtered = () => {
        const k = keys()
        // Grupo salvo que não existe mais (app renomeada/removida) não filtra.
        const group = [...k.values()].includes(data.group) ? data.group : ""
        const list = data.apps.filter((a) =>
            a.name.includes(data.query.trim().toLowerCase()) &&
            (!statusFilter() || statusFilter().includes(a.checks.status)) &&
            (!group || k.get(a.name) === group))
        return SORTS[data.sort]?.fn ? [...list].sort(SORTS[data.sort].fn) : list
    }
    const setFilter = (k, v) => withFlip(() => { data[k] = v; save(k, v) })

    // Sem nenhuma app, filtro/ordenação/resumo só poluem o empty state.
    const noApps = () => !data.loading && !data.error && !data.apps.length

    // Chips de grupo + select de ordenação.
    const toolbar = t.div({ className: "toolbar", hidden: noApps },
        t.div({ className: "chips", role: "group", "html-aria-label": () => tr("home.groups.aria") }, () => {
            const k = keys()
            const names = [...new Set(k.values())].sort((a, b) => (a === OTHERS) - (b === OTHERS) || a.localeCompare(b))
            if (names.length < 2) return []
            const count = (g) => [...k.values()].filter((v) => v === g).length
            const active = names.includes(data.group) ? data.group : ""
            return [{ key: "", label: () => tr("home.groups.all"), n: data.apps.length }, ...names.map((g) => ({ key: g, label: g === OTHERS ? () => tr("home.groups.others") : g, n: count(g) }))].map((c) =>
                t.button({
                    rid: `${c.key}-${c.n}-${active === c.key}`,
                    type: "button",
                    className: `chip-filter${active === c.key ? " active" : ""}`,
                    "html-aria-pressed": String(active === c.key),
                    onclick: () => setFilter("group", c.key),
                },
                    t.span({ textContent: c.label }),
                    t.span({ className: "chip-count", textContent: String(c.n) }),
                ))
        }),
        t.label({ className: "select" },
            t.span({ className: "select-label", textContent: () => tr("home.sort.label") }),
            t.select({ onchange: (e) => setFilter("sort", e.target.value) },
                ...Object.entries(SORTS).map(([key, s]) => t.option({ value: key, textContent: () => s.label, selected: data.sort === key }))),
        ),
    )

    const page = t.main({ className: "page" },
        t.header({ className: "page-head" },
            t.div({ className: "title" },
                t.h1({ textContent: () => tr("home.title") }),
                liveBadge(data),
            ),
            hostStats(),
            // "Nova app" começa do zero: descarta um deploy salvo do onboarding.
            t.a({ className: "btn btn-primary", href: "/onboarding", onclick: () => { try { sessionStorage.removeItem("dokk:onboarding") } catch {} } },
                t.span({ className: "icon icon-plus" }),
                t.span({ textContent: () => tr("home.newApp") }),
            ),
            t.input({
                type: "search",
                className: "search",
                hidden: noApps,
                placeholder: () => tr("home.search.placeholder"),
                value: () => data.query,
                oninput: (e) => withFlip(() => data.query = e.target.value),
            }),
        ),
        t.div({ hidden: noApps }, () => data.loading
            ? skeleton(summary({ apps: Array.from({ length: skeletonCount() }, (_, i) => fakeApp(i)), status: "" }))
            : summary(data)),
        toolbar,
        t.div({ className: () => `groups${data.reveal ? " reveal" : ""}` }, () => {
            if (data.error) return t.p({ rid: "error", className: "muted", textContent: () => tr("home.error.load", { message: data.error }) })
            if (data.loading) return t.div({ className: "grid" }, ...Array.from({ length: skeletonCount() }, (_, i) => skeleton(appCard(fakeApp(i)))))
            const list = filtered()
            if (!data.apps.length && !data.query && !data.status) {
                return t.div({ rid: "first", className: "empty-state" },
                    emptyIcon(),
                    t.h2({ textContent: () => tr("home.empty.title") }),
                    t.p({ className: "muted", textContent: () => tr("home.empty.text") }),
                    t.div({ className: "empty-actions" },
                        t.a({ className: "btn btn-primary", href: "/onboarding" },
                            t.span({ className: "icon icon-plus" }),
                            t.span({ textContent: () => tr("home.empty.create") }),
                        ),
                        t.a({ className: "btn", href: "/registries", textContent: () => tr("home.empty.registries") }),
                    ),
                )
            }
            if (!list.length) return t.p({ rid: "empty", className: "muted", textContent: () => tr("home.noResults") })
            return groupApps(list, keys()).map(appGroup)
        }),
    )

    page.destroy = close
    return page
}
