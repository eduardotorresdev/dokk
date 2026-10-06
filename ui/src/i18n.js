import { store, watch } from "shablon"
import en from "./locales/en/common.js"
import es from "./locales/es/common.js"
import ptBR from "./locales/pt-BR/common.js"

// Idiomas suportados, com o nome no próprio idioma (nunca traduzido).
export const LANGS = [
    { code: "en", label: "English" },
    { code: "es", label: "Español" },
    { code: "pt-BR", label: "Português (Brasil)" },
]
export const DEFAULT_LANG = "en"

const LANG_KEY = "dokk:lang"
const PENDING_KEY = "dokk:lang-pending"

function getItem(key) {
    try { return localStorage.getItem(key) } catch { return null }
}
function setItem(key, value) {
    try { localStorage.setItem(key, value) } catch {}
}
function removeItem(key) {
    try { localStorage.removeItem(key) } catch {}
}

// "pt", "pt-PT", "PT_br" → "pt-BR"; "es-419" → "es"; "en-GB" → "en"; o
// resto → "". Mesmas regras do normalizeLang do Go.
export function normalizeLang(tag) {
    const primary = String(tag ?? "").trim().toLowerCase().split(/[-_]/)[0]
    return { pt: "pt-BR", es: "es", en: "en" }[primary] || ""
}

// Primeiro idioma do navegador que a gente suporta; senão, inglês.
export function detectLang(list = navigator.languages?.length ? navigator.languages : [navigator.language]) {
    for (const tag of list || []) {
        const l = normalizeLang(tag)
        if (l) return l
    }
    return DEFAULT_LANG
}

// Única fonte reativa: ler i18n.lang dentro de um watch/binding rastreia o idioma.
// pageTitle: rótulo do título da aba definido pela página (setPageTitle).
export const i18n = store({ lang: normalizeLang(getItem(LANG_KEY)) || detectLang(), pageTitle: "" })

watch(() => document.documentElement.lang = i18n.lang)

// ---------- dicionários ----------

const dicts = { en: {}, es: {}, "pt-BR": {} }

export function register(more) {
    for (const [lang, entries] of Object.entries(more)) {
        const dict = dicts[lang]
        if (!dict) continue
        for (const [key, value] of Object.entries(entries)) {
            if (key in dict) console.warn(`i18n: chave registrada duas vezes: ${key}`)
            dict[key] = value
        }
    }
}

register({ en, es, "pt-BR": ptBR })

const warned = new Set()

function lookup(lang, key) {
    if (key in dicts[lang]) return dicts[lang][key]
    if (key in dicts[DEFAULT_LANG]) return dicts[DEFAULT_LANG][key]
    return undefined
}

function fill(text, params) {
    if (!params) return text
    return text.replace(/\{([A-Za-z0-9_]+)\}/g, (all, name) => name in params ? String(params[name]) : all)
}

// Texto traduzido (sempre texto puro: só vai para textContent/atributos).
export function tr(key, params) {
    const lang = i18n.lang
    const text = lookup(lang, key)
    if (text === undefined) {
        if (!warned.has(key)) {
            warned.add(key)
            console.warn(`i18n: chave sem tradução: ${key}`)
        }
        return key
    }
    return fill(text, params)
}

// Erro guardado em estado: texto do servidor (já traduzido ou stderr cru) ou
// { key, params } traduzido na hora, para acompanhar a troca de idioma.
export const errText = (e) => !e ? "" : typeof e === "string" ? e : tr(e.key, e.params)

// Plural: usa key.<categoria> (one, other…) e cai para key.other.
export function trn(key, count, params = {}) {
    const lang = i18n.lang
    const cat = cachedIntl("PluralRules", lang).select(count)
    const k = lookup(lang, `${key}.${cat}`) !== undefined ? `${key}.${cat}` : `${key}.other`
    return tr(k, { ...params, count: fmtNumber(count) })
}

// ---------- formatação ----------

const intl = new Map()

function cachedIntl(kind, lang, opts) {
    const id = `${kind}|${lang}|${JSON.stringify(opts ?? {})}`
    let f = intl.get(id)
    if (!f) {
        f = new Intl[kind](lang, opts)
        intl.set(id, f)
    }
    return f
}

export function fmtNumber(n, opts) {
    return cachedIntl("NumberFormat", i18n.lang, opts).format(n)
}

export function fmtDate(d, opts = { dateStyle: "medium" }) {
    return cachedIntl("DateTimeFormat", i18n.lang, opts).format(new Date(d))
}

export function fmtDateTime(d) {
    return fmtDate(d, { dateStyle: "short", timeStyle: "medium" })
}

export function fmtTime(d) {
    return fmtDate(d, { timeStyle: "medium" })
}

// "há 3 min" / "3 min ago" / "hace 3 min".
export function fmtRelative(iso) {
    if (!iso) return "—"
    const f = cachedIntl("RelativeTimeFormat", i18n.lang, { numeric: "always", style: "short" })
    const min = Math.round((Date.now() - new Date(iso)) / 60000)
    if (min < 1) return tr("common.time.now")
    if (min < 60) return f.format(-min, "minute")
    const h = Math.round(min / 60)
    if (h < 24) return f.format(-h, "hour")
    return f.format(-Math.round(h / 24), "day")
}

// ---------- preferência salva ----------

// Idioma salvo no servidor (GET /api/session .lang); "" = ainda não escolhido.
let serverLang = ""

// Troca o idioma na hora e tenta salvar no servidor. Sem sessão (login,
// setup) o PUT falha e o "pending" faz o envio depois do próximo login.
export function setLang(code, { remote = true } = {}) {
    const lang = normalizeLang(code)
    if (!lang) return
    i18n.lang = lang
    setItem(LANG_KEY, lang)
    if (!remote) return
    setItem(PENDING_KEY, "1")
    fetch("/api/prefs", {
        method: "PUT",
        headers: { "X-Dokk": "1", "Content-Type": "application/json" },
        body: JSON.stringify({ lang }),
    }).then((res) => {
        if (!res.ok) return
        removeItem(PENDING_KEY)
        serverLang = lang
    }).catch(() => {})
}

// Chamado pelo guard do router com o .lang da sessão.
export function syncServerLang(s) {
    if (getItem(PENDING_KEY)) setLang(i18n.lang)
    else if (s && s !== i18n.lang && normalizeLang(s) === s) {
        i18n.lang = s
        setItem(LANG_KEY, s)
    } else if (s === "" && normalizeLang(getItem(LANG_KEY))) setLang(i18n.lang)
    serverLang = s ?? ""
}

// Fim do onboarding: o idioma detectado vira a escolha salva.
export function ensureServerLang() {
    if (serverLang === "") setLang(i18n.lang)
}
