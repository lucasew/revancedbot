# revancedbot Specification

This document constrains the `revancedbot` command: how it turns ReVanced jobs into a simple-binary F-Droid tree on disk.

Status: approved
Genre: cli

The key words MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY in this
document are to be interpreted as described in BCP 14 (RFC 2119,
RFC 8174) when, and only when, they appear in all capitals.

## Intention

Job: Given a positional simple-binary F-Droid root and a signing secret, `revancedbot` writes a complete tree of ReVanced-patched Android apps to disk. Uploading that tree to any host (GitHub Pages, S3, elsewhere) is not this binary’s job.

Non-goals:

1. Deploy, git commit, GitHub Pages, S3, and any other publish of the tree.
2. `fdroid build` and F-Droid buildserver mode.
3. Install of the Android SDK, `apksigner`, and `aapt`.
4. Keystore and secrets stored under Repo.
5. Aurora Store and Google Play download (token dispenser, protobuf).
6. Multi-tenant hosted service.
7. An importable library API. Product types live under `internal/`.
8. Per-app custom patch matrices.
9. Hard integrity proof of a store listing (Play signing certificate as identity).

Inherited C (cite the file):

- Language Go — `go.mod` (version is that file, not this one)
- Module path `github.com/lucasew/revancedbot` — `go.mod`
- Binary name `revancedbot` — `cmd/revancedbot`
- Command-line parse Cobra; configuration Viper — `internal/cli/root.go`, `internal/config/config.go`
- Session, pools, progress TUI — workspaced `pkg/taskgroup` via `internal/cli/root.go`
- Log lines — workspaced `pkg/logging` `NewPlainHandler` via `internal/cli/root.go`
- Progress-aware HTTP and known-URL fetch — workspaced `httpclient` and `fetchurl` via `internal/drivers/prelude.go`, `internal/netx`
- GitHub release metadata — `google/go-github` in `go.mod`
- Dev tool pins — this repository’s `mise.toml`
- Built-in downloader ids `aptoide`, `apkpure`, `apkmirror` — `internal/download`
- Signing blob `v` = 1, JKS — `internal/signing/blob.go`
- Atomic publish — `internal/fdroid/atomic.go`
- Host preflight — `internal/toolscheck`

## Technique

| ID | Input | Rule | Output |
|----|-------|------|--------|
| TEC-01 | Subcommand + Repo path + Cache path | Kitchen-sink `run` plus step commands. Live Repo `{repo,metadata,config.yml}` changes only by atomic publish from Cache stage. `revancedbot.yaml` is never written | The type named in the command table |
| TEC-02 | Command result | Plumbing error → exit 1. Per-package failure is a skip, not a command failure. `run` exits 0 after a successful publish even when every package was skipped. `smoke` exits 1 when zero packages succeed. Machine lines on stdout only for `keys generate`, `list-jobs`, and `download`. Logs on stderr. Session folds logs when the TUI is active | Process status + streams |
| TEC-03 | Stock package id + preferred versions + downloader order + optional CDP URL | Walk versions top to bottom. For each version walk downloaders in order. First file that passes ValidateAPK and stock identity wins. `run`, `smoke`, and `download` share that gate. CDP is an upgrade: each downloader decides whether it can finish. A downloader that cannot finish MUST fail that downloader only. `revancedbot` MUST continue with the next downloader. Exhausted versions → skip the Job | Stock APK path under Cache. Exhausted → skip |
| TEC-04 | Cached patches file | Every package the patches advertise is a Job. Each `run` and `smoke` shuffles that list before work. One success per Job per run. First version that downloads and patches wins. Package work uses Isolate: one fail does not cancel siblings | Job list + per-package outcome |
| TEC-05 | Stock APK + SigningBlob + patches | ReVanced CLI applies patches. Package-rename patch always on. F-Droid id is stock id + `.revanced`. After patch, host `apksigner` signs the APK with the same SigningBlob that signs the F-Droid index. ReVanced CLI MUST NOT receive `--keystore` | Patched APK under Cache work |
| TEC-06 | Pasteable secret | `keys generate` runs `keytool` and prints one blob. The blob alias is `revancedbot`. No operator `--alias`. A run that signs MUST validate the blob and materialize the keystore only under Cache | SigningBlob; keystore file under Cache |
| TEC-07 | Stage tree + host `fdroid` | Simple-binary `fdroid update` on the stage. Same SigningBlob signs the index. On success, atomic publish into live Repo. Failure leaves the previous publish | Published tree |
| TEC-08 | Long work on an interactive terminal | Schedule real work through the Session (`Go`, `Map`, `Each`, `Isolate`). TUI starts lazily on the first scheduled task. Same TTY / `CI` / `NO_COLOR` / `TERM=dumb` guards as workspaced. Force TUI with `WORKSPACED_FORCE_TUI`. No `REVANCEDBOT_*` TUI flag. `keys generate` MUST NOT schedule a progress task | Progress UI when a task is scheduled on an interactive terminal. Otherwise logs |

ValidateAPK: size above 1 KiB, ZIP `PK` magic, not HTML, contains `AndroidManifest.xml`.

Stock identity: host `aapt` package id MUST equal the requested stock id. When a version was requested, versionName MUST prefix-match that version (`3.3` accepts `3.3.6`). Empty requested version skips the version check. A rejected file is deleted. A Cache name-hit uses ValidateAPK then stock identity. A Cache hit that fails identity is deleted and fetched again.

Stock id aliases (closed set), applied before fetch: `com.youtube.android` and `com.google.youtube` → `com.google.android.youtube`; `com.youtube.music` → `com.google.android.apps.youtube.music`.

Artifact preference inside one downloader: single APK, then universal / multi-ABI, then broad DPI.

## Tooling

| TEC | Tool | Relation | We do not | Cite |
|-----|------|----------|-----------|------|
| TEC-01 parse | Cobra, Viper | adopt | a second argv parser and a second config stack | `go.mod`, `internal/cli`, `internal/config` |
| TEC-01 publish | stage + rename | implement | in-place rewrite of live `repo/` | `path:internal/fdroid/atomic.go`, `stdlib:os.Rename` |
| TEC-02 | `os.Exit`, slog | implement | distinct exit codes beyond 0 and 1 | `path:cmd/revancedbot/main.go` |
| TEC-03 HTTP | workspaced `httpclient` + `fetchurl` | adopt | a bare `http.Client` that hides work from the Session | `path:internal/netx`, `path:internal/drivers/prelude.go` |
| TEC-03 store fetch | built-in downloaders | implement | Aurora, Play | `path:internal/download` |
| TEC-03 garbage | ValidateAPK | implement | Play-certificate identity | `path:internal/download/validate.go` |
| TEC-03 identity | host `aapt` | wrap | Play-certificate identity; exact versionName | `path:internal/apkmeta` |
| TEC-03 browser | `github.com/go-rod/rod` | wrap | rod launcher; in-process Chrome; a workspaced rod factory | none in workspaced `pkg/driver`; pattern `lewtec/fusionsolar-bot` `setupBrowser` |
| TEC-04 jobs | ReVanced CLI `list-versions` | wrap | a second job catalog | `path:internal/revanced/jobs.go` |
| TEC-04 isolate | workspaced `taskgroup` Isolate / Map | adopt | a sequential side path for package work | `path:internal/app/pipeline.go` |
| TEC-05 patch | ReVanced CLI `java -jar` | wrap | a second patcher | `path:internal/revanced/patch.go` |
| TEC-05 sign | host `apksigner` | wrap | `--keystore` on ReVanced CLI | `path:internal/apksign/sign.go` |
| TEC-06 | `keytool` | wrap | operator-facing keytool flags in the happy path | `path:internal/signing` |
| TEC-07 | `fdroid` on PATH | wrap | `fdroid build` | `path:internal/fdroid` |
| TEC-08 | workspaced `taskgroup` Session | adopt | a custom progress framework | `path:internal/cli/root.go` |
| logs | workspaced `logging.NewPlainHandler` | adopt | stdlib `TextHandler` as the product logger | `path:internal/cli/root.go` |
| GitHub metadata | `google/go-github` | adopt | a hand-rolled Releases client | `go.mod` |
| ReVanced CLI jar + patches | GitHub + GitLab tag + mirrors | wrap | jars inside this project’s release artifact | `path:internal/revanced/tools.go` |

| Cell | Pick | C or D | Implements | Cite if C |
|------|------|--------|------------|-----------|
| Language | Go | C | TEC-01–TEC-08 | `go.mod` |
| Runtime | this binary + host `java` / `fdroid` / `keytool` / `apksigner` | C | TEC-05–TEC-07 | `internal/toolscheck` |
| Persistence | files: Repo + Cache | C | TEC-01, TEC-07 | `internal/workspace` |
| UI | workspaced lazy TUI | C | TEC-08 | `internal/cli/root.go` |
| Packaging | GoReleaser → GitHub Releases → consumer mise pin | D | distribution of this binary | this repository’s `mise.toml` names `goreleaser` |
| Identity | SigningBlob | C | TEC-06 | `internal/signing/blob.go` |
| Host OS | Linux | C | consumer CI and e2e | `e2e/`, `mise.toml` |

Minimal driver prelude: blank-import `httpclient/native` and `fetchurl` only. Not the full workspaced desktop prelude.

Rod: `rod.New().ControlURL(cdp).Connect()` only. Never launch a browser. The Operator supplies the CDP URL. `REVANCEDBOT_CDP_URL` overrides `browser.cdp_url`. The CDP host (Browserless included) belongs to the consumer workflow.

## Terminology

| Concept | Approved | Banned |
|---------|----------|--------|
| Positional F-Droid simple-binary root | Repo | output dir, fdroid root (as the type), site |
| Disposable work directory | Cache | tmp (as the type), CI cache |
| Operator-owned YAML in Repo | AuthorityDoc | config (alone), settings, `config.yml` |
| Generated fdroidserver YAML | `config.yml` | authority, source of truth |
| Pasteable signing secret | SigningBlob | keystore (as the secret), p12 blob |
| One stock package + version list | Job | app, task (as the package unit) |
| Built-in stock fetch implementation | downloader | source (as the type), scraper (as the type) |
| Chrome DevTools Protocol endpoint | CDP URL | browser (as the URL), webdriver |
| F-Droid package id after rename | patched id | revanced package (as the field) |
| Stock Android package id | stock id | app id (alone) |
| Person who runs the binary | Operator | user, consumer (as the actor) |
| Repository that hosts and deploys the tree | consumer repo | this repo (as that tree) |
| workspaced Session progress | Session | progress bar framework, TUI app |

`REPO` in command usage means the Repo path argument. `CACHE` in prose means the Cache directory.

## Types

### Command-line interface

Grammar: `revancedbot <command> [REPO]`. Persistent flags: `--cache` (empty → temporary directory), `--config` (empty → `REPO/revancedbot.yaml`).

| Command | Type it mutates | Transition | Bad input |
|---------|-----------------|------------|-----------|
| `run REPO` | Repo | TEC-03–TEC-08 then TEC-07 | missing AuthorityDoc, missing tools, bad SigningBlob, tool fetch fail, `fdroid update` fail, publish fail → exit 1. Package fail → skip |
| `smoke REPO` | Repo | same until `--max` successes | same plumbing. Zero successes → exit 1 |
| `fdroid-init REPO` | Repo | seed stage, write stage `config.yml`, publish layout | missing AuthorityDoc, bad SigningBlob, publish fail → exit 1 |
| `fdroid-update REPO` | Repo | seed stage, `fdroid update`, publish | missing AuthorityDoc, missing tools, `fdroid` fail, publish fail → exit 1 |
| `fetch-tools REPO` | Cache | latest CLI jar + patches into Cache | resolve fail, download fail → exit 1 |
| `download REPO` | Cache | one stock APK via TEC-03 | missing AuthorityDoc, missing `--package`, every downloader fails, identity fail after fetch → exit 1 |
| `patch REPO` | Cache | TEC-05 on `--in` → `--out` | missing `--in` / `--out`, missing tools, CLI fail, `apksigner` fail → exit 1 |
| `list-jobs REPO` | none (stdout) | TEC-04 print | tool fetch fail, parse fail → exit 1 |
| `keys generate` | none (stdout) | TEC-06 print one line | missing `keytool` → exit 1 |
| `keys validate REPO` | Cache | materialize keystore | missing SigningBlob, invalid SigningBlob → exit 1 |
| `version` | none | print | extra args → Cobra default |

### Stored entities

| Entity | Kind | Identity authority | A/B rels `(min,max)` | Root | Invariant IDs |
|--------|------|--------------------|----------------------|------|---------------|
| Repo | entity | Operator path (positional) | Repo `(1,1)` — AuthorityDoc `(1,1)`; Repo `(1,1)` — PublishedPackage `(0,*)` | yes | INV-01, INV-02, INV-03, INV-08 |
| AuthorityDoc | weak entity | `REPO/revancedbot.yaml` (Operator file) | AuthorityDoc `(1,1)` — Repo `(1,1)` | no | INV-03 |
| PublishedPackage | weak entity | `(patched id, versionCode)` in `REPO/repo` | PublishedPackage `(1,1)` — Repo `(1,1)` | no | INV-04 |
| Job | value | — (from current patches each run) | — | — | INV-05, INV-09 |
| SigningBlob | value | `REVANCEDBOT_SIGNING` | SigningBlob `(1,1)` — Cache keystore `(0,1)` | — | INV-02, INV-08 |
| Cache | value | `--cache` when set; a temporary directory when `--cache` is absent | Cache `(1,1)` — stage `(1,1)` | — | INV-01, INV-02, INV-09 |

### Relationships

| Rel | A role | B role | A (min,max) | B (min,max) | Identifying? | Owner | Ban |
|-----|--------|--------|-------------|-------------|--------------|-------|-----|
| has | Repo | AuthorityDoc | (1,1) | (1,1) | yes | path under Repo | `revancedbot` rewrite of the YAML |
| contains | Repo | PublishedPackage | (0,*) | (1,1) | yes | files under `REPO/repo` | write those files outside publish |
| materializes | SigningBlob | Cache keystore | (1,1) | (0,1) | no | `CACHE/signing/keystore.jks` | keystore under Repo |

Cache is a workspace, not a second aggregate root. An explicit `--cache` path is still disposable.

### AuthorityDoc fields

| Field | Role |
|-------|------|
| `repo_name`, `repo_url`, `repo_description` | branding copied into generated `config.yml` |
| `downloaders` | ordered ids from the built-in set. Empty → `aptoide`, `apkpure`, `apkmirror` |
| `pool_io`, `pool_cpu`, `pool_internet` | positive overrides. Omitted → workspaced defaults with Internet = 2 |
| `log_level` | log verbosity |
| `browser.cdp_url` | CDP URL. Empty is valid. Top-level `cdp_url` is accepted when this is empty. Env `REVANCEDBOT_CDP_URL` wins when set |

### SigningBlob fields

`v` MUST be 1. Required: `keystore_b64`, `storepass`, `keypass`, `alias`. `storetype` defaults to `JKS`. Encoding: one line, base64 of JSON. Raw JSON is accepted on decode.

### Public contract

| Item | Rule |
|------|------|
| Repo argument | Positional on every command that names `REPO` |
| `--cache` | Absent → create a temporary directory |
| `--config` | When set, that file MUST exist and is the AuthorityDoc |
| `REVANCEDBOT_SIGNING` | Required on `run`, `smoke`, `fdroid-init`, `fdroid-update`, `patch`, `keys validate`. `REVANCEDBOT_SIGNING_BLOB` is accepted when this is empty |
| `GITHUB_TOKEN` | MAY be set for GitHub rate limits. `REVANCEDBOT_GITHUB_TOKEN` is accepted when this is empty |
| Exit | 0 success. 1 any returned error. No other product codes |
| stdout | `keys generate`: one blob line. `list-jobs`: `package_id` then tab then comma-separated versions; empty version is the token `Any`. `download`: downloader id, tab, sha256, tab, path. Cache hit: `cache`, tab, path |
| stderr | logs. While the TUI is active, Session routing matches workspaced |

`revancedbot` sets `REVANCEDBOT_KEYSTORE_PASS` and `REVANCEDBOT_KEY_PASS` for the `fdroid` child only. Those names MUST NOT be committed.

## Invariants

| ID | Predicate | On | Forbidden bypass |
|----|-----------|----|------------------|
| INV-01 | Live Repo `{repo,metadata,config.yml}` change only via atomic publish from Cache stage. A failed run leaves the previous publish | Repo | write APKs and indexes straight into live `REPO/repo` |
| INV-02 | Keystore and secrets never live under Repo | SigningBlob, Repo | `keystore:` path under Repo |
| INV-03 | `revancedbot` never overwrites AuthorityDoc | AuthorityDoc | generate `revancedbot.yaml` |
| INV-04 | F-Droid id is stock id + `.revanced` | PublishedPackage | leave the stock id in the index |
| INV-05 | One successful version per Job per run | Job | ship two patched versions of one stock id in one run |
| INV-06 | Rod never launches a browser | TEC-03 | `launcher` package, local Chrome start |
| INV-07 | ReVanced jars and patches are not inside this project’s release artifact | packaging | attach `.jar` / `.rvp` to GoReleaser |
| INV-08 | The same SigningBlob signs patched APKs and the F-Droid index | SigningBlob | a second key for the index |
| INV-09 | A kept stock APK has package id equal to the requested stock id. When a version was requested, versionName prefix-matches | Job, Cache | accept after ValidateAPK only; exact versionName |

## Errors

| Public operation | Bad input | One reaction |
|------------------|-----------|--------------|
| `run REPO` | missing AuthorityDoc, missing host tools, missing SigningBlob, invalid SigningBlob, tool fetch fail, `fdroid update` fail, publish fail | exit 1; live Repo unchanged if publish did not succeed |
| `run REPO` | one Job fails download, patch | skip that Job; continue; include it in the end summary |
| `run REPO` | every Job skipped | still run TEC-07; exit 0 if publish succeeds |
| `smoke REPO` | plumbing as `run` | exit 1 |
| `smoke REPO` | zero Jobs succeed | exit 1; do not publish a success |
| `fdroid-init REPO` | missing AuthorityDoc, bad SigningBlob, publish fail | exit 1 |
| `fdroid-update REPO` | missing AuthorityDoc, missing tools, `fdroid` fail, publish fail | exit 1 |
| `fetch-tools REPO` | resolve fail, download fail | exit 1 |
| `download REPO` | missing AuthorityDoc, missing `--package` | exit 1 |
| `download REPO` | every downloader fails (including give-up without CDP) | exit 1 |
| `download REPO` | identity fail after fetch | exit 1; delete the file |
| `download REPO` | Cache hit fails identity | delete the file; fetch again |
| stock identity | package id mismatch, or requested version does not prefix-match | delete the file; that version fails |
| `patch REPO` | missing flags, CLI fail, `apksigner` fail | exit 1 |
| `list-jobs REPO` | fetch fail, parse fail | exit 1 |
| `keys generate` | missing `keytool` | exit 1; no stdout blob |
| `keys validate REPO` | missing SigningBlob, invalid SigningBlob | exit 1 |
| downloader.Fetch | CDP URL set but connect fails | fail that downloader; caller tries the next id |
| downloader.Fetch | page needs a browser and CDP is absent | fail that downloader; caller tries the next id |
| ValidateAPK | small body, HTML, not ZIP, no manifest | delete the file; treat as downloader failure |
| unknown downloader id | id not in the built-in set | fail that id; try the next |
| Cache name-hit | file exists but ValidateAPK fails | delete; fetch again |

## Actors

| Actor | Obligations |
|-------|-------------|
| Operator | Places AuthorityDoc. Supplies `REVANCEDBOT_SIGNING`. Provides host tools on `PATH`. Optionally supplies a CDP URL. Deploys the written Repo with some other system |

## Capabilities

| ID | Actor | Sea-level goal |
|----|-------|----------------|
| CAP-01 | Operator | Produce a SigningBlob and keep it out of Repo |
| CAP-02 | Operator | Build a simple-binary Repo from current ReVanced patches |
| CAP-03 | Operator | Smoke-test until N packages succeed |
| CAP-04 | Operator | Fetch one stock APK into Cache |
| CAP-05 | Operator | Patch one APK into Cache |
| CAP-06 | Operator | List Jobs from cached patches |

## Quality

| Concern | Measure. If none, why it cannot happen |
|---------|----------------------------------|
| Exit contract | `cmd/revancedbot/main.go` maps any returned error to exit 1. TEC-02 names the skip and empty-success cases. Tests can assert those exits |
| Untrusted input | Store HTML and APK bodies are untrusted. ValidateAPK then stock identity (INV-09). A bad SigningBlob refuses to start. No Play-certificate check (non-goal 9) |

## Security

In scope:

- SigningBlob in the environment, never in Repo
- Keystore file only under Cache, mode 0600
- Generated `config.yml` uses `{env: …}` for passwords
- Untrusted download bodies (TEC-03)

Why “no security” cannot be claimed: `revancedbot` handles a signing secret and writes installable APKs.

Residual risk: a hostile store can still serve a real APK whose package id matches the requested stock id (and whose versionName prefix-matches, when a version was requested).

## Success

- [ ] After a failed `run`, live Repo `{repo,metadata,config.yml}` match the last successful publish.
- [ ] A patched APK published into Repo has patched id equal to stock id + `.revanced`.
- [ ] `keys generate` prints exactly one line on stdout.
- [ ] `run` exits 0 when publish succeeds and every Job was skipped.
- [ ] `smoke` exits 1 when zero packages succeed.
- [ ] `run` with no `REPO/revancedbot.yaml` and no `--config` exits 1.
- [ ] ValidateAPK rejects an HTML body.
- [ ] `download` of an APK whose package id is not the requested stock id exits 1 and does not keep the file.
- [ ] `keys generate` does not accept `--alias`.
- [ ] `revancedbot` does not write `revancedbot.yaml`.
- [ ] No keystore file exists under Repo after a successful run.
- [ ] The process does not start a local Chrome (INV-06).

## Later work

1. Marketing listing scrapers (Play, APKMirror copy).
2. Automatic cleanup of historical APKs.
3. Incremental skip of download and patch work when a version is already present.
4. Content-addressed Cache and CI cache save/restore.
5. Consumer-repo public versus private.

## Assumptions

| ID | Fact | If false |
|----|------|----------|
| AS-01 | ReVanced CLI `list-versions` text remains parseable as Jobs | `list-jobs` and `run` fail closed until the parser changes |
| AS-02 | A host can put `java`, `keytool`, `fdroid`, `apksigner`, and `aapt` on `PATH` | preflight fails with a missing-tool list |
| AS-03 | Latest patches are reachable via `REVANCEDBOT_PATCHES_FILE`, then `REVANCEDBOT_PATCHES_URL`, then GitLab tags, then GitHub assets, then a known mirror | `fetch-tools` fails; no silent old patches |
| AS-04 | An Operator who wants the rod upgrade can reach a CDP URL from the `revancedbot` process | downloaders that need a browser give up; others continue |

## Decision history

- Two directories (Repo vs Cache) and simple-binary output only. Rejected: dump APKs to Pages from this repository; `fdroid build`.
- Signing via one pasteable blob and `keytool` behind `keys generate`. Rejected: operator-managed keytool flags; keystore in Repo.
- ReVanced CLI patches; host `apksigner` signs the APK with the same SigningBlob as the F-Droid index. Rejected: pass `--keystore` to ReVanced CLI.
- Progress is workspaced Session. Rejected: a custom bar framework.
- Always stage a successful patch, including a versionCode already in the tree. Rejected: skip staging on duplicate versionCode.
- Rod connects to an Operator CDP URL only, same as fusionsolar-bot. Rejected: in-process launcher; require CDP for `revancedbot` to start.
- Missing AuthorityDoc refuses `run`, `smoke`, `fdroid-init`, `fdroid-update`, and `download`. Rejected: hidden built-in defaults for those commands.
- Version numbers live in `go.mod` and `mise.toml`. Rejected: copied pins in this file.
- Stock identity after ValidateAPK: stock id match and versionName prefix. Same gate on `download`. Rejected: ValidateAPK-only; Play-certificate identity; exact versionName.
- `keys generate` has no `--alias`; blob alias is `revancedbot`. Rejected: operator `--alias` flag.
