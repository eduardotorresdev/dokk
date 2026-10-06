// dokk · demo ao vivo. Roda ANTES do main.js e troca o servidor por uma
// simulação em memória: window.fetch e EventSource respondem a toda rota
// /api/* com o mesmo formato JSON do dokk (main.go, onboarding.go,
// registries.go, prefs.go, auth.go). Nenhuma requisição sai do navegador.
//
// Parâmetros da URL (todos opcionais):
//   view=home|app|onboarding|registries   tela inicial (padrão: home)
//   app=<nome>                            app aberta em view=app (padrão: shop-api)
//   tab=history|versions|logs|vars|settings  aba aberta em view=app
//   lang=en|es|pt-BR                      idioma da UI (padrão: en)
//   autoplay=1                            com view=onboarding: tour automático em loop
(() => {
    "use strict"

    const params = new URLSearchParams(location.search)
    const normLang = (s) => ({ pt: "pt-BR", es: "es", en: "en" })[String(s || "").trim().toLowerCase().split(/[-_]/)[0]] || ""
    let LANG = normLang(params.get("lang")) || "en"
    const VIEW = params.get("view") || "home"
    const START_APP = params.get("app") || "shop-api"
    const START_TAB = params.get("tab") || ""
    const AUTOPLAY = params.get("autoplay") === "1" && (VIEW === "onboarding" || VIEW === "new-app")

    // ---------- endereço ----------

    // Os arquivos são relativos à pasta da demo (/dokk/demo/ no Pages). Um
    // <base> fixo nessa pasta mantém isso depois que a URL do documento vira
    // "/" logo abaixo.
    const dir = location.href.replace(/[?#].*$/, "").replace(/[^/]*$/, "")
    const base = document.createElement("base")
    base.href = dir
    document.head.prepend(base)

    // O router do dokk lê location.pathname ("/", "/apps/x"...). Dentro do
    // iframe a URL passa a ser a rota do app; nada disso vira requisição (a
    // Navigation API intercepta) e ninguém recarrega o iframe.
    const ROUTES = {
        home: "/",
        app: `/apps/${encodeURIComponent(START_APP)}`,
        onboarding: "/onboarding",
        "new-app": "/onboarding",
        registries: "/registries",
    }
    history.replaceState(null, "", ROUTES[VIEW] || "/")

    // ---------- armazenamento ----------

    // Os iframes da landing dividem a mesma origem: cada um guarda as chaves
    // do dokk só em memória, para um idioma não vazar para o outro.
    const mem = new Map([["dokk:lang", LANG], ["dokk:app-count", "6"]])
    try {
        const P = Storage.prototype
        const get = P.getItem, set = P.setItem, del = P.removeItem
        const own = (k) => String(k).startsWith("dokk:")
        P.getItem = function (k) { return own(k) ? (mem.has(k) ? mem.get(k) : null) : get.call(this, k) }
        P.setItem = function (k, v) { return own(k) ? void mem.set(k, String(v)) : set.call(this, k, v) }
        P.removeItem = function (k) { return own(k) ? void mem.delete(k) : del.call(this, k) }
    } catch {}

    // ---------- utilitários ----------

    const SEC = 1000, MIN = 60 * SEC, HOUR = 60 * MIN, DAY = 24 * HOUR
    const GiB = 1024 ** 3
    const now = () => Date.now()
    const iso = (t) => new Date(t).toISOString()
    const rand = (a, b) => a + Math.random() * (b - a)
    const pick = (list) => list[Math.floor(Math.random() * list.length)]
    const clamp = (v, a, b) => Math.max(a, Math.min(b, v))
    const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
    const hex = (n) => Array.from({ length: n }, () => "0123456789abcdef"[Math.floor(Math.random() * 16)]).join("")
    const DOMAIN = "example.com"

    // Mensagens de erro do servidor (i18n.go), no idioma ativo.
    const ERR = {
        en: { appNotFound: "app not found", actionRunning: "another action is already running on this app", invalidAppName: "invalid name: use lowercase letters, numbers and hyphens", invalidImage: "invalid image", appExists: "an app with this name already exists", alreadyDeployed: "the app is already deployed; change the version in the Versions tab", confirmMismatch: "confirmation does not match the app name", registryNotFound: "registry not found", invalidRegistryPassword: "invalid password or token" },
        es: { appNotFound: "app no encontrada", actionRunning: "ya hay una acción en curso en esta app", invalidAppName: "nombre inválido: usa letras minúsculas, números y guiones", invalidImage: "imagen inválida", appExists: "ya existe una app con ese nombre", alreadyDeployed: "la app ya tiene un deploy; cambia la versión en la pestaña Versiones", confirmMismatch: "la confirmación no coincide con el nombre de la app", registryNotFound: "registry no encontrado", invalidRegistryPassword: "contraseña o token inválido" },
        "pt-BR": { appNotFound: "app não encontrada", actionRunning: "já existe uma ação em andamento nesta app", invalidAppName: "nome inválido: use letras minúsculas, números e hífen", invalidImage: "imagem inválida", appExists: "já existe uma app com esse nome", alreadyDeployed: "a app já tem deploy; troque a versão pela aba Versões", confirmMismatch: "confirmação não confere com o nome da app", registryNotFound: "registry não encontrado", invalidRegistryPassword: "senha ou token inválido" },
    }
    const err = (status, key) => ({ status, body: { error: (ERR[LANG] || ERR.en)[key] || key } })

    const APP_NAME = /^[a-z0-9][a-z0-9-]*$/
    const IMAGE = /^[a-z0-9][a-z0-9._\-/:@]*$/i
    const validImage = (s) => typeof s === "string" && IMAGE.test(s) && !s.endsWith(":") && !s.endsWith("/")
    const repoOf = (image) => image.split("@")[0].replace(/:[^/:]*$/, "")
    const tagOf = (image) => (image.split("@")[0].match(/:([^/:]+)$/) || [])[1] || "latest"

    // ---------- estado: apps ----------

    // Uma app de exemplo: o que o monitor do dokk montaria a partir dos
    // relatórios do Dokku e do docker.
    const SEED = [
        {
            name: "shop-api", image: "ghcr.io/acme/api:1.8.2", domains: ["api.example.com"], procs: { web: 2 }, port: 5000,
            deployedAgo: 2 * HOUR + 14 * MIN, cpu: 7.5, mem: 6.1, kind: "api",
            env: { DATABASE_URL: "postgres://shop:9f2c7e1a@shop-db.internal:5432/shop", REDIS_URL: "redis://cache.internal:6379/0", JWT_SECRET: "c0ffee-5ecret-7d1a", LOG_LEVEL: "info", SENTRY_DSN: "https://4f1e@o12.ingest.sentry.io/77" },
            versions: ["1.8.2", "1.8.1", "1.8.0", "1.7.4", "1.7.3", "1.7.0", "1.6.2"],
        },
        {
            name: "shop-web", image: "ghcr.io/acme/web:3.4.0", domains: ["store.example.com", "www.store.example.com"], procs: { web: 2 }, port: 3000,
            deployedAgo: 26 * HOUR, cpu: 4.2, mem: 5.3, kind: "web",
            env: { API_URL: "http://shop-api.web:5000", NEXT_PUBLIC_SITE_URL: "https://store.example.com", NODE_ENV: "production" },
            services: [{ app: "shop-api", vars: ["API_URL"] }],
            versions: ["3.4.0", "3.3.2", "3.3.1", "3.3.0", "3.2.0"],
        },
        {
            name: "shop-worker", image: "ghcr.io/acme/worker:1.8.2", domains: [], ssl: false, procs: { worker: 3 }, port: 0,
            deployedAgo: 2 * HOUR + 11 * MIN, cpu: 12.8, mem: 8.4, kind: "worker",
            env: { API_INTERNAL_URL: "http://shop-api.web:5000", DATABASE_URL: "postgres://shop:9f2c7e1a@shop-db.internal:5432/shop", QUEUE_CONCURRENCY: "8" },
            services: [{ app: "shop-api", vars: ["API_INTERNAL_URL"] }],
            versions: ["1.8.2", "1.8.1", "1.8.0", "1.7.4"],
            blips: ["degraded"],
        },
        {
            name: "docs", image: "ghcr.io/acme/docs:2026.10.1", domains: ["docs.example.com"], procs: { web: 1 }, port: 80,
            deployedAgo: 3 * DAY + 5 * HOUR, cpu: 0.4, mem: 0.9, kind: "static",
            env: { NGINX_GZIP: "on" },
            versions: ["2026.10.1", "2026.09.3", "2026.09.2", "2026.09.1"],
        },
        {
            name: "status-page", image: "louislam/uptime-kuma:1.23.13", domains: ["status.example.com"], procs: { web: 1 }, port: 3001,
            deployedAgo: 12 * DAY, cpu: 1.6, mem: 2.2, kind: "status",
            env: { UPTIME_KUMA_PORT: "3001", TZ: "UTC" },
            storage: [{ host: "/var/lib/dokku/data/storage/status-page/data", container: "/app/data", readonly: false }],
            versions: ["1.23.13", "1.23.12", "1.23.11", "1.23.10"],
        },
        {
            name: "grafana", image: "grafana/grafana:11.2.0", domains: ["grafana.example.com"], procs: { web: 1 }, port: 3000,
            deployedAgo: 6 * DAY + 3 * HOUR, cpu: 2.1, mem: 3.4, kind: "grafana",
            env: { GF_SERVER_ROOT_URL: "https://grafana.example.com", GF_SECURITY_ADMIN_PASSWORD: "s3cr3t-gr4f4n4" },
            storage: [{ host: "/var/lib/dokku/data/storage/grafana/data", container: "/var/lib/grafana", readonly: false }],
            versions: ["11.2.0", "11.1.4", "11.1.3", "11.0.1"],
        },
    ]

    const HISTORY_SIZE = 24
    const apps = new Map() // nome → app
    const actions = new Map() // nome → Action (a última, como o actionLog do servidor)
    let buildSeq = 1000

    function makeApp(s) {
        const deployedAt = now() - s.deployedAgo
        const a = {
            name: s.name,
            image: s.image,
            deployed: true,
            deployedAt,
            createdAt: deployedAt - rand(5, 40) * DAY,
            domains: s.domains,
            ssl: s.ssl ?? s.domains.length > 0,
            procs: Object.fromEntries(Object.entries(s.procs).map(([k, n]) => [k, { running: n, total: n }])),
            port: s.port,
            status: "healthy",
            history: [],
            lastCheck: now() - rand(1, 9) * SEC,
            usage: { cpu: s.cpu, mem: s.mem, baseCpu: s.cpu, baseMem: s.mem },
            kind: s.kind,
            env: { ...(s.port ? { PORT: String(s.port) } : {}), ...s.env, DOKKU_APP_TYPE: "dockerfile" },
            services: s.services || [],
            storage: s.storage || [],
            tags: (s.versions || [tagOf(s.image)]).map((tag, i) => ({ tag, updated: deployedAt - 20 * MIN - i * rand(2, 9) * DAY })),
            builds: [],
            busy: "",
            sslExpires: now() + rand(38, 81) * DAY,
        }
        // Histórico dos checks: quase tudo saudável, com o último deploy
        // aparecendo como reinício e alguns soluços antigos.
        for (let i = 0; i < HISTORY_SIZE; i++) a.history.push("healthy")
        if (s.deployedAgo < 3 * HOUR) a.history.splice(HISTORY_SIZE - 9, 2, "restarting", "starting")
        for (const b of s.blips || []) a.history[Math.floor(rand(3, 12))] = b
        // Builds antigos: o deploy atual, um restart e deploys anteriores.
        const tagIdx = (a.tags.findIndex((t) => t.tag === tagOf(a.image)) + 1) || 1
        const past = [
            { source: "git:from-image", image: a.image, at: deployedAt },
            { source: "ps:restart", at: deployedAt - rand(4, 20) * HOUR },
            ...a.tags.slice(tagIdx, tagIdx + 3).map((t, i) => ({ source: "git:from-image", image: `${repoOf(a.image)}:${t.tag}`, at: t.updated + (i + 1) * 30 * MIN })),
            { source: "ps:rebuild", at: deployedAt - rand(15, 30) * DAY },
        ].sort((x, y) => y.at - x.at)
        for (const p of past) {
            const dur = p.source === "git:from-image" ? rand(28, 75) * SEC : rand(9, 20) * SEC
            a.builds.push(makeBuild(a, p.source, p.image, p.at - dur, dur, true))
        }
        return a
    }

    // Um registro de build como o que o Dokku grava em data/builds/<app>/<id>.json,
    // com o roteiro do log (linhas e quando cada uma aparece).
    function makeBuild(a, source, image, startedAt, dur, finished) {
        const id = `${Math.floor(startedAt / 1000)}-${(buildSeq++).toString(36)}`
        const script = source === "git:from-image" ? deployScript(a, image || a.image, dur) : restartScript(a, source, dur)
        return {
            id,
            kind: source === "git:from-image" ? "deploy" : source.replace("ps:", ""),
            source,
            status: finished ? "succeeded" : "running",
            started_at: startedAt,
            finished_at: finished ? startedAt + dur : null,
            exit_code: finished ? 0 : null,
            image: image || null,
            script,
        }
    }

    const buildJSON = (b) => ({
        id: b.id, kind: b.kind, status: b.status, source: b.source,
        started_at: iso(b.started_at), finished_at: b.finished_at ? iso(b.finished_at) : null, exit_code: b.exit_code,
    })

    // Log de um git:from-image, com as marcas que o onboarding usa para as
    // fases (pulling → building → releasing/deploying → checks → deployed).
    function deployScript(a, image, dur) {
        const name = a.name
        const repo = repoOf(image)
        const tag = tagOf(image)
        const k = dur / 13500 // escala o roteiro para a duração do build
        const at = (ms) => Math.round(ms * k)
        const layers = Array.from({ length: 5 }, () => hex(12))
        const L = []
        const add = (ms, ...lines) => lines.forEach((l, i) => L.push([at(ms + i * 90), l]))
        add(0, `-----> Pulling image ${image}`)
        add(500, `${tag}: Pulling from ${repo.replace(/^[^/]*\.[^/]*\//, "")}`)
        layers.forEach((l, i) => add(800 + i * 220, `${l}: Pulling fs layer`))
        layers.forEach((l, i) => add(1600 + i * 260, `${l}: Download complete`))
        layers.forEach((l, i) => add(2300 + i * 150, `${l}: Pull complete`))
        add(3100, `Digest: sha256:${hex(64)}`, `Status: Downloaded newer image for ${image}`)
        add(3600, `-----> Building ${name} from Dockerfile`)
        add(3900, "#0 building with \"default\" instance using docker driver", "#1 [internal] load build definition from Dockerfile", "#1 transferring dockerfile: 86B done", "#1 DONE 0.0s")
        add(4700, `#2 [internal] load metadata for ${image}`, "#2 DONE 0.0s")
        add(5300, `#3 [1/1] FROM ${image}`, "#3 CACHED")
        add(5900, "#4 exporting to image", "#4 exporting layers done", `#4 writing image sha256:${hex(64)} done`, `#4 naming to docker.io/dokku/${name}:latest done`, "#4 DONE 0.1s")
        add(7000, `-----> Releasing ${name}...`)
        add(7400, `-----> Checking for predeploy task`, `       No predeploy task found, skipping`)
        add(7900, `-----> Deploying ${name} via the docker-local scheduler...`)
        add(8300, `-----> Deploying ${Object.keys(a.procs)[0] || "web"} (count=${Object.values(a.procs)[0]?.total || 1})`, `       Starting container ${name}.${Object.keys(a.procs)[0] || "web"}.1`)
        add(9300, "=====> Processing deployment checks")
        add(9700, `       Attempting pre-flight checks (${Object.keys(a.procs)[0] || "web"}.1)`, `       Waiting for 5 seconds (${Object.keys(a.procs)[0] || "web"}.1)`)
        add(11200, `       Healthcheck succeeded name='port listening check' (${Object.keys(a.procs)[0] || "web"}.1)`, `       All checks successful (${Object.keys(a.procs)[0] || "web"}.1)`)
        add(11800, `-----> Running post-deploy`, `-----> Checking for postdeploy task`, `       No postdeploy task found, skipping`)
        if (a.domains.length) add(12300, `-----> Configuring ${a.domains[0]}...(using built-in template)`, a.ssl ? "-----> Creating https nginx.conf" : "-----> Creating http nginx.conf", "       Reloading nginx")
        add(12900, "-----> Renaming containers", `       Renaming container ${name}.web.1 (${hex(12)}) to ${name}.web.1.${Math.floor(now() / 1000)}`)
        add(13300, "=====> Application deployed:", ...(a.domains.length ? a.domains.map((d) => `       ${a.ssl ? "https" : "http"}://${d}`) : [`       (no domains: ${name} is an internal service)`]))
        return L
    }

    function restartScript(a, source, dur) {
        const name = a.name
        const k = dur / 9000
        const L = []
        const add = (ms, ...lines) => lines.forEach((l, i) => L.push([Math.round((ms + i * 90) * k), l]))
        const verb = source === "ps:rebuild" ? "Rebuilding" : source === "ps:start" ? "Starting" : "Restarting"
        add(0, `-----> ${verb} ${name}`)
        add(600, `-----> Releasing ${name}...`)
        Object.entries(a.procs).forEach(([p, v], i) => add(1500 + i * 400, `-----> Deploying ${p} (count=${v.total})`))
        add(3000, "=====> Processing deployment checks")
        add(3600, "       Attempting pre-flight checks (web.1)", "       Waiting for 5 seconds (web.1)")
        add(6800, "       All checks successful (web.1)")
        add(7600, "-----> Running post-deploy", "       Reloading nginx")
        add(8600, `=====> Application ${source === "ps:start" ? "started" : "restarted"}`)
        return L
    }

    const buildText = (b) => {
        const elapsed = b.status === "running" ? now() - b.started_at : Infinity
        return b.script.filter(([t]) => t <= elapsed).map(([, l]) => l).join("\n") + "\n"
    }

    function appJSON(a) {
        const out = {
            name: a.name,
            deployed: a.deployed,
            locked: a.busy === "deploy" || a.busy === "install",
            "deploy-source": a.deployed || a.image ? "docker-image" : "",
            "git-sha": a.deployed ? a.image : "",
            "last-deploy-at": a.deployed ? iso(a.deployedAt) : null,
            "proxy-type": "nginx",
            domains: a.domains,
            ssl: a.ssl,
            processes: a.deployed ? a.procs : {},
            checks: { status: a.status, "last-check-at": a.deployed ? iso(a.lastCheck) : null, history: a.history },
        }
        if (a.deployed && a.status !== "suspended") out.usage = { cpu: round(a.usage.cpu, 2), mem: round(a.usage.mem, 2) }
        if (a.busy) out.action = a.busy
        return out
    }
    const round = (v, n) => Math.round(v * 10 ** n) / 10 ** n

    const appsJSON = () => [...apps.values()].map(appJSON)

    // Página interna: containers, portas, volumes, SSL, variáveis, builds e ligações.
    function detailJSON(a) {
        const containers = []
        if (a.deployed && a.status !== "suspended") {
            for (const [p, v] of Object.entries(a.procs)) {
                for (let i = 1; i <= v.running; i++) {
                    const cpu = Math.max(0.01, a.usage.cpu / v.running * rand(0.8, 1.2) * 4)
                    const memMiB = a.usage.mem / 100 * 7.76 * 1024 / v.running * rand(0.95, 1.05)
                    const starting = a.status === "starting" || a.status === "restarting"
                    containers.push({
                        name: `${a.name}.${p}.${i}`,
                        process: p,
                        state: "running",
                        status: starting ? "Up 2 seconds (health: starting)" : `Up ${upFor(a.deployedAt)}`,
                        image: `dokku/${a.name}:latest`,
                        created: iso(a.deployedAt),
                        cpu: `${cpu.toFixed(2)}%`,
                        memory: `${memMiB.toFixed(1)}MiB / 7.76GiB`,
                        "mem-perc": `${(memMiB / (7.76 * 1024) * 100).toFixed(2)}%`,
                        retiring: false,
                    })
                }
            }
            // Durante um deploy o container antigo aparece saindo.
            if (a.busy === "deploy" || a.busy === "install" || a.busy === "restart") {
                const p = Object.keys(a.procs)[0]
                containers.push({
                    name: `${a.name}.${p}.1.${Math.floor(now() / 1000)}`, process: p, state: "running", status: "Up 3 hours",
                    image: `dokku/${a.name}:latest`, created: iso(a.deployedAt - 3 * HOUR), cpu: "0.08%", memory: "61.2MiB / 7.76GiB", "mem-perc": "0.77%", retiring: true,
                })
            }
        }
        const usedBy = [...apps.values()]
            .filter((o) => o !== a)
            .flatMap((o) => o.services.filter((l) => l.app === a.name).map((l) => ({ app: o.name, vars: l.vars })))
        return {
            containers,
            ports: a.port && a.domains.length ? [`http:80:${a.port}`, ...(a.ssl ? [`https:443:${a.port}`] : [])] : [],
            storage: a.storage,
            ssl: a.ssl
                ? { enabled: true, "expires-at": iso(a.sslExpires), issuer: "Let's Encrypt R11", hostnames: a.domains }
                : { enabled: false, "expires-at": "", issuer: "", hostnames: [] },
            "config-keys": Object.keys(a.env).sort(),
            builds: a.builds.slice(0, 20).map(buildJSON),
            services: a.services.filter((l) => apps.has(l.app)),
            "used-by": usedBy,
            networks: ["bridge"],
        }
    }

    function upFor(t) {
        const s = (now() - t) / 1000
        if (s < 60) return `${Math.max(1, Math.round(s))} seconds`
        if (s < 3600) return `${Math.round(s / 60)} minutes`
        if (s < 86400) return `${Math.round(s / 3600)} hours`
        return `${Math.round(s / 86400)} days`
    }

    // ---------- estado: host ----------

    const host = {
        cores: 4,
        per: [22, 14, 31, 9],
        memTotal: 7.76 * GiB,
        memUsed: 3.42 * GiB,
        diskTotal: 77.4 * GiB,
        diskUsed: 31.6 * GiB,
    }
    // Passeio aleatório com volta à média: oscila sem pular.
    function stepHost() {
        const busy = [...apps.values()].some((a) => a.busy)
        const t = now() / 1000
        host.per = host.per.map((v, i) => {
            const target = 18 + 9 * Math.sin(t / 23 + i * 1.9) + (busy ? 28 * (i === 1 || i === 2 ? 1 : 0.4) : 0)
            return clamp(v + (target - v) * 0.45 + rand(-6, 6), 2, 97)
        })
        const memTarget = 3.4 * GiB + 0.18 * GiB * Math.sin(t / 37) + (busy ? 0.55 * GiB : 0)
        host.memUsed = clamp(host.memUsed + (memTarget - host.memUsed) * 0.4 + rand(-0.03, 0.03) * GiB, 2 * GiB, 7 * GiB)
        host.diskUsed += rand(0, 0.004) * GiB + (busy ? 0.03 * GiB : 0)
    }
    function hostJSON() {
        stepHost()
        const cpu = host.per.reduce((s, v) => s + v, 0) / host.per.length
        return {
            cpu: round(cpu, 2), "per-core": host.per.map((v) => round(v, 2)), cores: host.cores, "cores-used": round(cpu / 100 * host.cores, 2),
            "mem-used": Math.round(host.memUsed), "mem-total": Math.round(host.memTotal),
            "disk-used": Math.round(host.diskUsed), "disk-total": Math.round(host.diskTotal), at: iso(now()),
        }
    }

    // ---------- estado: registries, prefs ----------

    let registries = [
        { server: "docker.io", username: "acmebot" },
        { server: "ghcr.io", username: "ana-acme" },
    ]
    let autoRenew = true

    // ---------- eventos (SSE /api/events) ----------

    const listeners = new Set()
    let pushTimer = 0
    // Agrupa mudanças próximas numa só mensagem, como uma coleta do monitor.
    const changed = () => {
        if (pushTimer) return
        pushTimer = setTimeout(() => {
            pushTimer = 0
            const data = JSON.stringify(appsJSON())
            for (const fn of listeners) fn(data)
        }, 60)
    }

    // ---------- ações em segundo plano ----------

    // Mesma vida de uma ação do servidor: registra, roda, termina. O status
    // da app segue o que o monitor veria em cada fase.
    function runAction(a, name, { ms = 8000, source = null, image = null, onDone = null } = {}) {
        const cur = actions.get(a.name)
        if (cur?.running) return null
        const startedAt = now()
        const act = { name, running: true, "started-at": iso(startedAt) }
        actions.set(a.name, act)
        a.busy = name
        let build = null
        if (source) {
            build = makeBuild(a, source, image, startedAt, ms, false)
            a.builds.unshift(build)
        }
        const restarts = ["restart", "deploy", "install", "config", "rename"].includes(name)
        if (restarts && a.deployed) a.status = "restarting"
        if (name === "deploy" && !a.deployed) a.status = "starting"
        if (name === "start") a.status = "starting"
        if (name === "destroy") a.status = "removing"
        changed()
        if (restarts && a.deployed) {
            setTimeout(() => { if (a.busy === name) { a.status = "starting"; changed() } }, ms * 0.72)
        }
        setTimeout(() => {
            if (build) {
                build.status = "succeeded"
                build.finished_at = now()
                build.exit_code = 0
            }
            a.busy = ""
            act.running = false
            act["finished-at"] = iso(now())
            if (name === "destroy") {
                apps.delete(a.name)
                changed()
                return
            }
            if (name === "stop") {
                a.status = "suspended"
                for (const p of Object.values(a.procs)) p.running = 0
            } else if (name !== "ssl-renew") {
                for (const p of Object.values(a.procs)) p.running = p.total
                a.status = "starting"
                setTimeout(() => { if (!a.busy && a.status === "starting") { a.status = "healthy"; changed() } }, 2500)
            }
            if (source === "git:from-image" || name === "restart" || name === "start" || name === "config") a.lastCheck = now()
            onDone?.()
            changed()
        }, ms)
        return { ...act }
    }

    // Um deploy por imagem: troca a versão quando termina.
    function deployImage(a, image, name, ms) {
        return runAction(a, name, {
            ms, source: "git:from-image", image,
            onDone: () => {
                a.image = image
                a.deployed = true
                a.deployedAt = now()
                if (!a.tags.some((t) => t.tag === tagOf(image))) a.tags.unshift({ tag: tagOf(image), updated: now() - 12 * MIN })
            },
        })
    }

    // ---------- monitor: o painel "respira" ----------

    // A cada coleta: uso de CPU/memória de cada app oscila e o histórico de
    // checks anda uma casa.
    let ticks = 0
    setInterval(() => {
        ticks++
        for (const a of apps.values()) {
            if (!a.deployed) continue
            const u = a.usage
            const boost = a.busy ? 2.2 : 1
            u.cpu = clamp(u.cpu + (u.baseCpu * boost - u.cpu) * 0.35 + rand(-0.25, 0.25) * u.baseCpu, 0.05, 90)
            u.mem = clamp(u.mem + (u.baseMem - u.mem) * 0.3 + rand(-0.04, 0.04) * u.baseMem, 0.1, 60)
            if (a.status === "suspended") continue
            a.lastCheck = now()
            if (ticks % 2 === 0) {
                a.history.push(a.status)
                if (a.history.length > HISTORY_SIZE) a.history.shift()
            }
        }
        changed()
    }, 5000)

    // De tempos em tempos uma app recebe deploy (versão nova) ou reinicia,
    // para a home mostrar a transição ao vivo.
    let lastAuto = ""
    const autoDeploy = () => {
        const candidates = [...apps.values()].filter((a) => a.deployed && !a.busy && a.status === "healthy" && a.name !== lastAuto && a.name !== "shop")
        const a = pick(candidates)
        if (a) {
            lastAuto = a.name
            if (/^ghcr\.io\/acme\//.test(a.image) && Math.random() < 0.6) {
                const tag = tagOf(a.image)
                const next = /^\d+\.\d+\.\d+$/.test(tag) ? tag.replace(/\d+$/, (n) => String(Number(n) + 1)) : tag
                deployImage(a, `${repoOf(a.image)}:${next}`, "deploy", rand(9, 12) * SEC)
            } else {
                runAction(a, "restart", { ms: rand(6, 9) * SEC, source: "ps:restart" })
            }
        }
        setTimeout(autoDeploy, rand(22, 34) * SEC)
    }
    setTimeout(autoDeploy, rand(9, 14) * SEC)

    for (const s of SEED) apps.set(s.name, makeApp(s))

    // ---------- API (fetch) ----------

    const ok = (body, status = 200) => ({ status, body })

    function onboardingStatus() {
        return {
            needed: false,
            done: true,
            "dokku-seen": true,
            dokku: { installed: true, version: "0.35.12" },
            host: { os: "ubuntu", "os-version": "24.04", arch: "amd64", root: true, "can-install": true, method: "bootstrap" },
            "global-domains": [DOMAIN],
            "suggested-domain": DOMAIN,
            letsencrypt: true,
            "letsencrypt-email": true,
            apps: apps.size,
            registries: registries.length,
            job: null,
        }
    }

    function releasesJSON(a) {
        const repo = repoOf(a.image)
        return {
            image: repo,
            current: a.image,
            releases: a.tags.map((t) => ({ tag: t.tag, image: `${repo}:${t.tag}`, updated: iso(t.updated) })),
        }
    }

    function versionsJSON(a) {
        return a.builds
            .filter((b) => b.source === "git:from-image" && b.image)
            .map((b, i) => ({
                sha: hex(40),
                date: iso(b.started_at + 4 * SEC),
                author: "Dokku",
                message: `Deploy image ${b.image}`,
                image: b.image,
                current: i === 0,
            }))
    }

    function handle(method, path, query, body) {
        let m
        if (path === "/api/session" && method === "GET") return ok({ lang: LANG, name: "Ana", email: "ana@acme.dev" })
        if (path === "/api/prefs" && method === "PUT") {
            if (!normLang(body?.lang)) return { status: 400, body: { error: "invalid language" } }
            LANG = body.lang
            return ok({ lang: LANG })
        }
        if (path === "/api/login" || path === "/api/setup") return ok({ name: "Ana", email: "ana@acme.dev" })
        if (path === "/api/logout") return ok({})
        if (path === "/api/host") return ok(hostJSON())
        if (path === "/api/onboarding/seen") return ok({ "dokku-seen": true })
        if (path === "/api/onboarding" && method === "GET") return ok(onboardingStatus())
        if (path === "/api/onboarding/done") return ok({ done: true })
        if (path === "/api/onboarding/dokku-seen") return ok({ "dokku-seen": true })
        if (path === "/api/onboarding/job") return ok({ job: null, lines: [], next: 0, truncated: false })
        if (path.startsWith("/api/onboarding/")) return { status: 409, body: { error: "Dokku is already installed" } }
        if (path === "/api/ssl/autorenew") {
            if (method === "PUT") autoRenew = !!body?.enabled
            return ok({ enabled: autoRenew })
        }
        if (path === "/api/registries") {
            if (method === "POST") {
                const server = String(body?.server || "").trim().toLowerCase().replace(/^https?:\/\//, "").replace(/\/+$/, "").replace(/^(index\.)?docker\.io$|^registry-1\.docker\.io$|^hub\.docker\.com$/, "docker.io")
                if (!body?.password) return err(400, "invalidRegistryPassword")
                registries = [...registries.filter((r) => r.server !== server), { server, username: String(body.username || "").trim() }]
            }
            if (method === "DELETE") {
                const server = query.get("server")
                if (!registries.some((r) => r.server === server)) return err(404, "registryNotFound")
                registries = registries.filter((r) => r.server !== server)
            }
            return ok({ registries })
        }
        if (path === "/api/apps") {
            if (method === "GET") return ok(appsJSON())
            if (method === "POST") return createApp(body || {})
        }
        if ((m = path.match(/^\/api\/apps\/([^/]+)(?:\/(.*))?$/))) {
            const name = decodeURIComponent(m[1])
            const rest = m[2] || ""
            const a = apps.get(name)
            if (!a) return err(404, "appNotFound")
            return appRoute(a, method, rest, query, body)
        }
        return { status: 404, body: { error: "not found" } }
    }

    function createApp(body) {
        const name = String(body.name || "")
        const image = String(body.image || "")
        if (!APP_NAME.test(name) || name.length > 63) return err(400, "invalidAppName")
        if (image && !validImage(image)) return err(400, "invalidImage")
        if (apps.has(name)) return err(409, "appExists")
        const a = {
            name, image, deployed: false, deployedAt: 0, createdAt: now(),
            domains: [`${name}.${DOMAIN}`], ssl: false,
            procs: { web: { running: 1, total: 1 } }, port: Number(body.port) || 5000,
            status: "unknown", history: [], lastCheck: now(),
            usage: { cpu: 0.6, mem: 1.4, baseCpu: rand(1, 4), baseMem: rand(1.5, 4) },
            kind: "api", env: { ...(body.env || {}), ...(body.port ? { PORT: String(body.port) } : {}) },
            services: [], storage: [], tags: [{ tag: tagOf(image), updated: now() - 9 * MIN }, { tag: "1.9.4", updated: now() - 6 * DAY }, { tag: "1.9.3", updated: now() - 13 * DAY }],
            builds: [], busy: "", sslExpires: now() + 90 * DAY,
        }
        apps.set(name, a)
        changed()
        if (!image) return ok({ name, action: null }, 201)
        const action = runAction(a, "deploy", {
            ms: 13600, source: "git:from-image", image,
            onDone: () => {
                a.deployed = true
                a.deployedAt = now()
                a.ssl = !!body.ssl
            },
        })
        return ok({ name, action }, 202)
    }

    function appRoute(a, method, rest, query, body) {
        const run = (r) => r ? ok(r, 202) : err(409, "actionRunning")
        if (rest === "" && method === "GET") return ok({ app: appJSON(a), detail: detailJSON(a), action: actions.get(a.name) ? { ...actions.get(a.name) } : null })
        if (rest === "" && method === "DELETE") {
            if (body?.confirm !== a.name) return err(400, "confirmMismatch")
            return run(runAction(a, "destroy", { ms: 5200 }))
        }
        if (rest === "config" && method === "GET") return ok({ ...a.env })
        let m
        if ((m = rest.match(/^config\/([A-Za-z_][A-Za-z0-9_]*)$/))) {
            const key = m[1]
            const restart = method === "PUT" ? !!body?.restart : query.get("restart") === "1"
            if (method === "PUT") a.env[key] = String(body?.value ?? "")
            else if (method === "DELETE") delete a.env[key]
            if (!restart) return ok({})
            return run(runAction(a, "config", { ms: 7000, source: "ps:restart" }))
        }
        if (rest === "versions") return ok(versionsJSON(a))
        if (rest === "releases") return ok(releasesJSON(a))
        if (rest === "install" && method === "POST") {
            const image = String(body?.image || "")
            if (!validImage(image) || repoOf(image) !== repoOf(a.image)) return { status: 400, body: { error: "invalid image" } }
            return run(deployImage(a, image, "install", 10500))
        }
        if (rest === "deploy" && method === "POST") {
            const image = String(body?.image || "")
            if (!validImage(image)) return err(400, "invalidImage")
            if (a.deployed && a.status === "healthy") return err(409, "alreadyDeployed")
            return run(deployImage(a, image, "deploy", 13600))
        }
        if (rest === "rename" && method === "POST") {
            const to = String(body?.to || "")
            if (!APP_NAME.test(to)) return err(400, "invalidAppName")
            if (apps.has(to)) return err(409, "appExists")
            return run(runAction(a, "rename", {
                ms: 6000,
                onDone: () => {
                    apps.delete(a.name)
                    actions.delete(a.name)
                    for (const o of apps.values()) for (const l of o.services) if (l.app === a.name) l.app = to
                    a.name = to
                    apps.set(to, a)
                },
            }))
        }
        if ((m = rest.match(/^builds\/([^/]+)\/log$/))) {
            const b = a.builds.find((x) => x.id === m[1])
            return b ? { status: 200, text: buildText(b) } : { status: 404, body: { error: "log not found" } }
        }
        if (["start", "stop", "restart", "ssl-renew"].includes(rest) && method === "POST") {
            const ms = { start: 6500, stop: 4500, restart: 8000, "ssl-renew": 6000 }[rest]
            const source = { start: "ps:start", restart: "ps:restart" }[rest] || null
            return run(runAction(a, rest, { ms, source, onDone: rest === "ssl-renew" ? () => { a.ssl = true; a.sslExpires = now() + 90 * DAY } : null }))
        }
        return err(404, "appNotFound")
    }

    const realFetch = window.fetch.bind(window)
    window.fetch = async function demoFetch(input, init = {}) {
        const raw = String(input?.url ?? input)
        const url = new URL(raw, location.href)
        const method = String(init.method || input?.method || "GET").toUpperCase()
        if (url.origin === location.origin && url.pathname.startsWith("/api/")) {
            let body = null
            try { body = init.body ? JSON.parse(init.body) : null } catch {}
            // Latência de rede de verdade, curta.
            await sleep(rand(35, 140))
            const res = handle(method, url.pathname, url.searchParams, body)
            if (res.text !== undefined) {
                return new Response(res.text, { status: res.status, headers: { "Content-Type": "text/plain; charset=utf-8" } })
            }
            return new Response(JSON.stringify(res.body ?? {}), { status: res.status, headers: { "Content-Type": "application/json" } })
        }
        // Arquivos da própria demo podem ser lidos; qualquer outra coisa não sai daqui.
        const own = new URL(dir)
        if (url.origin === own.origin && url.pathname.startsWith(own.pathname)) return realFetch(input, init)
        throw new TypeError(`dokk demo: request blocked (${url.href})`)
    }

    // ---------- EventSource ----------

    class DemoEventSource extends EventTarget {
        static CONNECTING = 0
        static OPEN = 1
        static CLOSED = 2
        constructor(url) {
            super()
            const u = new URL(String(url), location.href)
            this.url = u.href
            this.withCredentials = false
            this.readyState = 0
            this.onopen = this.onmessage = this.onerror = null
            this._stop = null
            setTimeout(() => {
                if (this.readyState === 2) return
                const stop = stream(u.pathname, this)
                if (!stop) {
                    this.readyState = 2
                    this._emit("error")
                    return
                }
                this._stop = stop
            }, rand(80, 180))
        }
        _open() {
            this.readyState = 1
            this._emit("open")
        }
        _emit(type, data) {
            if (this.readyState === 2 && type !== "error") return
            const e = data === undefined ? new Event(type) : new MessageEvent(type, { data, origin: location.origin })
            this.dispatchEvent(e)
            const h = this[`on${type}`]
            if (typeof h === "function") h.call(this, e)
        }
        close() {
            this.readyState = 2
            this._stop?.()
            this._stop = null
        }
    }
    window.EventSource = DemoEventSource

    function stream(path, es) {
        if (path === "/api/events") {
            es._open()
            const send = (data) => es._emit("apps", data)
            send(JSON.stringify(appsJSON()))
            listeners.add(send)
            return () => listeners.delete(send)
        }
        const m = path.match(/^\/api\/apps\/([^/]+)\/logs$/)
        if (m) {
            const a = apps.get(decodeURIComponent(m[1]))
            if (!a) return null
            es._open()
            // As últimas linhas primeiro (como `dokku logs --tail`), depois ao vivo.
            const t0 = now()
            const backlog = Array.from({ length: 140 }, (_, i) => logLine(a, t0 - (140 - i) * rand(1200, 3800)))
            backlog.sort((x, y) => x.t - y.t)
            for (const l of backlog) es._emit("message", JSON.stringify(l.text))
            let timer = 0
            const next = () => {
                const burst = Math.random() < 0.2 ? Math.ceil(rand(2, 5)) : 1
                for (let i = 0; i < burst; i++) es._emit("message", JSON.stringify(logLine(a, now()).text))
                timer = setTimeout(next, a.status === "suspended" ? rand(6000, 9000) : rand(350, 1700))
            }
            timer = setTimeout(next, rand(400, 900))
            return () => clearTimeout(timer)
        }
        return null
    }

    // ---------- logs de mentira, com cara de produção ----------

    const IPS = ["10.0.3.12", "10.0.3.17", "172.17.0.1", "10.0.3.4"]
    const ROUTES_API = [
        ["GET", "/v1/products?page=2", 200], ["GET", "/v1/products/8812", 200], ["POST", "/v1/cart/items", 201], ["GET", "/v1/cart", 200],
        ["POST", "/v1/checkout", 200], ["GET", "/v1/orders/41077", 200], ["GET", "/healthz", 200], ["GET", "/v1/search?q=lamp", 200],
        ["PATCH", "/v1/cart/items/3", 200], ["GET", "/v1/products/9999", 404], ["POST", "/v1/auth/refresh", 200], ["GET", "/v1/categories", 304],
    ]
    const ROUTES_WEB = ["/", "/products/desk-lamp", "/cart", "/checkout", "/_next/static/chunks/main-4f1e.js", "/products?category=lighting", "/account/orders", "/favicon.ico", "/api/revalidate"]
    const JOBS = ["SendOrderEmail", "SyncInventory", "GenerateInvoicePDF", "RefreshSearchIndex", "ChargePayment", "ExpireCarts", "ResizeProductImage"]

    function logLine(a, t) {
        const procs = Object.keys(a.procs)
        const p = pick(procs)
        const n = Math.ceil(rand(0, a.procs[p]?.total || 1)) || 1
        const proc = `${p}.${n}`
        const ms = (lo, hi) => `${Math.round(rand(lo, hi))}ms`
        const r = Math.random()
        let msg
        switch (a.kind) {
            case "api": {
                if (r < 0.04) msg = `level=warn msg="slow query" table=orders duration=${ms(600, 1400)}`
                else if (r < 0.06) msg = `level=error msg="payment provider timeout" order=${Math.floor(rand(41000, 42000))} retry=1`
                else if (r < 0.1) msg = `level=info msg="cache miss" key=product:${Math.floor(rand(8000, 9999))}`
                else {
                    const [verb, url, code] = pick(ROUTES_API)
                    msg = `${pick(IPS)} - - "${verb} ${url} HTTP/1.1" ${code} ${Math.floor(rand(120, 4800))} ${(rand(2, 90) / 1000).toFixed(3)}`
                }
                break
            }
            case "web": {
                if (r < 0.05) msg = `warn  - Fast Refresh disabled in production; ignoring HMR ping`
                else msg = `${pick(["GET", "GET", "GET", "HEAD"])} ${pick(ROUTES_WEB)} ${r < 0.08 ? 404 : 200} in ${ms(3, 180)}`
                break
            }
            case "worker": {
                const job = pick(JOBS)
                if (r < 0.05) msg = `level=warn msg="job retry" job=${job} attempt=2 error="connection reset by peer"`
                else if (r < 0.15) msg = `level=info msg="queue stats" pending=${Math.floor(rand(0, 40))} running=${Math.floor(rand(1, 8))}`
                else msg = `level=info msg="job done" job=${job} id=${Math.floor(rand(100000, 999999))} took=${ms(30, 900)}`
                break
            }
            case "static": {
                msg = `${pick(IPS)} - - "GET ${pick(["/", "/getting-started", "/guides/deploy", "/api/reference", "/assets/app.css", "/search.json"])} HTTP/1.1" ${r < 0.04 ? 404 : 200} ${Math.floor(rand(400, 22000))} "-" "Mozilla/5.0"`
                break
            }
            case "status": {
                const site = pick(["store.example.com", "api.example.com", "docs.example.com", "grafana.example.com"])
                msg = r < 0.06 ? `[MONITOR] WARN: Monitor #${Math.floor(rand(1, 9))} '${site}': Pending: timeout of 48000ms exceeded | Interval: 60 seconds` : `[MONITOR] INFO: Monitor #${Math.floor(rand(1, 9))} '${site}': Successful Response: ${ms(18, 240)} | Interval: 60 seconds | Type: http`
                break
            }
            default: {
                msg = r < 0.05
                    ? `logger=context userId=1 orgId=1 uname=ana t=${iso(t)} level=warn msg="Request Completed" method=GET path=/api/live/ws status=-1 duration=${ms(1, 9)}`
                    : `logger=context userId=1 orgId=1 uname=ana t=${iso(t)} level=info msg="Request Completed" method=GET path=/api/ds/query status=200 duration=${ms(4, 300)}`
            }
        }
        return { t, text: `${iso(t)} ${a.name}[${proc}]: ${msg}` }
    }

    // ---------- links externos e aba inicial ----------

    // Os domínios das apps são de exemplo: clicar neles não sai da demo.
    document.addEventListener("click", (e) => {
        const link = e.target.closest?.("a[href]")
        if (link instanceof HTMLAnchorElement && new URL(link.href).origin !== location.origin) e.preventDefault()
    }, true)

    const waitFor = async (fn, ms = 15000) => {
        const end = now() + ms
        while (now() < end) {
            const v = fn()
            if (v) return v
            await sleep(80)
        }
        return null
    }
    const visible = (el) => !!el && el.getClientRects().length > 0 && !el.closest("[hidden]")

    if (VIEW === "app" && START_TAB) {
        const order = ["history", "versions", "logs", "vars", "settings"]
        const idx = order.indexOf(START_TAB)
        if (idx > 0) waitFor(() => document.querySelectorAll(".page-app .tabs .tab")[idx]).then((tab) => tab?.click())
    }

    // ---------- autoplay do onboarding ----------

    if (!AUTOPLAY) return

    let stopped = false
    // Qualquer interação de verdade devolve o controle a quem está vendo.
    for (const type of ["pointerdown", "keydown", "wheel", "touchstart"]) {
        window.addEventListener(type, (e) => {
            if (!e.isTrusted || stopped) return
            stopped = true
            cursor?.remove()
        }, { capture: true, passive: true })
    }

    let cursor = null
    const makeCursor = () => {
        const el = document.createElement("div")
        el.setAttribute("aria-hidden", "true")
        el.style.cssText = "position:fixed;left:0;top:0;width:22px;height:22px;z-index:2147483647;pointer-events:none;transform:translate(-40px,-40px);transition:transform .7s cubic-bezier(.3,.7,.2,1),opacity .3s;filter:drop-shadow(0 2px 4px rgb(0 0 0 / .45))"
        el.innerHTML = '<svg viewBox="0 0 24 24" width="22" height="22"><path d="M4 2.5v17.2l4.6-4.3 2.9 6.6 3.1-1.4-2.9-6.5H18Z" fill="#fff" stroke="#111" stroke-width="1.4" stroke-linejoin="round"/></svg>'
        document.body.append(el)
        return el
    }
    let cx = -40, cy = -40
    const moveTo = async (x, y) => {
        cursor ??= makeCursor()
        const d = Math.hypot(x - cx, y - cy)
        const ms = clamp(d * 1.1, 280, 850)
        cursor.style.transitionDuration = `${ms}ms`
        cursor.style.transform = `translate(${x - 4}px, ${y - 3}px)`
        cx = x
        cy = y
        await sleep(ms + 60)
    }
    const ripple = (x, y) => {
        const r = document.createElement("div")
        r.style.cssText = `position:fixed;left:${x - 14}px;top:${y - 14}px;width:28px;height:28px;border-radius:50%;background:rgb(107 155 255 / .45);pointer-events:none;z-index:2147483646`
        document.body.append(r)
        r.animate([{ transform: "scale(.3)", opacity: 1 }, { transform: "scale(1.6)", opacity: 0 }], { duration: 500, easing: "ease-out" }).finished.then(() => r.remove(), () => r.remove())
    }
    const reveal = async (el) => {
        const r = el.getBoundingClientRect()
        if (r.top < 70 || r.bottom > innerHeight - 20) {
            el.scrollIntoView({ block: "center", behavior: "smooth" })
            await sleep(650)
        }
    }
    const point = async (el) => {
        await reveal(el)
        const r = el.getBoundingClientRect()
        await moveTo(r.left + Math.min(r.width / 2, 60), r.top + r.height / 2)
    }
    const click = async (el) => {
        if (stopped || !el) return
        await point(el)
        if (stopped) return
        ripple(cx, cy)
        await sleep(120)
        el.focus?.({ preventScroll: true })
        el.click()
    }
    const type = async (el, text) => {
        for (const ch of text) {
            if (stopped) return
            el.value += ch
            el.dispatchEvent(new InputEvent("input", { bubbles: true, data: ch, inputType: "insertText" }))
            await sleep(rand(45, 120) + (/[/:.]/.test(ch) ? 120 : 0))
        }
    }
    const clear = async (el) => {
        el.select?.()
        await sleep(350)
        el.value = ""
        el.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "deleteContentBackward" }))
        await sleep(250)
    }
    const card = () => document.querySelector(".page-onboarding .wizard-body .wizard-card")
    const primary = () => [...(card()?.querySelectorAll(".wizard-actions .btn-primary") || [])].find(visible)

    const IMAGE_DEMO = "ghcr.io/acme/shop:2.0.0"
    const NAME_DEMO = "shop"

    async function tour() {
        // Passo Método: "Imagem Docker" (a única disponível) e Continuar.
        const method = await waitFor(() => document.querySelector(".page-onboarding .method-card"))
        if (!method) return
        await sleep(1600)
        await click(method)
        await sleep(700)
        await click(primary())

        // Passo Imagem: digita a imagem; o ghcr.io já está conectado.
        const image = await waitFor(() => card()?.querySelector("input.input.mono"))
        if (!image) return
        await sleep(700)
        await click(image)
        await type(image, IMAGE_DEMO)
        await waitFor(() => visible(card()?.querySelector(".registry-connected")), 3000)
        await sleep(1100)
        await click(primary())

        // Passo App: troca o nome sugerido e dispara o deploy.
        const name = await waitFor(() => card()?.querySelector('input.input.mono[maxlength="63"]'))
        if (!name) return
        await sleep(800)
        await click(name)
        await clear(name)
        await type(name, NAME_DEMO)
        await sleep(1200)
        await click(primary())

        // Deploy: só acompanha (fases + log) até ficar no ar.
        await sleep(1500)
        const log = await waitFor(() => { const el = document.querySelector(".deploy-log"); return visible(el) && el }, 8000)
        if (log) await point(log)
        await waitFor(() => document.querySelector(".page-onboarding .success-card"), 45000)
        await sleep(900)
        const open = document.querySelector(".success-card .btn-primary")
        if (open) await point(open)
        await sleep(4200)
    }

    async function loop() {
        await waitFor(() => document.querySelector(".page-onboarding"), 20000)
        while (!stopped) {
            await tour()
            if (stopped) return
            // Recomeça do zero: a app criada some e o assistente reabre.
            apps.delete(NAME_DEMO)
            actions.delete(NAME_DEMO)
            changed()
            await moveTo(innerWidth - 60, innerHeight - 40)
            window.navigation?.navigate("/onboarding", { history: "replace" })
            window.scrollTo({ top: 0 })
            await sleep(600)
        }
    }
    addEventListener("DOMContentLoaded", loop)
})()
