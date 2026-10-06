// Confere os dicionários da UI (mesmas regras do ui_i18n_test.go): mesmos
// arquivos e chaves nos 3 idiomas, chaves no namespace do arquivo, sem
// duplicadas nem vazias, mesmos {parâmetros}, plurais .one + .other e toda
// chave estática usada em tr("...") / trn("...") / { key: "..." } existente
// em en; chave montada (tr(`prefixo.${x}`) ou "prefixo." + x) só nos
// prefixos de BUILT.
// Uso: node ui/scripts/check-i18n.mjs [diretório src]
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join, relative, resolve, dirname } from "node:path"
import { fileURLToPath, pathToFileURL } from "node:url"

const LANGS = ["en", "es", "pt-BR"]
const NAMESPACES = ["app", "auth", "common", "errors", "home", "onboarding", "registries"]
const BUILT = ["common.status.", "common.action.running.", "onboarding.job.step.", "onboarding.dokku.reason.", "errors."]

const src = resolve(process.argv[2] ?? join(dirname(fileURLToPath(import.meta.url)), "../src"))
const root = join(src, "locales")
const problems = []
const add = (s) => problems.push(s)

const entryRe = /^\s*"([^"\\]+)"\s*:\s*("(?:[^"\\]|\\.)*")\s*,?\s*$/
const keyRe = /^[a-z]+(\.[A-Za-z0-9_-]+)+$/
const params = (s) => [...new Set([...s.matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((m) => m[1]))].sort().join(",")

function listDir(dir) {
    try {
        return readdirSync(dir).sort()
    } catch (e) {
        add(`${relative(src, dir) || dir}: ${e.message}`)
        return []
    }
}

// Formato linha a linha (duplicadas só aparecem aqui; o import as esconde).
function checkLines(file, ns) {
    const seen = new Set()
    readFileSync(file, "utf8").split("\n").forEach((line, i) => {
        const trim = line.trim()
        if (trim === "" || trim.startsWith("//") || trim === "export default {" || trim === "}") return
        const where = `${relative(src, file)}:${i + 1}`
        const m = line.match(entryRe)
        if (!m) return add(`${where}: formato inesperado: ${trim}`)
        try {
            JSON.parse(m[2])
        } catch {
            return add(`${where}: formato inesperado (valor): ${trim}`)
        }
        if (seen.has(m[1])) add(`${where}: chave duplicada "${m[1]}"`)
        seen.add(m[1])
    })
}

const dicts = {}
const files = {}
for (const lang of LANGS) {
    dicts[lang] = {}
    files[lang] = []
    for (const name of listDir(join(root, lang))) {
        const file = join(root, lang, name)
        if (!name.endsWith(".js") || statSync(file).isDirectory()) {
            add(`${lang}/${name}: arquivo inesperado`)
            continue
        }
        const ns = name.slice(0, -3)
        files[lang].push(ns)
        checkLines(file, ns)
        let dict
        try {
            dict = (await import(pathToFileURL(file))).default
        } catch (e) {
            add(`${lang}/${name}: ${e.message}`)
            continue
        }
        for (const [k, v] of Object.entries(dict ?? {})) {
            if (!keyRe.test(k) || !k.startsWith(ns + ".")) add(`${lang}/${name}: chave "${k}" fora do namespace "${ns}"`)
            if (typeof v !== "string" || v.trim() === "") add(`${lang}/${name}: valor vazio em "${k}"`)
            dicts[lang][k] = String(v)
        }
    }
}
for (const name of listDir(root)) if (!LANGS.includes(name)) add(`locales/${name}: idioma inesperado`)

for (const lang of LANGS) {
    if (files[lang].join() !== files.en.join()) add(`${lang}: arquivos [${files[lang]}], en tem [${files.en}]`)
    for (const ns of NAMESPACES) if (!files[lang].includes(ns)) add(`${lang}: falta o namespace ${ns}`)
}
for (const lang of LANGS.slice(1)) {
    for (const k of Object.keys(dicts.en)) if (!(k in dicts[lang])) add(`${lang}: falta a chave "${k}"`)
    for (const [k, v] of Object.entries(dicts[lang])) {
        if (!(k in dicts.en)) add(`${lang}: chave "${k}" não existe em en`)
        else if (params(v) !== params(dicts.en[k])) add(`${lang}: "${k}" com parâmetros [${params(v)}], en tem [${params(dicts.en[k])}]`)
    }
}
for (const k of Object.keys(dicts.en)) {
    const base = k.slice(0, k.lastIndexOf("."))
    if (k.endsWith(".one") && !(base + ".other" in dicts.en)) add(`en: plural "${base}" sem .other`)
    if (k.endsWith(".other") && !(base + ".one" in dicts.en)) add(`en: plural "${base}" sem .one`)
}

// Chaves estáticas no código (fora de locales/).
function walk(dir) {
    for (const name of listDir(dir)) {
        const path = join(dir, name)
        if (statSync(path).isDirectory()) {
            if (path !== root && name !== "node_modules") walk(path)
            continue
        }
        if (!name.endsWith(".js")) continue
        const rel = relative(src, path)
        const code = readFileSync(path, "utf8")
        // Chave montada em template: tr(`common.status.${s}`).
        for (const [, fn, prefix] of code.matchAll(/\b(trn?)\(\s*`([^`$]*)\$\{/g)) {
            if (!BUILT.some((p) => prefix.startsWith(p))) add(`${rel}: ${fn}(\`${prefix}\${...}\`) monta chave fora dos conjuntos permitidos`)
        }
        // Erros guardados como { key: "..." } (resolvidos depois por errText).
        for (const [, key] of code.matchAll(/\bkey:\s*"([a-z]+\.[^"]+)"/g)) {
            if (!(key in dicts.en)) add(`${rel}: { key: "${key}" } não existe em en`)
        }
        for (const [, fn, key, plus] of code.matchAll(/\b(trn?)\(\s*["`]([^"`$]+)["`]\s*(\+?)/g)) {
            if (plus) {
                if (!BUILT.some((p) => key.startsWith(p))) add(`${rel}: ${fn}("${key}" + ...) monta chave fora dos conjuntos permitidos`)
            } else if (fn === "trn") {
                for (const cat of [".one", ".other"]) if (!(key + cat in dicts.en)) add(`${rel}: trn("${key}") sem ${cat} em en`)
            } else if (!(key in dicts.en)) {
                add(`${rel}: tr("${key}") não existe em en`)
            }
        }
    }
}
walk(src)

if (problems.length) {
    console.error(problems.sort().join("\n"))
    console.error(`\n${problems.length} problema(s) nos dicionários`)
    process.exit(1)
}
console.log(`i18n ok: ${Object.keys(dicts.en).length} chaves × ${LANGS.length} idiomas`)
