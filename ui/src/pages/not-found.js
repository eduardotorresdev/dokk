import { t } from "shablon"
import { brand, langSelect } from "../shared.js"
import { register, tr } from "../i18n.js"
import { scene } from "./login.js"
import en from "../locales/en/errors.js"
import es from "../locales/es/errors.js"
import ptBR from "../locales/pt-BR/errors.js"

register({ en, es, "pt-BR": ptBR })

// Códigos com título e texto próprios (errors.<código>.title/.text).
const ERRORS = [401, 403, 404, 500, 502, 503]

// Tela de erro (4xx/5xx): a doca animada ocupa a tela toda, sem moldura, com
// o código e a mensagem por cima.
export function errorPage(code = 404) {
    const key = ERRORS.includes(code) ? code : code >= 500 ? 500 : 404
    return t.main({ className: "error-page" },
        scene({ framed: false }),
        t.div({ className: "error-top" }, brand(), t.div({ className: "auth-lang" }, langSelect())),
        t.section({ className: "error-body" },
            t.span({ className: "error-code", textContent: String(code) }),
            t.h1({ textContent: () => tr(`errors.${key}.title`) }),
            t.p({ className: "muted", textContent: () => tr(`errors.${key}.text`) }),
            t.div({ className: "error-actions" },
                t.a({ className: "btn btn-primary", href: "/", textContent: () => tr("errors.home") }),
                code >= 500 ? t.button({ type: "button", className: "btn", onclick: () => location.reload(), textContent: () => tr("errors.retry") }) : null,
            ),
        ),
    )
}

export function notFound() {
    return errorPage(404)
}

export function errorRoute(route) {
    return errorPage(Number(route.params.code) || 500)
}
