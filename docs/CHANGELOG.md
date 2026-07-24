# Changelog

All notable changes to GoQuark are documented in this file.

Format loosely follows [Keep a Changelog](https://keepachangelog.com/).  
Versioning follows the root [`VERSION`](../VERSION) file (semver).

## [Unreleased]

### Added

### Changed

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
