<p align="center">
  <img src="ui/assets/icon-512.png" alt="dokk" width="96" height="96">
</p>

<h1 align="center">dokk</h1>

<p align="center">
  A web panel for <a href="https://dokku.com">Dokku</a>, shipped as a single binary.<br>
  From server to a Dokku PaaS, with one binary. Manage it from the browser.
</p>

<p align="center">
  <a href="https://github.com/eduardotorresdev/dokk/actions/workflows/ci.yml"><img src="https://github.com/eduardotorresdev/dokk/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/eduardotorresdev/dokk/releases/latest"><img src="https://img.shields.io/github/v/release/eduardotorresdev/dokk?display_name=tag" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License"></a>
</p>

<p align="center">
  <a href="https://eduardotorresdev.github.io/dokk/">Website</a> -
  <a href="https://github.com/eduardotorresdev/dokk/releases">Releases</a> -
  <a href="https://dokku.com/docs/">Dokku docs</a>
</p>

---

## What is dokk?

dokk is one Go binary with the UI embedded. Run it on a server and you get:

- **Guided onboarding.** Detects an existing Dokku install, or **installs Dokku for you**: on Ubuntu and Debian with the official `bootstrap.sh` (Docker, nginx, Dokku), on any other distro with Docker and the official Dokku container. Then sets the global domain, Let's Encrypt, and walks you to your first deploy.
- **Live dashboard.** All apps with status, processes and resource share, streamed over SSE. Host CPU, memory and disk.
- **App detail.** Streaming logs, build logs, deploy history, published tags, env vars, ports, linked services. Start, stop, restart, rename, remove with progress.
- **Deploy from Docker images.** Public images directly, private ones after a registry login (Docker Hub, GHCR, GitLab, self-hosted). Install any published tag from the Versions tab.
- **Domains and HTTPS.** Per-app domain and Let's Encrypt certificate from the wizard; renewal and auto-renew from the panel.
- **Login.** A superuser created on first run (or via `DOKK_USER` / `DOKK_PASSWORD`).
- **Three languages.** English, Español, Português (BR).

dokk is **not** a replacement for Dokku. Every action in the UI is a real Dokku command on the server; the `dokku` CLI, plugins and `git push` keep working alongside it. Remove dokk and your apps keep running.

Deploy methods other than Docker image (git push, Dockerfile, buildpacks) are coming soon.

## Quick install

On a fresh Linux server, as root:

```sh
curl -fsSL https://eduardotorresdev.github.io/dokk/install.sh | sudo sh
```

The installer:

1. detects the architecture (amd64 / arm64);
2. downloads the latest release from [GitHub Releases](https://github.com/eduardotorresdev/dokk/releases);
3. verifies the archive against `checksums.txt` (sha256);
4. installs the binary to `/usr/local/bin/dokk`;
5. creates and starts a `dokk` systemd service.

Environment options for the installer:

| Variable | Effect |
| --- | --- |
| `DOKK_VERSION=v0.1.0` | Pin a specific release instead of latest. |
| `DOKK_ADDR=0.0.0.0:7070` | Listen address written into the service (default `127.0.0.1:7070`). |
| `DOKK_NO_SERVICE=1` | Install the binary only; don't create or start the systemd service. |

Example:

```sh
curl -fsSL https://eduardotorresdev.github.io/dokk/install.sh | sudo DOKK_VERSION=v0.1.0 sh
```

### Manual install

Download `dokk_linux_amd64.tar.gz` or `dokk_linux_arm64.tar.gz` plus `checksums.txt` from the [releases page](https://github.com/eduardotorresdev/dokk/releases), verify, and place the binary in your `PATH`:

```sh
sha256sum -c checksums.txt --ignore-missing
tar -xzf dokk_linux_amd64.tar.gz
sudo install -m 0755 dokk /usr/local/bin/dokk
```

## Requirements

- A Linux server with root (`sudo`). dokk runs as root because it installs Dokku and runs its commands.
- **RAM:** 1 GB minimum (Dokku's recommendation); 2 GB is comfortable with a few apps.
- **Disk:** ~10 GB, more if you keep many images.
- **Ports:** `7070` for the panel (bound to localhost by default), `80`/`443` for apps.

### Supported distributions

dokk is a static binary and runs on **any Linux, amd64 or arm64**. It also installs Dokku on any distro; only the method differs.

| Distribution | Arch | dokk installs Dokku | How | Uses existing Dokku |
| --- | --- | :-: | --- | :-: |
| Ubuntu 22.04 / 24.04 / 26.04 | amd64, arm64 | Yes | Official `bootstrap.sh` (apt, Docker, nginx, Dokku) | Yes |
| Debian 11 / 12 / 13 | amd64, arm64 | Yes | Official `bootstrap.sh` | Yes |
| Fedora, RHEL, Rocky, Alma, Arch, openSUSE, Amazon Linux, others | amd64, arm64 | Yes | Docker (installed if missing) + official Dokku container | Yes |

## What happens after you install

1. **The installer does its job.** Detects your architecture, downloads the latest release, verifies the sha256 against `checksums.txt`, installs `/usr/local/bin/dokk` and starts the `dokk` systemd service.
2. **Open the panel.** It listens on `127.0.0.1:7070` by default: `ssh -L 7070:127.0.0.1:7070 root@SERVER`, then <http://localhost:7070>.
3. **First run: create superuser.** Name, email, password. This account manages the panel and can only be created once (or set `DOKK_USER` / `DOKK_PASSWORD`).
4. **Setup 1/6, Dokku.** The onboarding looks for Dokku on the server. If it's there, it shows the version and reuses it. If not, **dokk installs Dokku for you**: one click runs the official installer (`bootstrap.sh` on Ubuntu/Debian, Docker + official Dokku container elsewhere) and streams the log. Steps shown: *Fetching the latest version*, *Installing Dokku*, *Verifying the installation*.
5. **Setup 2/6, Configuration.** Global domain (apps live at `app.your-domain`; `ip.sslip.io` works with no DNS) and an email to enable Let's Encrypt with auto-renewal.
6. **Setup 3/6, Deploy method.** Docker image today; git push, Dockerfile and buildpacks are coming soon.
7. **Setup 4/6, Docker image.** Image and tag (e.g. `ghcr.io/user/app:1.0.0`). For private images pick the provider (Docker Hub, GHCR, GitLab, other) and sign in; credentials go to `dokku registry:login` and never return to the browser.
8. **Setup 5/6, App.** Name, container port, environment variables (paste a whole `.env` into any field), HTTPS on/off. *Create app and deploy*.
9. **Setup 6/6, Deploy.** Phases *Pulling image*, *Preparing build*, *Starting container*, *Health checks*, *Live*, with the build log. Then *Your app is live* and a button to open it.

## First steps

1. Open the panel. By default it listens only on localhost, so from your machine:

   ```sh
   ssh -L 7070:127.0.0.1:7070 root@SERVER
   ```

   then visit <http://localhost:7070>. If you installed with `DOKK_ADDR=0.0.0.0:7070`, open `http://SERVER:7070` directly (see [Security](#security)).

2. Create the superuser on the first-run page.
3. Follow the onboarding: detect Dokku or let dokk install it, set the global domain (a wildcard DNS record, or `<ip>.sslip.io` with no DNS at all), optionally an email for Let's Encrypt.
4. Create your first app: Docker image, environment variables (paste a whole `.env` into any field), container port, domain and HTTPS. Watch the deploy until it's live.

## Configuration

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-addr` | `127.0.0.1:7070` | HTTP listen address. |
| `-data` | `/var/lib/dokk` | Data directory (superuser, sessions, preferences). |
| `-interval` | `5s` | Interval between status checks. |
| `-dokku-tag` | *(empty = latest)* | Dokku version the onboarding installs. |
| `-dokku-mode` | `auto` | How Dokku runs: `host` (bootstrap.sh), `docker` (official `dokku/dokku` container) or `auto` (bootstrap on supported Ubuntu/Debian, container elsewhere). Also `DOKK_DOKKU_MODE`. |
| `-version` | | Print the version and exit. |

### Environment

| Variable | Description |
| --- | --- |
| `DOKK_USER`, `DOKK_PASSWORD` | Create the superuser from the environment instead of the first-run page. |

### systemd

The installer writes `/etc/systemd/system/dokk.service`. Useful commands:

```sh
systemctl status dokk
journalctl -u dokk -f
systemctl restart dokk
```

To change flags (for example the listen address), edit the `ExecStart` line in the unit, then:

```sh
systemctl daemon-reload && systemctl restart dokk
```

## Upgrade

Re-run the installer. It fetches the latest release, replaces the binary and restarts the service:

```sh
curl -fsSL https://eduardotorresdev.github.io/dokk/install.sh | sudo sh
```

Pin a version with `DOKK_VERSION=vX.Y.Z` if you need to roll back.

## Uninstall

```sh
systemctl disable --now dokk
rm /usr/local/bin/dokk /etc/systemd/system/dokk.service
systemctl daemon-reload
```

dokk's data lives in `/var/lib/dokk`; remove it if you want a clean slate. Dokku and your apps are untouched.

## Security

- dokk runs as root and can do anything Dokku can. Treat access to the panel like SSH access to the server.
- The default bind is `127.0.0.1:7070`. The recommended way in is an SSH tunnel (`ssh -L 7070:127.0.0.1:7070 root@SERVER`).
- If you expose it, put it behind a reverse proxy with TLS (nginx, Caddy, or a Dokku app) and restrict with a firewall. Session cookies are marked `Secure` when the request arrives over HTTPS (directly or via `X-Forwarded-Proto: https`).
- The superuser can only be created once. Choose a strong password; failed logins are limited to 8 attempts per IP every 15 minutes.
- Registry credentials are passed to `dokku registry:login` and never sent back to the browser.

## Building from source

Requires Go 1.26 and Node (for UI checks only; the UI has no build step and is embedded as static files).

```sh
git clone https://github.com/eduardotorresdev/dokk
cd dokk
make build          # go build -o bin/dokk .
sudo ./bin/dokk -addr 127.0.0.1:7070
```

Development:

```sh
make run            # go run . with DOKK_USER=admin DOKK_PASSWORD=admin
make dev            # UI dev server (cd ui && npm run dev)
make check          # go vet, go test, UI syntax + i18n checks
make build-linux    # static linux/amd64 binary in bin/
```

## CLI and MCP

The same `dokk` binary is also a client for a remote panel, in two thin layers over the same code: a CLI for people and CI, and an MCP server for agents. Both do the same things.

```sh
dokk login https://dokk.example.com   # opens the browser (OAuth + PKCE), saves ~/.config/dokk/credentials.json
dokk apps                             # table with status, image and URL
dokk release --app web --bump minor --install
```

In CI, set `DOKK_URL` and `DOKK_TOKEN` (an API token, `dokk_...`) instead of logging in; they take precedence over the saved credential.

| CLI | MCP tool | What it does |
| --- | --- | --- |
| `dokk login [url]` / `logout` / `whoami` | `login {url}` | Browser login and the saved credential |
| `dokk apps` | `list_apps` | Apps with status, image and URL |
| `dokk status <app>` | `app_status` | Image, version, commit, URL, processes, checks |
| `dokk logs <app> [-n N]` | `app_logs {app, lines?}` | Last lines of the app log |
| `dokk start\|stop\|restart <app>` | `start_app` / `stop_app` / `restart_app` | Runs the action and waits for it (start/restart: until healthy) |
| `dokk exec <app> [--run] [--process web] -- <cmd…>` | `exec {app, command, mode?, process?}` | Runs a command **inside the app's container** (never on the host); the CLI exits with the remote exit code |
| `dokk releases <app>` | `list_releases` | GitHub releases, the installed one marked (also when the app runs an image by digest) |
| `dokk install <app> <tag> [--no-hooks]` | `install_release {app, tag, dir?, skip_hooks?}` | Installs a tag of the app's image, with the `dokk.yml` hooks, and waits until healthy |
| `dokk rollback <app> [version] [--no-hooks]` | `rollback_release {app, version?, dir?, skip_hooks?}` | Installs the given version or, without one, the highest stable registry version below the current one |
| `dokk release --app <app> …` | `create_release {app, bump\|version, dir, …}` | Next version → commit pushed? → `docker buildx build --platform linux/amd64 --push` → GitHub release → optional install |
| `dokk build-log <app> <id>` | `build_log` | Log of a build |
| `dokk config get\|set\|unset builder [value]` / `config list` | `config {action, key, value?}` | This machine's config (the builder, see [Builder](#builder)) |
| `dokk help` | `tools/list` | List of commands |

Everything that lists or shows something takes `--json` for scripts. On an interactive terminal the CLI uses colors, spinners and boxes; when piped, in CI (`CI=true`), with `NO_COLOR` or `TERM=dumb` it prints plain text with timestamps. The MCP server never prints colors: tool results are plain text, with ANSI stripped from logs and command output, and errors say what to do next.

`dokk release` flags: `--app`, `--bump patch|minor|major`, `--version`, `--dir`, `--notes`, `--install`, `--prerelease`, `--image` (override the image repository), `--platform`, `--builder`, `--no-hooks`, `--json`. It needs `git` and a builder (docker buildx or podman, see [Builder](#builder)) logged in to the app's registry, an app that deploys from an image and is linked to a GitHub repository. A non-zero exit code means failure.

**Versions.** The next version starts from the highest of the app's git tags (in its tag format, e.g. `back@{version}`) and the semver tags already in the image's registry, so a registry ahead of the GitHub releases is never overwritten. On the remote's default branch the version is a stable bump (`--bump`, default patch). On any other branch it is a **hash version**, `<next patch of the highest stable>-sha.<UTC commit time YYYYMMDDHHMMSS>.g<sha7>`, published as a prerelease; `--bump` is refused there. `--version` always wins.

### `dokk.yml`

The client looks for `dokk.yml` (or `dokk.yaml`, `dokk.json`) from `--dir` (default: the current directory) up to the git root:

```yaml
apps:
  pbs-back:
    build: { context: ., dockerfile: api/Dockerfile }   # default: context = dir, dockerfile = <context>/Dockerfile
    hooks:
      pre-install: ops/dokk/backup-pb.sh                 # relative to the repo root
      post-install: ""                                   # optional
    config: { PBS_BUILD_VERSION: "{version}" }           # set before each install
  pbs-back-qa:
    build: { context: ., dockerfile: api/Dockerfile }
```

The parser covers only this subset of YAML: maps (block and `{ a: b }`), strings (plain, `'single'`, `"double"`) and lists of strings, with `#` comments.

**Hooks run on the client machine** (your laptop, the MCP server's machine or the CI runner), never on the server: `sh -c <hook>` with the repo root as cwd, the output in the progress. They wrap every install: `dokk install`, `dokk release --install`, `dokk rollback` and the MCP tools. A failing `pre-install` aborts before anything touches the app, and the error names the hook; a failing `post-install` marks the result as failed (the install already happened). Besides the process environment, hooks get:

| Variable | Value |
| --- | --- |
| `ACTION` | `install` or `rollback` |
| `APP`, `DOKK_URL` | the app and the panel |
| `FROM_IMAGE`, `TO_IMAGE` | the current and the target image |
| `FROM_VERSION`, `TO_VERSION` | their tags (for an image pinned by digest, the tag the registry resolves for it) |
| `FROM_DIGEST`, `TO_DIGEST` | the digest, when the image is pinned as `repo@sha256:…` |

`--no-hooks` skips them only after typing the app name (or with `--yes` in scripts); the MCP `skip_hooks` parameter is meant to be used only when the user asks.

**`config`** sets app variables before each install, without a restart, so the new image already starts with them. Placeholders: `{version}` (semver, e.g. `2.9.1`), `{tag}` (git tag, e.g. `back@2.9.1`), `{image}` and `{sha}` (the release commit).

### Builder

The image is built **on the client machine**, with the builder that machine has: podman on a Mac, `docker buildx` in CI. The builder is per machine, so it is never in `dokk.yml`.

| Builder | Commands |
| --- | --- |
| `docker` | `docker buildx build --platform … -t <image> [-f …] --push <context>` |
| `podman` | `podman build --platform … -t <image> [-f …] <context>`, then `podman push <image>` |
| `auto` (default) | `docker` if `docker buildx version` works, else `podman` (on the PATH or `/opt/podman/bin/podman`), else an error asking to install one or configure it |

Precedence: `--builder` (`dokk release`, `builder` in `create_release`) > `DOKK_BUILDER` > `~/.config/dokk/config.json` (`{"builder":"podman"}`, 0600, next to `credentials.json`) > `auto`. A binary path also works (`DOKK_BUILDER=/opt/podman/bin/podman`); the kind comes from its name.

```sh
dokk config set builder podman     # or a path; `unset` goes back to auto
dokk config get builder
dokk config list                   # effective value and where it came from (flag/env/config/auto)
```

The MCP `config` tool (`{action: get|set|unset, key: "builder", value?}`) writes the same file. The release step shows the builder used ("Build e push (podman)"). Logging in to the registry is the machine's job (`docker login` / `podman login`); when a push is refused for auth, the error says which command to run, e.g. `podman login ghcr.io`.

GitHub Actions example:

```yaml
name: Release app
on:
  workflow_dispatch:
    inputs:
      bump: { type: choice, options: [patch, minor, major], default: patch }
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Install dokk (client only)
        run: |
          curl -fsSL https://eduardotorresdev.github.io/dokk/install.sh | DOKK_CLIENT_ONLY=1 sh
          echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - name: Release
        env:
          DOKK_URL: https://dokk.example.com
          DOKK_TOKEN: ${{ secrets.DOKK_TOKEN }}
        run: dokk release --app web --bump ${{ inputs.bump }} --install
```

MCP in Claude Code (log in first with `dokk login`, or ask Claude to call the `login` tool):

```sh
claude mcp add dokk -- dokk mcp
```

## Release process

Releases are built by GitHub Actions. Push a tag and the workflow builds `linux/amd64`, `linux/arm64`, `darwin/amd64` and `darwin/arm64` archives, generates `checksums.txt` and publishes a GitHub Release:

```sh
git tag v0.2.0
git push origin v0.2.0
```

The install script always points at the latest release unless `DOKK_VERSION` is set.

## Contributing

Issues and pull requests are welcome. Before opening a PR:

1. run `make check`;
2. keep UI strings in all three locales (`ui/src/locales/{en,es,pt-BR}`); `make check` fails on missing keys;
3. for Dokku interactions, add a test alongside the code in `dokku/`.

If you're unsure whether something fits, open an issue first.

## Acknowledgements

dokk is a thin layer over two projects that deserve the credit:

- **[Dokku](https://dokku.com)** ([dokku/dokku](https://github.com/dokku/dokku), MIT) does the real work: builds, deploys, nginx, certificates, process management. Every action in dokk is a Dokku command. This project exists only because Dokku and its maintainers have kept it excellent for over a decade. Thank you.
- **[shablon](https://github.com/ganigeorgiev/shablon)** by [Gani Georgiev](https://github.com/ganigeorgiev) (MIT) is the tiny no-build reactive UI library dokk's entire frontend is written with. No bundler, no framework runtime, just ES modules served by the Go binary. It made the panel small and fast and was a joy to work with. Thanks, Gani.

## License

[MIT](LICENSE)
