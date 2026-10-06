#!/usr/bin/env node
// Gera site/demo/: a UI real do dokk (mesmo HTML, JS e CSS de ui/) rodando
// sem servidor. O site/demo-src/demo.js entra antes do main.js e simula a
// API inteira (fetch + EventSource) em memória.
//
//   npm ci --prefix ui     # traz o shablon
//   node site/build-demo.mjs
//
// Só usa a biblioteca padrão do Node. A pasta gerada fica fora do git.

import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const site = dirname(fileURLToPath(import.meta.url))
const root = dirname(site)
const ui = join(root, "ui")
const out = join(site, "demo")

const shablon = join(ui, "node_modules", "shablon")
if (!existsSync(join(shablon, "index.js"))) {
    console.error("build-demo: ui/node_modules/shablon não existe; rode `npm ci --prefix ui` antes.")
    process.exit(1)
}

rmSync(out, { recursive: true, force: true })
mkdirSync(out, { recursive: true })

// Código e assets da UI, sem alteração (o CSS ganha só o tema escuro fixo).
cpSync(join(ui, "src"), join(out, "src"), { recursive: true })
cpSync(join(ui, "assets"), join(out, "assets"), { recursive: true })
for (const f of ["index.js", "package.json", "LICENSE.md", "src"]) {
    if (existsSync(join(shablon, f))) cpSync(join(shablon, f), join(out, "node_modules", "shablon", f), { recursive: true })
}
cpSync(join(site, "demo-src", "demo.js"), join(out, "demo.js"))

// Tema escuro sempre: o bloco @media (prefers-color-scheme: dark) passa a
// valer em qualquer sistema.
const cssPath = join(out, "src", "style.css")
let css = readFileSync(cssPath, "utf8")
const darkQuery = "@media (prefers-color-scheme: dark)"
if (!css.includes(darkQuery)) throw new Error("build-demo: style.css sem o bloco de tema escuro")
css = css.replaceAll(darkQuery, "@media all").replace("color-scheme: light dark;", "color-scheme: dark;")
writeFileSync(cssPath, css)

// index.html: caminhos relativos (a demo vive em /dokk/demo/ no Pages),
// sem o preload da API, com o demo.js antes de tudo.
let html = readFileSync(join(ui, "index.html"), "utf8")
const must = (re, what) => { if (!re.test(html)) throw new Error(`build-demo: index.html mudou (${what})`) }
must(/<link rel="preload" href="\/api\/apps"[^>]*>/, "preload /api/apps")
must(/"shablon": "\/node_modules\/shablon\/index\.js"/, "import map")
must(/<meta charset="utf-8">/, "charset")
html = html
    .replace(/\s*<!--[^>]*-->/g, "")
    .replace(/\s*<link rel="preload" href="\/api\/apps"[^>]*>/, "")
    .replace(/\s*<meta name="theme-color" content="[^"]*" media="\(prefers-color-scheme: light\)">/, "")
    .replace(/<meta name="theme-color" content="([^"]*)" media="\(prefers-color-scheme: dark\)">/, '<meta name="theme-color" content="$1">')
    .replace('<meta name="color-scheme" content="light dark">', '<meta name="color-scheme" content="dark">')
    .replace(/"shablon": "\/node_modules\//, '"shablon": "./node_modules/')
    .replace(/(href|src)="\/(?!\/)/g, '$1="')
    .replace("<title>dokk</title>", "<title>dokk · demo</title>")
// O import map precisa vir antes de qualquer modulepreload: o demo.js
// (script síncrono) segura o parser e o preload scanner começaria a carregar
// os módulos sem o mapa.
const importMap = html.match(/\s*<script type="importmap">[\s\S]*?<\/script>/)[0]
html = html
    .replace(importMap, "")
    .replace('<meta charset="utf-8">', `<meta charset="utf-8">\n    <script src="demo.js"></script>${importMap}`)
if (/(href|src)="\/(?!\/)/.test(html)) throw new Error("build-demo: sobrou caminho absoluto no index.html")
writeFileSync(join(out, "index.html"), html)

console.log(`build-demo: ${out}`)
