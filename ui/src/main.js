import { router, t, watch } from "shablon"
import { home } from "./pages/home.js"
import { appDetail } from "./pages/app.js"
import { errorRoute, notFound } from "./pages/not-found.js"
import { login, setup } from "./pages/login.js"
import { onboarding } from "./pages/onboarding.js"
import { registries } from "./pages/registries.js"
import { brand, headerNav, navigate, userMenu } from "./shared.js"
import { i18n, syncServerLang, tr } from "./i18n.js"

const app = document.getElementById("app")

// Links antigos no formato "/#/apps/foo" viram "/apps/foo" antes do router
// (e do shim abaixo, que guarda a URL inicial) começar, sem criar entrada
// nova no histórico.
if (location.hash.startsWith("#/")) {
    history.replaceState(history.state, "", location.hash.slice(1) || "/")
}

// URLs limpas ("/apps/foo") via Navigation API, que o router do shablon
// intercepta. Navegador sem ela: shim mínimo abaixo, com o mesmo contrato
// que o router usa (evento "navigate" + navigation.navigate()).
if (!window.navigation) window.navigation = navigationShim()

// Cada página pode expor `destroy` (fechar streams etc.), chamado pelo
// router ao sair da rota.
// Transição entre rotas: fade + scale. Entrar numa app "aproxima" (cresce
// de um pouco menor); voltar pra home "afasta" (encolhe de um pouco maior).
const depth = (path) => (path.startsWith("/apps/") ? 1 : 0)
let lastDepth = depth(location.pathname)

// Header do painel (marca + conta): o mesmo elemento em todas as telas
// logadas (home, apps, registries e onboarding). Fica no topo e rola com a página — não é
// sticky —, e some nas telas de acesso e de erro.
const header = document.createElement("header")
header.className = "app-header"
header.hidden = true
app.before(header)
let headerReady = false
const showHeader = (on) => {
    if (on && !headerReady) {
        header.append(brand("", { loop: true, extra: t.div({ className: "header-right" }, headerNav(), userMenu()) }))
        headerReady = true
    }
    header.hidden = !on
}

// Título da aba por rota ("Apps - dokk"); segue a troca de idioma. A página
// pode trocar o rótulo depois com setPageTitle (ex.: onboarding).
const TITLES = [
    [/^\/apps\/([^/?]+)$/, (m) => decodeURIComponent(m[1])],
    [/^\/registries$/, () => tr("common.page.registries")],
    [/^\/onboarding$/, () => tr("common.page.onboarding")],
    [/^\/login$/, () => tr("common.page.login")],
    [/^\/setup$/, () => tr("common.page.setup")],
    [/^\/$/, () => tr("common.page.apps")],
    // /404, /erro/{código} e qualquer endereço desconhecido.
    [/./, () => tr("common.page.notFound")],
]
let titleWatch = null
const routeTitle = () => {
    for (const [re, label] of TITLES) {
        const m = location.pathname.match(re)
        if (m) return label(m)
    }
    return ""
}
const applyTitle = () => {
    titleWatch?.unwatch()
    titleWatch = watch(() => [i18n.lang, i18n.pageTitle], () => {
        const label = i18n.pageTitle || routeTitle()
        document.title = label ? `${label} - dokk` : "dokk"
    })
}

const mount = (page, { chrome = false } = {}) => (route) => {
    showHeader(chrome)
    i18n.pageTitle = ""
    applyTitle()
    const el = page(route)
    const next = depth(location.pathname)
    const dir = Math.sign(next - lastDepth)
    lastDepth = next
    app.replaceChildren(el)
    window.scrollTo(0, 0)
    if (!matchMedia("(prefers-reduced-motion: reduce)").matches) {
        el.animate(
            [{ opacity: 0, transform: `scale(${dir < 0 ? 1.02 : 0.98})` }, { opacity: 1, transform: "none" }],
            { duration: 260, easing: "cubic-bezier(.2, .8, .2, 1)" },
        )
    }
    return el.destroy
}

// Sem sessão, qualquer rota vai para o login (guardando para onde ia); com
// sessão, o login manda para a home. Respostas 401 da API fazem o mesmo.
const realFetch = window.fetch
window.fetch = async (input, init = {}) => {
    const url = String(input?.url ?? input)
    // Toda chamada à API leva o idioma ativo: o servidor traduz os erros dele.
    if (url.startsWith("/api/")) {
        const headers = new Headers(init.headers ?? (input instanceof Request ? input.headers : undefined))
        if (!headers.has("X-Dokk-Lang")) headers.set("X-Dokk-Lang", i18n.lang)
        init = { ...init, headers }
    }
    const res = await realFetch(input, init)
    const here = location.pathname
    if (res.status === 401 && url.startsWith("/api/") && !/^\/api\/(login|setup|session|prefs)/.test(url) && here !== "/login" && here !== "/setup") {
        sessionStorage.setItem("dokk:next", here + location.search)
        navigate("/login", { replace: true })
    }
    return res
}

// Sem superusuário (primeira execução), tudo vai para o setup.
const guard = (page) => async (route) => {
    // /api/session fica fora do redirect de 401, então dá para usar o wrapper.
    const res = await window.fetch("/api/session")
    const body = await res.json().catch(() => ({}))
    // Idioma salvo no servidor manda; uma escolha local pendente sobe agora.
    syncServerLang(body.lang)
    // Redirects trocam a entrada atual: o "voltar" não cai de novo no guard.
    const here = location.pathname
    const target = body.setup ? "/setup" : !res.ok ? "/login" : null
    if (target && here !== target) {
        if (!body.setup && here !== "/setup") sessionStorage.setItem("dokk:next", here + location.search)
        navigate(target, { replace: true })
        return
    }
    if (!target && (here === "/login" || here === "/setup")) {
        navigate("/", { replace: true })
        return
    }
    const chrome = [home, appDetail, onboarding, registries].includes(page)
    return mount(page, { chrome })(route)
}

router({
    "/": guard(home),
    "/apps/{name}": guard(appDetail),
    "/onboarding": guard(onboarding),
    "/registries": guard(registries),
    "/login": guard(login),
    "/setup": guard(setup),
    "/404": mount(notFound),
    "/erro/{code}": mount(errorRoute),
    // Endereço desconhecido: mostra o 404 sem trocar a URL (o servidor
    // devolve o index.html para qualquer caminho fora de /api e dos arquivos).
    "/{path...}": mount(notFound),
}, { pretty: true, fallbackPath: "/404" })

// Shim da Navigation API para navegadores sem ela: só o que o router do
// shablon e navigate() usam. Intercepta cliques em links da própria origem
// (sem target, download ou tecla modificadora) e o voltar/avançar do
// navegador, e dispara "navigate" com intercept() chamando o handler na hora.
function navigationShim() {
    const nav = new EventTarget()
    let current = location.pathname + location.search
    const fire = (url, navigationType, state) => {
        const dest = new URL(url)
        const hashChange = dest.pathname + dest.search === current && dest.hash !== ""
        current = dest.pathname + dest.search
        let handler = null
        const e = Object.assign(new Event("navigate"), {
            canIntercept: true,
            hashChange,
            downloadRequest: null,
            navigationType,
            destination: { url: dest.href, getState: () => state },
            intercept: (opts) => { handler = opts?.handler },
        })
        nav.dispatchEvent(e)
        nav.dispatchEvent(new Event("currententrychange"))
        handler?.()
    }
    nav.navigate = (url, { history: mode = "auto", state = null } = {}) => {
        const dest = new URL(url, location.href)
        if (dest.origin !== location.origin) return location.assign(dest.href)
        const replace = mode === "replace" || (mode === "auto" && dest.href === location.href)
        history[replace ? "replaceState" : "pushState"](state, "", dest.href)
        fire(dest.href, replace ? "replace" : "push", state)
    }
    window.addEventListener("popstate", (e) => fire(location.href, "traverse", e.state))
    document.addEventListener("click", (e) => {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
        const a = e.target.closest?.("a[href]")
        // <a> de SVG tem href como objeto (SVGAnimatedString): fica de fora.
        if (!(a instanceof HTMLAnchorElement) || (a.target && a.target !== "_self") || a.hasAttribute("download")) return
        const dest = new URL(a.href)
        if (dest.origin !== location.origin) return
        // Só a âncora mudou: deixa o navegador rolar até ela.
        if (dest.pathname + dest.search === location.pathname + location.search && dest.hash) return
        e.preventDefault()
        nav.navigate(dest.href)
    })
    return nav
}
