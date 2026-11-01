import { t, store } from "shablon"
import { tr } from "../i18n.js"
import { authLayout } from "./login.js"
import "./settings.js" // registra os textos settings.*

// Consentimento do OAuth (/oauth/authorize?…), no mesmo layout do login. O
// servidor já validou os parâmetros antes de servir a SPA e valida de novo no
// POST; a resposta diz para onde ir (o redirect de loopback do cliente, com
// code ou error).
export function oauthConsent() {
    const q = new URLSearchParams(location.search)
    const params = Object.fromEntries(["client_id", "redirect_uri", "code_challenge", "code_challenge_method", "state", "response_type"].map((k) => [k, q.get(k) || ""]))
    const client = params.client_id
    let host = params.redirect_uri
    try { host = new URL(params.redirect_uri).host } catch {}
    const ui = store({ busy: "", error: "", done: false })

    const answer = async (approve) => {
        if (ui.busy || ui.done) return
        ui.busy = approve ? "approve" : "deny"
        ui.error = ""
        try {
            const res = await fetch("/oauth/authorize", {
                method: "POST",
                headers: { "X-Dokk": "1", "Content-Type": "application/json" },
                body: JSON.stringify({ ...params, approve }),
            })
            const body = await res.json().catch(() => ({}))
            if (!res.ok || !body.redirect) throw new Error(body.error || res.statusText)
            ui.done = true
            location.assign(body.redirect)
        } catch (err) {
            ui.error = err.message
        } finally { ui.busy = "" }
    }
    const button = (approve) => t.button({
        type: "button", className: approve ? "btn btn-primary" : "btn",
        disabled: () => !!ui.busy || ui.done, onclick: () => answer(approve),
    }, () => ui.busy === (approve ? "approve" : "deny")
        ? [t.span({ className: "spinner" }), t.span({ textContent: () => tr("settings.oauth.busy") })]
        : [t.span({ textContent: () => tr(approve ? "settings.oauth.approve" : "settings.oauth.deny") })])

    const fact = (cls, text) => t.li({}, t.span({ className: `icon-dot ${cls}` }), text)

    return authLayout(t.div({ className: "login-form oauth-consent" },
        t.span({ className: "setup-step mono", textContent: () => tr("settings.oauth.step") }),
        t.h1({ className: "login-title-oauth", textContent: () => tr("settings.oauth.title", { client }) }),
        t.p({ className: "muted", textContent: () => tr("settings.oauth.text", { client }) }),
        t.ul({ className: "oauth-facts" },
            fact("warn", t.span({ textContent: () => tr("settings.oauth.warning") })),
            // Só o host em mono: o texto em volta vem do dicionário.
            fact("", t.span({}, () => {
                const [before, after = ""] = tr("settings.oauth.redirect", { host: "\u0000" }).split("\u0000")
                return [before, t.strong({ className: "oauth-host", textContent: host }), after]
            })),
            fact("", t.span({ textContent: () => tr("settings.oauth.revoke") })),
        ),
        t.p({ className: "form-error", role: "alert", hidden: () => !ui.error, textContent: () => ui.error }),
        t.p({ className: "oauth-done", hidden: () => !ui.done, textContent: () => tr("settings.oauth.done") }),
        t.div({ className: "oauth-actions" }, button(false), button(true)),
    ))
}
