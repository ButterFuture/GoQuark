# Changelog

All notable changes to GoQuark are documented in this file.

Format loosely follows [Keep a Changelog](https://keepachangelog.com/).  
Versioning follows the root [`VERSION`](../VERSION) file (semver).

## [Unreleased]

### Added
- Self-update: CLI `goquark update`, MCP tools, TUI startup check + key `U`
- Config flag `disable_update_auto_check` (skip TUI auto prompt only)

### Changed
- Project docs consolidated under `docs/` (disclaimer, changelog, acknowledgments)

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
- Export script for public clean tree

## [0.1.0] — 2026-07-23 (dev)

### Added
- Initial private development: login + download + MCP foundation
