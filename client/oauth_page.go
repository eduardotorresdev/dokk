package client

import (
	"html"
	"strings"
)

// CallbackPage é a página que o callback de loopback do `dokk login` mostra
// no navegador, na identidade do dokk. Autocontida (CSS e logo inline): o
// servidor local do cliente não tem os assets do painel. ok=false é a versão
// de erro/negado; msg (opcional) aparece como detalhe, escapado. Os textos
// saem em inglês e um script troca para es/pt-BR conforme o navegador.
func CallbackPage(ok bool, msg string) string {
	kind := "ok"
	if !ok {
		kind = "err"
	}
	detail := ""
	if msg = strings.TrimSpace(msg); msg != "" {
		detail = `<p class="detail">` + html.EscapeString(msg) + `</p>`
	}
	r := strings.NewReplacer("{{KIND}}", kind, "{{DETAIL}}", detail, "{{LOGO}}", logoSVG)
	return r.Replace(callbackHTML)
}

const logoSVG = `<svg class="logo" viewBox="0 0 40 40" aria-hidden="true"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#6b9bff"/><stop offset="1" stop-color="#1f4fe0"/></linearGradient><clipPath id="c"><rect width="40" height="40" rx="11"/></clipPath></defs><g clip-path="url(#c)"><rect width="40" height="40" fill="url(#g)"/><g fill="none" stroke="#fff" stroke-width="1.6" stroke-linecap="round"><path stroke-opacity=".75" d="M-40 32.5q2.5-2 5 0t5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0"/><path stroke-opacity=".4" d="M-37.5 36.5q2.5-2 5 0t5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0 5 0"/></g><g fill="#fff" fill-opacity=".7"><rect x="10" y="24" width="2.6" height="7.5" rx="1"/><rect x="27.4" y="24" width="2.6" height="7.5" rx="1"/></g></g><rect x="6" y="21.5" width="28" height="3.4" rx="1.7" fill="#fff"/><rect x="11.5" y="9" width="17" height="11" rx="1.8" fill="#fff"/><path d="M16.2 11.5v6M20 11.5v6M23.8 11.5v6" stroke="#1f4fe0" stroke-opacity=".55" stroke-width="1.4" stroke-linecap="round"/></svg>`

// Tokens copiados de ui/src/style.css (claro e escuro).
const callbackHTML = `<!doctype html>
<html lang="en" class="{{KIND}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>dokk</title>
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600;700&family=Geist+Mono:wght@400&display=swap">
<style>
:root{--bg:#f3f1ec;--card:#fdfcf9;--fg:#1a1917;--muted:#77726a;--border:#e0dcd3;--accent:#2f6bff;--soft:#e4ecff;--bad:#e5484d;--bad-soft:#fde4e4;--shadow:0 1px 2px rgb(40 30 10/.05),0 6px 20px -8px rgb(40 30 10/.12);color-scheme:light dark}
@media (prefers-color-scheme:dark){:root{--bg:#0c0c0e;--card:#121215;--fg:#f1f0ed;--muted:#8d8a85;--border:#24242a;--accent:#4d84ff;--soft:#13254d;--bad:#ff5c61;--bad-soft:#3a1517;--shadow:0 1px 0 rgb(255 255 255/.03) inset,0 6px 24px -10px rgb(0 0 0/.6)}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px 16px;background:var(--bg);color:var(--fg);font:14px/1.55 "Geist",ui-sans-serif,system-ui,sans-serif;-webkit-font-smoothing:antialiased}
main{width:100%;max-width:440px}
.brand{display:flex;align-items:center;gap:10px;margin:0 0 20px;font-weight:700;font-size:22px;letter-spacing:-.02em}
.logo{width:36px;height:36px;filter:drop-shadow(0 6px 14px rgb(47 107 255/.35))}
.card{background:var(--card);border:1px solid var(--border);border-radius:18px;box-shadow:var(--shadow);padding:28px}
.badge{width:44px;height:44px;border-radius:50%;display:grid;place-items:center;margin:0 0 16px;background:var(--soft);color:var(--accent)}
.err .badge{background:var(--bad-soft);color:var(--bad)}
.badge svg{width:22px;height:22px}
h1{margin:0 0 6px;font-size:22px;letter-spacing:-.02em}
p{margin:0;color:var(--muted)}
.detail{margin-top:14px;padding:10px 12px;border-radius:10px;background:var(--bg);border:1px solid var(--border);color:var(--fg);font:12.5px/1.5 "Geist Mono",ui-monospace,Menlo,monospace;overflow-wrap:anywhere}
.ok .e,.err .o{display:none}
</style>
</head>
<body>
<main>
<div class="brand">{{LOGO}}<span>dokk</span></div>
<section class="card" role="status">
<div class="badge"><svg class="o" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg><svg class="e" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><path d="M7 7l10 10M17 7 7 17"/></svg></div>
<h1 class="o" data-t="okTitle">Authorized</h1>
<p class="o" data-t="okText">You can go back to your terminal or assistant. This tab can be closed.</p>
<h1 class="e" data-t="errTitle">Not authorized</h1>
<p class="e" data-t="errText">Sign-in was not completed. Go back to your terminal to try again.</p>
{{DETAIL}}
</section>
</main>
<script>
(function(){
var T={es:{okTitle:"Autorizado",okText:"Puedes volver a tu terminal o asistente. Ya puedes cerrar esta pestaña.",errTitle:"No autorizado",errText:"El inicio de sesión no se completó. Vuelve a tu terminal para intentarlo de nuevo."},
pt:{okTitle:"Autorizado",okText:"Pode voltar ao terminal ou ao assistente. Esta aba já pode ser fechada.",errTitle:"Não autorizado",errText:"O login não foi concluído. Volte ao terminal para tentar de novo."}};
var langs=navigator.languages&&navigator.languages.length?navigator.languages:[navigator.language||"en"];
for(var i=0;i<langs.length;i++){var p=String(langs[i]).toLowerCase().split(/[-_]/)[0];if(p==="en")return;if(T[p]){var d=T[p];document.documentElement.lang=p==="pt"?"pt-BR":p;
document.querySelectorAll("[data-t]").forEach(function(el){el.textContent=d[el.getAttribute("data-t")]});return}}
})();
</script>
</body>
</html>
`
