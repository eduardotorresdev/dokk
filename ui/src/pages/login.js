import { t, store } from "shablon"
import { brand, langSelect, navigate } from "../shared.js"
import { errText, i18n, register, tr } from "../i18n.js"
import en from "../locales/en/auth.js"
import es from "../locales/es/auth.js"
import ptBR from "../locales/pt-BR/auth.js"

register({ en, es, "pt-BR": ptBR })

// Telas de acesso (login e criação do superusuário): formulário à esquerda e,
// à direita, uma doca em 3D feita em CSS.

// ---------- cena: doca em 3D (CSS) ----------
//
// Mar aberto com dois cargueiros passando (um em cada sentido) pelo centro
// do quadro e alguns itens de infra boiando à deriva. A origem do plano é o
// centro do painel.
//
// Loop perfeito: LOOP = 40s; itens boiando 4s. Em
// t = 40s tudo está como em t = 0: navios e itens dão a volta fora do quadro
// (ou sob a água).
const LOOP = 40

// Caixa 3D: x/y no chão, z altura da base, w × d de área e h de altura. Só
// as faces viradas para a câmera: topo + frente (+y) + direita (+x); num
// objeto girado 180° (o navio que vem no sentido contrário), fundo + esquerda.
const FACING = ["front", "right"]
const FACING_REV = ["back", "side"]
function box(kind, x, y, w, d, h, z = 0, faces = FACING) {
    const style = `--x:${x}px;--y:${y}px;--z:${z}px;--w:${w}px;--d:${d}px;--h:${h}px`
    return t.div({ className: `iso-block iso-${kind}`, style },
        t.div({ className: "iso-face iso-top" }),
        ...faces.map((f) => t.div({ className: `iso-face iso-${f}` })),
    )
}
const group = (cls, ...children) => t.div({ className: `iso-group ${cls}` }, ...children)

const C = { w: 60, d: 26, h: 24 } // container (comprido em x)
const SHADES = ["c1", "c2", "c3", "c4"]
const DECK = 30

// Uma caixa por pilha (as divisões entre containers ficam na textura).
function shipBody(stacks, faces = FACING) {
    const parts = [
        box("hull", 0, 0, 500, 100, 42, -12, faces),
        box("bridge", 410, 14, 60, 72, 64, DECK, faces),
        box("bridge-top", 404, 6, 72, 88, 6, DECK + 64, faces),
        box("funnel", 440, 40, 14, 20, 26, DECK + 70, faces),
    ]
    stacks.forEach((row, r) => row.forEach((n, c) => {
        if (n) parts.push(box(`container ${SHADES[(r + c * 2) % 4]}`, 16 + c * 64, 10 + r * 30, C.w, C.d, C.h * n, DECK, faces))
    }))
    return parts
}

// ---------- mundo: grid fixo no chão ----------
//
// Um único <canvas> 2D atrás da cena com um grid reto (linhas paralelas ao movimento
// dos navios e transversais), projetadas com a mesma câmera da cena (rotateX 56°, rotateZ
// 42°, perspectiva 2600px). Estáticas: desenha uma vez e só redesenha quando
// o painel muda de tamanho.
const WORLD = { gap: 120, half: 1500 }
const RX = (56 * Math.PI) / 180
const RZ = (42 * Math.PI) / 180
const PERSPECTIVE = 2600

function project(x, y, z) {
    // rotateZ e depois rotateX (ordem do CSS da direita para a esquerda).
    const x1 = x * Math.cos(RZ) - y * Math.sin(RZ)
    const y1 = x * Math.sin(RZ) + y * Math.cos(RZ)
    const y2 = y1 * Math.cos(RX) - z * Math.sin(RX)
    const z2 = y1 * Math.sin(RX) + z * Math.cos(RX)
    const k = PERSPECTIVE / (PERSPECTIVE - z2)
    return [x1 * k, y2 * k]
}

function mountWorld(host) {
    const canvas = document.createElement("canvas")
    canvas.className = "world"
    host.prepend(canvas)
    const ctx = canvas.getContext("2d")

    const draw = (w, h) => {
        const { gap, half } = WORLD
        ctx.clearRect(0, 0, w, h)
        ctx.lineWidth = 1.2
        for (let y = -half; y <= half; y += gap) {
            const [x0, y0] = project(-half, y, 0)
            const [x1, y1] = project(half, y, 0)
            ctx.beginPath()
            ctx.moveTo(w / 2 + x0, h / 2 + y0)
            ctx.lineTo(w / 2 + x1, h / 2 + y1)
            // Mais apagadas ao longe (topo do painel).
            ctx.strokeStyle = `rgb(255 255 255 / ${(0.05 + 0.1 * (y + half) / (2 * half)).toFixed(3)})`
            ctx.stroke()
        }
        // Linhas transversais: fecham o grid.
        ctx.strokeStyle = "rgb(255 255 255 / 0.08)"
        ctx.beginPath()
        for (let x = -half; x <= half; x += gap) {
            const [x0, y0] = project(x, -half, 0)
            const [x1, y1] = project(x, half, 0)
            ctx.moveTo(w / 2 + x0, h / 2 + y0)
            ctx.lineTo(w / 2 + x1, h / 2 + y1)
        }
        ctx.stroke()
    }
    const resize = () => {
        const r = host.getBoundingClientRect()
        const dpr = Math.min(window.devicePixelRatio || 1, 2)
        canvas.width = Math.round(r.width * dpr)
        canvas.height = Math.round(r.height * dpr)
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
        draw(r.width, r.height)
    }
    const ro = new ResizeObserver(resize)
    ro.observe(host)
    resize()
    return () => { ro.disconnect(); canvas.remove() }
}

function drifters() {
    return [
        ["crate", 44, 44, 26, 200, 0], ["server", 36, 54, 30, -260, -10],
        ["barrel", 26, 26, 30, 230, -20], ["crate", 54, 36, 22, -290, -30],
    ].map(([kind, w, d, h, y, delay]) =>
        t.div({ className: "iso-group iso-drift", style: `--y:${y}px;--delay:${delay}s` },
            group("iso-bob", box(kind, 0, 0, w, d, h, -6))))
}

// framed: com moldura (telas de acesso); sem, ocupa a tela toda (erros).
export function scene({ framed = true } = {}) {
    let stop = null
    return t.div({
        className: `login-art${framed ? "" : " login-art-full"}`,
        "html-aria-hidden": "true",
        onmount: (el) => { if (!stop) stop = mountWorld(el) },
        onunmount: () => { stop?.(); stop = null },
    },
        t.div({ className: "iso-scene", style: `--loop:${LOOP}s` },
            // Dois cargueiros, um em cada sentido, em faixas diferentes.
            t.div({ className: "iso-group iso-sailing", style: "--y:-150px" },
                group("iso-ship-bob", ...shipBody([[2, 3, 1, 2, 3, 1], [3, 2, 2, 1, 2, 2], [1, 2, 3, 2, 1, 3]]))),
            t.div({ className: "iso-group iso-sailing iso-sailing-rev", style: "--y:40px" },
                group("iso-ship-bob", ...shipBody([[3, 2, 3, 1, 2, 3], [2, 3, 2, 3, 3, 1], [3, 1, 3, 2, 1, 2]], FACING_REV))),
            ...drifters(),
        ),
    )
}

// Para onde ir depois do login: o caminho guardado em "dokk:next" (pathname +
// query). Valores antigos no formato "#/..." viram caminho; qualquer coisa
// fora da própria UI (ou o próprio login/setup) cai na home.
function safeNext(next) {
    if (next?.startsWith("#/")) next = next.slice(1)
    if (!next?.startsWith("/") || /^\/[\\/]/.test(next) || /^\/(login|setup)(?:[?#]|$)/.test(next)) return "/"
    return next
}

export function login() {
    const ui = store({ email: "", password: "", busy: false, error: "" })
    const submit = async (e) => {
        e.preventDefault()
        ui.busy = true
        ui.error = ""
        try {
            const res = await fetch("/api/login", {
                method: "POST",
                headers: { "X-Dokk": "1", "Content-Type": "application/json" },
                body: JSON.stringify({ email: ui.email, password: ui.password }),
            })
            const body = await res.json().catch(() => ({}))
            if (!res.ok) throw new Error(body.error || res.statusText)
            const next = sessionStorage.getItem("dokk:next")
            sessionStorage.removeItem("dokk:next")
            navigate(safeNext(next), { replace: true })
        } catch (err) {
            ui.error = err.message
            ui.password = ""
            form.querySelector("input[name=password]").focus()
        } finally {
            ui.busy = false
        }
    }
    const form = t.form({ className: "login-form", onsubmit: submit },
        t.h1({ textContent: () => tr("auth.login.title") }),
        t.p({ className: "muted", textContent: () => tr("auth.login.subtitle") }),
        t.label({ className: "field" },
            t.span({ textContent: () => tr("auth.email.label") }),
            t.input({
                className: "input", name: "email", type: "text", inputMode: "email", autocomplete: "username",
                placeholder: () => tr("auth.email.placeholder"), required: true, autofocus: true,
                value: () => ui.email, oninput: (e) => { ui.email = e.target.value; ui.error = "" },
            }),
        ),
        t.label({ className: "field" },
            t.span({ textContent: () => tr("auth.password.label") }),
            t.input({
                className: "input", name: "password", type: "password", autocomplete: "current-password",
                placeholder: "••••••••", required: true,
                value: () => ui.password, oninput: (e) => { ui.password = e.target.value; ui.error = "" },
            }),
        ),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => errText(ui.error) }),
        t.button({ type: "submit", className: "btn btn-primary login-submit", disabled: () => ui.busy },
            () => ui.busy
                ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("auth.login.busy") })]
                : [t.span({ textContent: () => tr("auth.login.submit") })]),
    )
    return authLayout(form)
}

// Layout das telas de acesso: formulário à esquerda, cidade à direita.
function authLayout(form, { onunmount } = {}) {
    return t.main({ className: "login", ...(onunmount ? { onunmount } : {}) },
        t.section({ className: "login-form-wrap" },
            t.div({ className: "auth-lang" }, langSelect()),
            brand("", { animated: true }),
            form,
            t.p({ className: "login-foot muted", textContent: location.hostname }),
        ),
        scene(),
    )
}

const MIN_PASSWORD = 10

// Primeira execução: cria o superusuário (nome, e-mail e senha) e já entra.
export function setup() {
    // Nome em branco fica vazio no servidor e o menu mostra o nome padrão no
    // idioma ativo (por isso o padrão é só placeholder).
    const ui = store({ name: "", email: "", password: "", confirm: "", busy: false, error: "" })
    const field = (label, key, attrs) => t.label({ className: "field" },
        t.span({ textContent: label }),
        t.input({
            className: "input", name: key, required: key !== "name", ...attrs,
            value: () => ui[key], oninput: (e) => { ui[key] = e.target.value; ui.error = "" },
        }),
    )
    const submit = async (e) => {
        e.preventDefault()
        if (ui.password.length < MIN_PASSWORD) return ui.error = { key: "auth.setup.error.passwordTooShort", params: { min: MIN_PASSWORD } }
        if (ui.password !== ui.confirm) return ui.error = { key: "auth.setup.error.mismatch" }
        ui.busy = true
        ui.error = ""
        try {
            const res = await fetch("/api/setup", {
                method: "POST",
                headers: { "X-Dokk": "1", "Content-Type": "application/json" },
                body: JSON.stringify({ name: ui.name, email: ui.email, password: ui.password, lang: i18n.lang }),
            })
            const body = await res.json().catch(() => ({}))
            if (!res.ok) throw new Error(body.error || res.statusText)
            navigate("/", { replace: true })
        } catch (err) {
            ui.error = err.message
        } finally {
            ui.busy = false
        }
    }
    const form = t.form({ className: "login-form", onsubmit: submit },
        t.span({ className: "setup-step mono", textContent: () => tr("auth.setup.step") }),
        t.h1({ textContent: () => tr("auth.setup.title") }),
        t.p({ className: "muted", textContent: () => tr("auth.setup.subtitle") }),
        field(() => tr("auth.name.label"), "name", { autocomplete: "name", placeholder: () => tr("auth.setup.defaultName") }),
        field(() => tr("auth.email.label"), "email", { type: "email", autocomplete: "username", placeholder: () => tr("auth.email.placeholder"), autofocus: true }),
        field(() => tr("auth.password.label"), "password", { type: "password", autocomplete: "new-password", placeholder: () => tr("auth.setup.password.placeholder", { min: MIN_PASSWORD }), minLength: MIN_PASSWORD }),
        field(() => tr("auth.confirm.label"), "confirm", { type: "password", autocomplete: "new-password", placeholder: () => tr("auth.confirm.placeholder") }),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => errText(ui.error) }),
        t.button({ type: "submit", className: "btn btn-primary login-submit", disabled: () => ui.busy },
            () => ui.busy
                ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("auth.setup.busy") })]
                : [t.span({ textContent: () => tr("auth.setup.submit") })]),
    )
    return authLayout(form)
}
