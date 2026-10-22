# Changelog

All notable changes to GoQuark are documented in this file.

Format loosely follows [Keep a Changelog](https://keepachangelog.com/).  
Versioning follows the root [`VERSION`](../VERSION) file (semver).

## [Unreleased]

### Added

### Changed

## [1.0.5] — 2026-10-22

### Added
- The update dialog and `goquark update` show the release notes. In the TUI
  they collapse to six lines with a `d` / clickable expand button; the text is
  stripped of escape sequences and capped before it reaches the terminal

### Fixed
- Self-update staged the download at a fixed `goquark.new`, so two updaters at
  once — the TUI and the MCP server, say — raced: whichever installed first
  left the other reporting `chmod ...goquark.new: no such file or directory`
  even though the update had succeeded. Staging now uses a unique name under a
  lock, and a second updater is told to wait instead
- An update is now also serialised within one process, so a caller that starts
  several update goroutines downloads the release once rather than once per
  goroutine. Both cases report `update.ErrUpdateInProgress`, which the TUI and
  CLI present as "wait for the running update" rather than as a failure
- Self-update no longer unlinks the running binary when it cannot be moved
  aside; a failure at that point used to leave nothing to run
- Windows cannot delete the image of a running process, so the replaced binary
  stayed behind after every update. It is now expected, reported, and removed
  at the next start, along with any staging file an interrupted download left

### Changed
- The GitHub Release body is now the CHANGELOG section for that version,
  instead of only a link to the commit comparison

## [1.0.4] — 2026-10-22

### Security
- Dependency `refraction-networking/utls` 1.6.7 → 1.8.2: ServerHellos were
  accepted without checking the TLS 1.3 downgrade canaries
  (GHSA-pmc3-p9hx-jq96), and the Chrome parrots were detectable through a
  missing padding extension (CVE-2026-26995) and a GREASE ECH mismatch
  (CVE-2026-27017) — every HTTPS request GoQuark makes handshakes through uTLS
- Dependency `klauspost/compress` 1.17.4 → 1.20.3: out-of-bounds read in the
  s2 decoder (GHSA-259r-337f-4rfw)
- Dependencies `golang.org/x/crypto` 0.30.0 → 0.58.0 and `golang.org/x/sys`
  0.28.0 → 0.49.0: clears CVE-2024-45337 and some twenty further advisories.
  All of them sit in `ssh`, `ssh/agent`, `knownhosts` and `openpgp`, which
  GoQuark never imports, so none was reachable — the bump keeps dependency
  scanners quiet rather than closing a live hole

### Changed
- Dependencies `golang.org/x/text` 0.21.0 → 0.43.0 and `golang.org/x/sync`
  0.10.0 → 0.24.0; `cloudflare/circl` is gone, uTLS 1.8 no longer needs it
- `go.mod` Go directive 1.23 → 1.26.0, as the upgraded modules require.
  `GOTOOLCHAIN=auto` (the default) fetches a matching toolchain on demand, so
  only `GOTOOLCHAIN=local` builders need to upgrade Go themselves

## [1.0.3] — 2026-08-31

### Security
- Self-update verifies the release `SHA256SUMS` before replacing the binary; a
  digest mismatch, a missing manifest, or an asset the manifest does not list
  aborts the update
- Installing a build from a repository other than `ButterFuture/GoQuark` now
  requires `goquark update --allow-custom-repo`, so `GOQUARK_UPDATE_REPO` alone
  can no longer redirect a binary replacement
- Update downloads are restricted to GitHub hosts, and `GITHUB_TOKEN` /
  `GH_TOKEN` are only ever sent there
- Download file names strip both path separators, so a server-supplied
  `..\..\name` can no longer escape the download directory on Windows
- QR login artifacts (`qrcode.png`, `qrcode.url.txt`) move from the working
  directory into the config directory and are written `0600` — the encoded URL
  is a live login token until it is scanned or expires
- `scripts/install.sh` fails closed when `SHA256SUMS` is absent or does not list
  the asset, and matches the asset name exactly instead of by substring

### Changed
- Dependency `cloudflare/circl` 1.3.7 → 1.6.3 (GO-2026-4550, GO-2025-3754)
- CI builds with a current patched Go toolchain rather than the `go.mod`
  minimum: release binaries carry the standard library statically

## [1.0.2] — 2026-07-25

### Added
- MCP QR login for agents: `goquark_login` / `goquark_login_poll` with labeled `qr_url` / `qr_content` / `qr_ascii`
- MCP tools: `goquark_status`, `goquark_stat`, download queue list/control, structured `goquark_ls`
- TUI screenshot in README; social preview asset for Open Graph

### Changed
- MCP download supports default download dir and `wait=false` enqueue
- README MCP section: collapsed tool list (`更多：MCP工具一览`); hosts auto-discover tools

### Fixed
- MCP config reload no longer copies `sync.Mutex` (go vet / CI)

## [1.0.1] — 2026-07-24

### Added
- Self-update: CLI `goquark update` / `update --yes`, MCP check/apply tools, TUI startup prompt + key `u`
- Config `disable_update_auto_check` (startup auto-check only)
- High-res README banner (`docs/banner.png`) with white title outline for dark mode
- Mermaid component architecture in README (zh/en)
- One-liner install script + `docs/BUILD.md` for optional source builds

### Changed
- TUI letter shortcuts case-insensitive; help text lowercase
- Update dialogs simplified; default keep auto-check on
- Docs legal pack under `docs/` (DISCLAIMER / CHANGELOG / ACKNOWLEDGMENTS)

### Fixed
- Modal footer “可点击” color on dark terminals
- Confirm button mouse hit-testing for custom labels

## [1.0.0] — 2026-07-24

### Added
- First public-facing release snapshot on `ButterFuture/GoQuark`
- QR login, browse, multi-connection download, TUI download center
- Pause / resume / cancel, progress, ETA, download history
- MCP stdio tools (`device`, `whoami`, `ls`, `download`)
- Multi-arch GitHub Actions CI template
- VERSION file + `scripts/build.sh` ldflags injection
- AGPL-3.0 license; takedown contact `ButterFuture@proton.me`

### Notes
- Unofficial; not affiliated with Quark / UC / Alibaba
- Public repo uses clean single-commit snapshots (no private dev history)

## [0.2.0] — 2026-07-24 (dev)

### Added
- VERSION-driven builds, product README rewrite

## [0.1.0] — 2026-07-23 (dev)

### Added
- Initial private development: login + download + MCP foundation
