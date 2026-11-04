# Container base image

The root `Dockerfile` builds a versioned base image for running one soho job per container. It holds the `soho` CLI and skill resources, the `claude`, `codex`, `grok` and `pi` agent runner CLIs, and the toolchain a job needs to build and test this repository: Go, a C compiler for the race detector, Git and Node.js. Downstream images extend it with `FROM`; the base itself contains no credentials, no runner login and no project checkout.

Herdr stays on the host. A host-side Herdr pane runs `docker exec -it` into the job container, so a person can watch and type into the in-container runner like any other pane. The image does not ship Herdr, and in-container `soho` cannot control host panes (see [Herdr and in-container soho](#herdr-and-in-container-soho)).

## Contents

| Component | Version input | Source and verification |
|---|---|---|
| Base OS | `DEBIAN_IMAGE` (`debian:trixie-slim` by digest) | Docker Official Image, pinned by index digest |
| Debian packages | `DEBIAN_SNAPSHOT` | `snapshot.debian.org` archive at a fixed timestamp; apt verifies the archive signatures with the base image's keyring |
| Go | `GO_VERSION`, `GO_SHA256_*` | `go.dev/dl` archive, SHA-256 from the official download index |
| Node.js | `NODE_VERSION`, `NODE_SHA256_*` | `nodejs.org/dist` archive, SHA-256 from the release's `SHASUMS256.txt` |
| Claude Code | `CLAUDE_CODE_VERSION`, `CLAUDE_CODE_SHA256_*` | Native build from `downloads.claude.ai/claude-code-releases/<version>/linux-<arch>/claude`, SHA-256 from that release's `manifest.json` (the source the official installer uses) |
| Grok CLI | `GROK_VERSION`, `GROK_SHA256_*` | Release artifact `x.ai/cli/grok-<version>-linux-<arch>` (the official installer's source), or the same file from the installer's fallback host `storage.googleapis.com/grok-build-public-artifacts/cli/` when that download fails; upstream publishes no checksum, so the digest is recorded when the version is pinned |
| Codex CLI | `CODEX_VERSION`, `container/runners/package-lock.json` | npm package `@openai/codex` and its platform package, installed with `npm ci` from the lockfile |
| Pi | `PI_VERSION`, `container/runners/package-lock.json` | npm package `@earendil-works/pi-coding-agent`, installed with `npm ci --ignore-scripts` as its documentation recommends; the `protobufjs` override is the one in the official Pi installer's `package.json` for that version; every other transitive version comes from this repository's own lockfile |
| soho | `SOHO_VERSION` | Built from the curated source in the build context with the release flags (`-trimpath -buildvcs=false -ldflags "-s -w -X …version=…"`) |
| Dockerfile frontend | `# syntax=` line | `docker/dockerfile` pinned by digest |

Debian packages installed explicitly: `ca-certificates`, `curl`, `fd-find` (also linked as `fd`), `gcc`, `git`, `less`, `libc6-dev`, `openssh-client`, `patch`, `procps`, `ripgrep`, `tini` and `xz-utils`. Their dependencies come from the same snapshot.

The npm lockfile records the exact version, registry URL and `sha512` integrity of every transitive package; `npm ci` refuses a tree that does not match it. Lifecycle scripts are disabled, so no package runs code at install time.

Runtime layout:

| Path | Content |
|---|---|
| `/usr/local/bin/soho` | soho CLI |
| `/usr/local/share/soho/skills/soho` | Skill resources (`SKILL.md`, roles, references, templates); `SOHO_SKILL_DIR` points here |
| `/usr/local/bin/claude`, `/usr/local/bin/grok` | Native runner binaries |
| `/opt/soho/runners` | npm tree for `codex` and `pi` (linked from `/usr/local/bin`) |
| `/usr/local/go`, `/opt/node` | Go and Node.js toolchains |
| `/workspace` | Working directory for the job checkout (owned by `agent`) |
| `/home/agent` | Home of the non-root `agent` user (UID and GID 1000) |

The image runs as `agent`, with `tini` as the entrypoint and `sleep infinity` as the default command: the container idles, and runner sessions start with `docker exec`. The environment sets `GOTOOLCHAIN=local` (the pinned Go never downloads another toolchain), `DISABLE_AUTOUPDATER=1`, `GROK_DISABLE_AUTOUPDATER=1` and `PI_SKIP_VERSION_CHECK=1`, so runners do not replace the pinned binaries at run time. To use the skill with a runner's skill loader, link the resource directory into that runner's skill directory inside the container, for example `ln -s "$SOHO_SKILL_DIR" ~/.claude/skills/soho`.

## Build

The build reads only an allowlisted context: `Dockerfile.dockerignore` denies everything and allows `go.mod`, the `soho` command, `internal/`, the skill resources and the runner lockfile, then excludes tests, test data and dot files. Version control data, local state, agent configuration, documentation and anything else in the checkout never reach the builder.

Build with a dedicated BuildKit builder pinned by digest. The builder version is part of the reproducibility boundary, and the exporter of Docker's default `docker` driver on the classic image store does not apply `rewrite-timestamp`, which leaves build-time file timestamps in the layers.

```sh
docker buildx create --name soho-base --driver docker-container \
  --driver-opt image=moby/buildkit:v0.33.1@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea
docker buildx build --builder soho-base \
  --platform linux/arm64 \
  --build-arg SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)" \
  --build-arg SOHO_VERSION="$(git describe --tags --always)" \
  --build-arg VCS_REF="$(git rev-parse HEAD)" \
  --provenance=false --sbom=false \
  --output type=docker,name=soho-base:0.1.0,dest=soho-base-0.1.0.tar,rewrite-timestamp=true \
  .
docker load -i soho-base-0.1.0.tar
```

Use `--platform linux/amd64` on an x86-64 host. Run each platform's image on a host of that architecture: under QEMU emulation of `linux/amd64` on an ARM host, the native Claude Code binary aborts at start (its Bun runtime crashes), while the other tools run. The tag carries `BASE_IMAGE_VERSION` (also the `org.opencontainers.image.version` label); bump both together when an input changes. `VCS_REF` records the full source commit in the `org.opencontainers.image.revision` label, `SOHO_VERSION` the `git describe` name in `io.github.soho.cli.version`, and `SOURCE_DATE_EPOCH` the image `created` time; the label is empty when `VCS_REF` is not passed. Provenance and SBOM attestations are disabled because they record builder and context details; the inputs are documented here instead. `docker buildx rm soho-base` removes the builder when it is no longer needed.

To inspect the exact context the build receives, export the `build-context` stage:

```sh
docker buildx build --target build-context --output type=local,dest=/tmp/soho-context .
```

## Using the base downstream

`container/examples/downstream/Dockerfile` is a generic downstream image. Two references to the base exist, and they are different digests:

- **Local tag.** After `docker load`, the base exists only on this Docker engine as `soho-base:0.1.0`. `docker build` resolves local images, so `FROM soho-base:0.1.0` works: `docker build -t my-job-image:dev container/examples/downstream`. Check that the tag still points at the expected build with `docker image inspect --format '{{.Id}}' soho-base:0.1.0`; the image ID is the digest of the image config. A `docker-container` builder (such as the pinned builder above) does not see the engine's local images, so build downstream images with the engine's own builder.
- **Registry digest.** A digest after `@` in `FROM` is a manifest digest, which only a registry resolves. The local image ID is not one: `FROM soho-base:0.1.0@sha256:<image ID>` fails. Once the base is pushed to a registry you control, pin downstream images to the digest the push reports (`docker image inspect --format '{{json .RepoDigests}}'`): `FROM <registry>/soho-base:0.1.0@sha256:<repo digest>`. The example's `BASE_IMAGE` build argument takes either form. This repository does not publish the image.

Downstream images should keep `USER agent`, the `/workspace` working directory and the entrypoint unless they have a reason to change them, and must keep credentials out of their builds as well.

## Reproducibility

What is pinned: the frontend, the base image and every download by digest; Debian packages by snapshot timestamp; npm packages by lockfile integrity; the soho source by the checked-in tree. `SOURCE_DATE_EPOCH` fixes the image creation time and history timestamps, and `rewrite-timestamp=true` clamps the modification time of every file in the layers to that value.

Verify a rebuild by building twice without the cache, with the same builder, platform, build arguments and image name, and comparing the exported archives:

```sh
# build as above into a.tar, then again with --no-cache into b.tar
sha256sum a.tar b.tar
go run ./container/imagecheck compare a.tar b.tar
```

Identical archive digests mean a bit-for-bit identical image: the same config (image ID), manifest and compressed layers. `compare` also works on two `docker save` archives and on builds exported under different names (the name is recorded in the archive's `index.json` and `manifest.json`, so their archive digests differ). It first verifies both archives (see [Archive integrity](#archive-integrity)): every blob is hashed against its name and every layer's bytes against its recorded diff id, and a mismatch or a missing blob exits 4 before anything is printed. It then prints both image IDs and, when they differ, every differing config field, layer and file attribute (`mode`, `uid`, `mtime`, `content`, …); it exits 0 only when the image IDs are identical, which after that verification means every layer is identical too.

The boundary of the claim:

- The bit-for-bit result is verified for two builds on the same machine with the pinned builder and the same platform. Another BuildKit version, another platform or another exporter may produce different layer bytes; the inputs stay the same, the bytes are not promised. Each platform is its own image: `linux/amd64` and `linux/arm64` builds have different digests.
- The inputs are immutable only while their sources serve them: a removed snapshot, release artifact or npm version stops the build rather than changing it, because every download is checked against its pinned digest. Every download runs under an explicit transfer budget: a stalled transfer (below 1 KiB/s) aborts the attempt after 60 s (`--speed-limit 1024 --speed-time 60`), each attempt is capped at 10 min (`--max-time 600`), and no new attempt starts after 20 min (`--retry 5 --retry-delay 10 --retry-max-time 1200`) although an attempt already running is not cut off; a fully stalled download therefore ends in about 7 min (6 attempts of 60 s plus the 5 waits of 10 s) and the worst case per download is 30 min (20 min before the last attempt starts plus 10 min for that attempt to finish), which the Grok chain doubles because its fallback host carries the same budget. An attempt restarts the file from the beginning, so the largest pinned artifact (the Claude Code binary, about 245 MiB for `linux-x64` at the pinned version) needs a sustained 420 KiB/s or so to finish inside one 10-minute attempt; a slower link fails the build within the budget instead of finishing. The job's `timeout-minutes: 120` remains the outer cap. Retries and the fallback can only change how long the build takes, never the bytes it accepts.
- The Grok digest is a first-use pin: it proves later builds receive the bytes recorded at pin time, not that those bytes are authentic beyond the HTTPS download from the official host.
- apt and npm can write caches, logs and timestamps; the Dockerfile removes the known ones. Any remaining difference is listed by `compare` rather than hidden.

## Inventory and smoke test

`container/smoke.json` lists the commands that prove each tool is present and runs without a login: `--version` and `--help` for every runner, the toolchain versions, the skill resources and the runtime user. `imagecheck smoke` runs each one in a fresh container with `--network none` and checks the reported version against the `ARG` defaults in the `Dockerfile`:

```sh
go run ./container/imagecheck smoke --image soho-base:0.1.0 --spec container/smoke.json \
  --build-arg SOHO_VERSION="$(git describe --tags --always)"
```

## Secret and independence scans

`imagecheck` scans both ends of the build:

- `imagecheck context <dir> --allow <prefix>…` scans an exported build context: every file must sit under an allowed prefix, no path component may be version control, local state, agent configuration or a credential file, and no file may contain a private key or a recognizable access token. A symlink is never followed: its target text gets the token rules and the deny literals, and the path it points at gets the forbidden-name rule.
- `imagecheck image <docker-save.tar>` scans every layer, the image config and the archive's own index files (`manifest.json`, `index.json`, `oci-layout` and the other non-layer files): no credential file in a home directory and no `.git` directory (both compared ASCII case-insensitively, so `.GIT` or `/Root/.AWS/credentials` are caught as an extractor on a case-insensitive file system would write them), no secret-looking environment variable with a value, no host home path in the config or history, and the token patterns in every entry that carries data, whatever its tar type. Member names are normalized the way an extractor writes them (a leading `/`, `./` or `..` cannot move a credential file out of the rule), and the credential and `.git` rules also apply to the path a symlink or hard link points at. A zero-size regular `.wh.` entry is a whiteout marker and records a deletion, so it is not judged as a credential file; any other `.wh.` entry is scanned like every file.

Each match is its own line: `finding <rule> <path> <offset> <length> sha256:<file>` for a match in file content, where the hash is the SHA-256 of the whole file; `finding <rule> <path> -` for a path rule; `finding <rule> <path> @<field>` for a match in tar metadata (`name`, `link`, `uname`, `gname` or `pax`). A finding never prints the bytes matched in file content, a link target or an owner or PAX value, but it does print the path of the entry it belongs to: the relative path in a context, the member name in a layer or the archive file name; a secret-looking environment variable is reported by its name (`config-env-secret:<NAME>`), never its value. A path that holds a deny literal or matches a built-in token rule is instead printed as an ordinal (`entry#<n>`, see [Deny literals](#deny-literals)), with the rule reported at `@name`, so the matched bytes never appear in the displayed path. Every output and error message of every subcommand — with or without a deny file, before and after it is loaded — also passes through the built-in rule mask, which replaces each rule match by `[deny-token:<rule>]`; the mask acts on each whole message, so a multi-line match such as a private-key block held by a name is masked as a unit, and it reaches only what the rules match, so a token-shaped string that matches no rule stays as written. The summary line counts files, bytes, findings, accepted matches and unmatched accept entries; the exit code is 0 only when there is no finding and no unmatched accept entry.

### Deny literals

Both subcommands take `--deny-file <path>`: a private file, kept outside the repository, with one literal per line (a canary, a host or user name, a private path, any name that must never ship). Literals match ASCII case-insensitively in file content and in every name the artifact exports: context directory, file and symlink names and symlink targets; layer member names, link targets, owner user and group names and PAX records (names and link targets both as recorded and as resolved, so `a/./b` or `a//b` cannot split a literal `a/b`); the image config; the archive index files and their names. A hit reports `deny-token:<line>` and where it was found, never the literal. A path that contains a literal is printed as `entry#<n>` (its position in the walk or in the layer), as is a path that matches a built-in token rule, and every output and error message written after the deny file is loaded passes through the deny redaction, which runs last so a deny literal does not survive in a rule marker, and, like every message of every subcommand, through the built-in rule mask as well. Errors raised before that point (an unknown flag, a malformed `--accept` entry, a deny file that cannot be read or holds a token shorter than 4 bytes) name only the argument position, the line number or the error category, never the value or the path, so the literal does not reach the terminal or a log either way; those early diagnostics pass through the built-in rule mask only, which does not know the deny file's literals. Deny hits cannot be accepted.

### Accepting reviewed matches

Third-party binaries and data in the image contain strings that look like secrets without being one: upstream test keys shipped with the Go distribution, self-test vectors in libraries, token prefixes in the string tables of compiled runners, base64 data. `--accept <entry>` and `--accept-file <path>` record such matches after review, one entry per line:

```text
<rule> <offset> <length> sha256:<file sha256> <path>
```

An entry binds to one exact match: the rule, the path inside the layer or context (`archive:<name>` for an archive index file), the byte offset, the match length and the SHA-256 of the whole file, all as printed by the scan. When byte-identical copies of a file sit at the same path in several layers, that match is printed once per layer and one entry accepts each of those occurrences, because they are the same reviewed bytes. Another match in the same file, a changed byte anywhere in the file, or the same bytes under another path are not covered and remain findings. An entry names no build: a later build whose file has the same bytes at the same path still matches it, while any change to that file's bytes leaves the entry unmatched. An entry that matches nothing is printed as `unmatched-accept` and fails the scan, so a stale list does not linger. Only the token rules (`private-key`, `anthropic-key`, `openai-key`, `xai-key`, `github-token`, `npm-token`, `aws-access-key`, `slack-token`, `google-api-key`) and `host-path` can be accepted; deny literals, credential and `.git` paths, forbidden or non-allowlisted names, secret environment variables and metadata findings cannot. The layer index is not part of an entry, and accepted matches are still printed as `accepted`.

`container/image-scan-accept.linux-arm64.txt` and `container/image-scan-accept.linux-amd64.txt` list the reviewed matches of each platform's image, grouped with the reason and the upstream source of each file. Because entries are bound to file bytes, every pin change that replaces one of those files makes its entries unmatched: scan without the list, review each new match without printing it (rule, length, surrounding structure, the file's upstream origin), and regenerate the platform's list from the scan output. Each list belongs to its own platform's image: scanning one platform's image with the other platform's list fails the scan, because the entries for files that differ between the platforms stay unmatched (an entry for a file that is byte-identical on both platforms matches in both), so both lists are reviewed and regenerated together when a shared pin changes.

```sh
go run ./container/imagecheck image soho-base-0.1.0.tar \
  --accept-file container/image-scan-accept.linux-arm64.txt --deny-file /path/outside/repo/deny.txt
```

### Archive integrity

Before it scans or compares, `imagecheck` checks that a saved archive is internally consistent and fails with exit 4 otherwise: `manifest.json` names exactly one image; `index.json`, `oci-layout` and `repositories`, when present, parse; the config is a JSON object whose `rootfs.diff_ids` has one valid digest per manifest layer; no outer entry name repeats, the config and layers are regular entries, and no non-regular outer entry carries data; every `blobs/sha256/<hex>` blob hashes to its name, including blobs `manifest.json` does not name; `index.json`, when present, lists at least one image manifest, and each one is a blob in the archive that names the same config and the same layers, in order, as `manifest.json` (a nested index is refused), so an OCI reader and a `docker save` reader load the same image; and every layer's uncompressed bytes, to the end of the stream, hash to its recorded diff id. A missing blob is an error, not a skipped layer. The config is capped at 16 MiB; every other outer file it keeps in memory is capped at 16 MiB, and those other files together at 64 MiB; layers are streamed.

### What the scans do not prove

The token rules recognize known formats and the deny file lists known literals: a credential in another format, or one hidden inside compressed data within a file (a nested gzip or zip), is not detected; a nested uncompressed tar is scanned as raw bytes. Metadata findings cover what the archive records; a build tool that writes no owner names leaves nothing to check there.

A useful negative test is a canary: put a file holding a unique literal in the checkout outside the allowlist, export the context, and confirm the scan with that literal in the deny file finds nothing; then add the literal to an allowed file, and to a file or directory name, in a copy of the exported context and confirm the scan reports each one.

## Running a job

One container per job; one Compose project per job keeps containers, networks and volumes apart. `container/examples/compose.yaml` is the reference, with `macos.env.example` and `windows.env.example`:

```sh
docker compose -p job-42 --env-file /path/to/job-42.env -f container/examples/compose.yaml up -d
docker compose -p job-42 -f container/examples/compose.yaml exec agent claude
docker compose -p job-42 -f container/examples/compose.yaml down       # keep runner state
docker compose -p job-42 -f container/examples/compose.yaml down -v    # remove runner state
```

Without Compose:

```sh
docker run -d --name job-42 \
  --mount type=bind,source=/path/to/jobs/42/repo,target=/workspace \
  --mount type=volume,source=job-42-claude,target=/home/agent/.claude \
  soho-base:0.1.0
docker exec -it job-42 claude
docker rm -f job-42          # the job-42-* volumes stay until docker volume rm
```

- **Workspace.** The job checkout is bind-mounted read-write at `/workspace`. On Windows hosts with Docker Desktop (WSL 2 backend), use forward-slash paths in the env file (`C:/jobs/42/repo`); keeping the checkout inside the WSL file system is faster than a Windows drive. Clone with `core.autocrlf=false` so files keep LF line endings.
- **Runner state.** Each runner keeps logins, sessions and settings under its home directory (`~/.claude`, `~/.codex`, `~/.grok`, `~/.pi`). The example gives each job its own named volumes: they survive `down` and container restarts and are deleted with `down -v` or `docker volume rm`. Retain them while a job may resume; delete them when the job closes.
- **Lifecycle.** `up -d` starts the idle container; each `exec` starts one runner session; stopping the container ends every session; `down` removes the container and network.
- **Resources.** The examples set no CPU or memory limits.

### Credentials

The image holds no credential, and nothing in the build accepts one: no build argument or `ENV` carries a key, and runner login files exist only in runtime volumes. Choose per runner:

- **Interactive login.** Run the runner in the container (`docker exec -it job-42 claude`) and log in once; the login lands in that job's state volume, never in an image layer.
- **Key files.** Mount each key read-only as a file. The Compose example declares `secrets:` entries, mounted at `/run/secrets/<name>`, with placeholder sources; point `ANTHROPIC_API_KEY_FILE`, `OPENAI_API_KEY_FILE` and `XAI_API_KEY_FILE` at files outside any checkout. Export a key only into the runner process: `docker exec -it job-42 sh -c 'ANTHROPIC_API_KEY="$(cat /run/secrets/anthropic_api_key)" exec claude'`. File-based Compose secrets are bind mounts of the host file and keep its owner and mode: on a native Linux engine a `0600` key file owned by another UID is unreadable by `agent` (UID 1000), so give the file to UID 1000 or make it readable inside a directory only you can open. Docker Desktop for macOS presented such files as readable by `agent` in testing; check your own engine with `docker compose exec agent cat /run/secrets/<name> >/dev/null`.
- **Other services.** The image does not include clients or configuration for additional services, such as a Hermes or Cinzel endpoint or an ai-memory server. A downstream image or the job's runtime adds the client; its endpoint, tokens and any runner MCP entry that references them are injected at run time only, the same way: a read-only secret file (the Compose example declares the placeholders `hermes_credentials`, `cinzel_credentials` and `ai_memory_credentials`, from `HERMES_CREDENTIALS_FILE`, `CINZEL_CREDENTIALS_FILE` and `AI_MEMORY_CREDENTIALS_FILE`) and configuration kept in the job's runner state volume. What each client expects in that file is defined by the client, not by this image.

Avoid `environment:`, `docker run -e` and `--env-file` for keys: those values are stored in the container configuration and anyone who can run `docker inspect` (or `kubectl get pod -o yaml`) reads them. A key exported inside one `exec` is not stored in the container configuration, but any process of the same user in that container, including another `docker exec` session, can read it from `/proc/<pid>/environ`; treat everyone who can `docker exec` into the job as able to read its keys. Never bind-mount a host runner configuration directory read-write into a job: it would share the host's sessions and let the job change them. Do not pass keys as build arguments; they would be recorded in the image history.

### Herdr panes

Herdr runs on the host. A host pane attaches to the container with an interactive TTY:

```sh
docker exec -it job-42 claude
```

`-i` keeps stdin open and `-t` allocates a terminal, so keys typed into the pane (or sent with `herdr pane send-keys`) reach the runner, and resizing the pane resizes the runner's terminal. When the session ends, the pane returns to the host shell; the container keeps running.

## Herdr and in-container soho

The `soho` binary in the image runs the commands that need no Herdr server, such as `--version`, `--help`, `env` (which reports the skill directory and the installed runner versions) and `lint` for briefs, and it serves the skill resources. Commands that drive Herdr (`init`, `spawn`, `dispatch`, `wait`, `roster`, …) need the Herdr server's control socket and `HERDR_ENV`. Herdr does not run in the container, and no bridge forwards its socket into one, so those commands are not available inside the container.

Orchestration therefore stays on the host: the orchestrating agent and `soho` run in Herdr on the host, and a pane runs a containerized runner with `docker exec -it`. `soho spawn` starts runner CLIs directly on the host; it has no option to start them inside a container, so a containerized worker pane is opened and driven by hand (or by a host tool) rather than by `spawn`. Report files a containerized runner writes under `/workspace` appear in the host checkout through the bind mount.

## Continuous integration

`.github/workflows/container-image.yml` builds and checks the image on pull requests and on pushes to `main` that change a build input: `Dockerfile`, `Dockerfile.dockerignore`, `go.mod`, `cmd/soho/**`, `internal/**`, `skills/soho/**`, `container/**` or the workflow itself.

Each platform runs on its own native GitHub-hosted runner, without emulation: `ubuntu-24.04` (x86-64) for `linux/amd64` and `ubuntu-24.04-arm` (aarch64) for `linux/arm64`. The amd64 image needs a native x86-64 host because the native Claude Code binary aborts under QEMU emulation (see [Build](#build)).

The job checks out the exact commit under test: the pull request's head commit, not the synthetic merge commit, or the pushed commit on `main`. It validates that the value is a full 40-character SHA, fetches it anonymously with its full history and every tag, and fails unless `HEAD` is that commit. `git describe --tags --always` is resolved once from that checkout, and the same `SOHO_VERSION` is passed to the build and to the smoke check. The build uses the [Build](#build) command: the pinned BuildKit builder, `--no-cache`, one platform, the Dockerfile defaults, `SOURCE_DATE_EPOCH` from the commit, `VCS_REF` set to the full commit SHA, no attestations and the timestamp-rewriting Docker archive output. The archive is hashed and loaded into the runner's engine.

The job fails when any of these does not hold:

- the runner's `uname -m` and the Docker server's architecture match the platform;
- the loaded image is `linux` with the platform's architecture, its `org.opencontainers.image.revision` label is the commit SHA and its `io.github.soho.cli.version` label is the `SOHO_VERSION` passed to the build;
- `imagecheck smoke` exits 0 and reports every check of `container/smoke.json` as ok, run offline as described in [Inventory and smoke test](#inventory-and-smoke-test);
- `imagecheck image` finds nothing in the archive with that platform's `container/image-scan-accept.linux-<arch>.txt`;
- `imagecheck context` finds nothing in the exported `build-context` stage, with the allowed prefixes of `Dockerfile.dockerignore`.

The workflow has read-only `contents` permission, uses no third-party actions, reads no secrets and pushes nothing. It publishes its evidence in the job log and in the run's step summary: run URL, source SHA and tree, `SOHO_VERSION`, runner and image architecture, image ID, archive SHA-256 and the smoke and scan summaries. The private deny-literal scan and the two-build `compare` remain local steps. Because the scans run there without a deny file, the built-in rule mask is what keeps a matched token out of the public job log: a finding that names an entry whose path holds a rule match prints its ordinal instead of the path, and every output message passes through the mask, which acts on each whole message and reaches only what the rules match, so a token-shaped string that matches no rule is not masked (see [Secret and independence scans](#secret-and-independence-scans)).

## Native-only checks

The container covers Linux builds and tests of the Go code, the runners' CLI surface and the job workflow inside one container. It does not replace native testing on macOS, Linux and Windows. These stay on native hosts:

- Herdr itself, its panes, tray and window integration, and the optional plugin's terminal interface.
- Windows behavior: Credential Manager, Windows paths and process handling, console and terminal semantics.
- macOS behavior: process-tree inspection and ownership checks, and any macOS-specific path or permission handling.
- Every OS-native test of process, locking, installation and terminal behavior that the project's native CI matrix runs.

## Updating pins

Change one input at a time, rebuild, rerun `smoke`, the scans and `compare`, then bump `BASE_IMAGE_VERSION`.

- **Base image and frontend:** `docker buildx imagetools inspect debian:trixie-slim` (or `docker/dockerfile:<version>`) and copy the index digest.
- **Debian packages:** set `DEBIAN_SNAPSHOT` to a `YYYYMMDDTHHMMSSZ` timestamp listed on `snapshot.debian.org`, usually the one in the new base image's `/etc/apt/sources.list.d/debian.sources` comment.
- **Go:** take the `sha256` of the two `linux-amd64` and `linux-arm64` archives from `https://go.dev/dl/?mode=json&include=all`.
- **Node.js:** take the two `linux-x64` and `linux-arm64` `.tar.xz` lines from `https://nodejs.org/dist/v<version>/SHASUMS256.txt`.
- **Claude Code:** take `platforms["linux-x64"].checksum` and `platforms["linux-arm64"].checksum` from `https://downloads.claude.ai/claude-code-releases/<version>/manifest.json`.
- **Grok CLI:** download `https://x.ai/cli/grok-<version>-linux-x86_64` and `-linux-aarch64`, compare each against the same file from the installer's fallback host (`storage.googleapis.com/grok-build-public-artifacts/cli/`), and record the SHA-256.
- **Codex and Pi:** edit the versions in `container/runners/package.json` (and `CODEX_VERSION`/`PI_VERSION` in the `Dockerfile`), then regenerate the lockfile with the pinned Node.js release, for example `docker run --rm -v "$PWD/container/runners:/w" -w /w node:<NODE_VERSION>-trixie-slim npm install --package-lock-only --ignore-scripts`. Review the lockfile diff: every `resolved` URL must be on `registry.npmjs.org` and every entry must keep an `integrity` value.
