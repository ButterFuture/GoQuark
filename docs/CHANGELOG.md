# Changelog

All notable changes to GoQuark are documented in this file.

Format loosely follows [Keep a Changelog](https://keepachangelog.com/).  
Versioning follows the root [`VERSION`](../VERSION) file (semver): a release
with only fixes raises the patch number (1.1.0 → 1.1.1), one that adds or
changes features raises the minor number (1.1.x → 1.2.0), and the major number
is kept for changes that break existing setups.

## [Unreleased]

## [1.2.0] — 2026-10-28

### Added
- WeChat QR login, the same way the PC client logs in with WeChat:
  `goquark login --wechat`, `Tab` on the TUI login screen to switch between
  Quark-app and WeChat scan (Quark stays the default), and `method: "wechat"`
  on the MCP tool `goquark_login`. Thanks to @litan1106 for the first version
- `goquark logout` and the MCP tool `goquark_logout`. Logging out, there and
  with `o` in the TUI, now ends the session on Quark's server first, as the PC
  client does, then clears it locally even if the server does not answer; the
  device profile is kept. Logging out in the TUI or over MCP also pauses the
  downloads, to resume after logging in again. Before, the TUI only forgot the
  session locally and the server kept it valid

### Changed
- Downloads run in a background download service shared by the TUI, MCP and
  `goquark download`, which now see one queue and one history. Before, each
  process kept its own and the last to save overwrote the others', which could
  lose or garble the download history when two were open. The service starts
  with the first download and exits when idle; an update never interrupts a
  download, the service moves to the new version once downloads finish.
  `GOQUARK_DOWNLOAD_SERVICE=off` keeps downloads in each process.
  `goquark download` is paused, not dropped, by Ctrl+C
- Quitting the TUI with downloads running asks, the first time, whether to
  pause them or let them finish in the background ("下次不再询问" ticked by
  default). The choice is `tui_quit_downloads` in `config.json`, and the MCP
  tool `goquark_download_settings` reads or changes it as `tui_quit`
- The TUI's bottom line shows only the shortcuts that fit the terminal, whole,
  and always ends with `?全部快捷键`; `?` opens the full list for the file
  browser or the download center, where pressing a listed key runs it. The
  shortcut keys also work with a Chinese input method (`？`, full-width letters)

### Fixed
- A large file paused, cancelled or hit by a failed part could show 100%:
  a paused one then turned into 已完成 with most of the file missing, and one
  paused right then could end up 已取消. Progress now stays at what is really
  downloaded, so a pause resumes where it stopped and a failure shows as one

### Security
- `goquark whoami --json` and the MCP tools `goquark_whoami` and
  `goquark_login_poll` no longer include the session's credentials (`pus`,
  `sToken`, `kfToken`, `mobilekps`) in the account info; only nickname, ids,
  membership and capacity are shown

## [1.1.0] — 2026-10-25

> **About the version number.** This release is 1.1.0, straight after 1.0.6.
> It is not a major update. Up to 1.0.6 every release raised the last number,
> fixes and features alike; from now on a release with only fixes raises the
> last number (1.1.0 → 1.1.1) and one that adds or changes features raises the
> middle one (1.1.x → 1.2.0). This one adds a feature, so it starts 1.1.
>
> **关于版本号：** 本次发布的版本号是 1.1.0，紧接 1.0.6。这不是大版本更新，只是规范了
> 版本号：此前不论修复还是新功能都只加最后一位，从这一版起，只含修复的发布加最后一位
> （1.1.0 → 1.1.1），加了或改了功能的发布加中间一位（1.1.x → 1.2.0）。本次带了一个新
> 功能，所以从 1.1 开始。

### Added
- `max_downloads` in the config sets how many files download at once, 1 to
  10 (default 3, as before); the rest queue. The download center's title shows
  how many slots are in use and how many files wait for one
- MCP tool `goquark_download_settings` reads or changes `max_downloads`. A
  change applies to the running queue at once and is saved to the config
  (`persist=false` keeps it to that session). `goquark_status` and
  `goquark_downloads` report the limit too, and `goquark_downloads` how many
  files are downloading and queued

### Changed
- The download center groups a folder download into one row instead of one
  row per file. The row's bar covers the total size of everything underneath,
  and it shows the combined speed, the time left, how many files are done and
  how many failed. Enter, → or a double-click opens it, one level at a time;
  Esc goes back up to the row it came from. Pause, resume and
  cancel in its menu act on the whole folder; cancelling one asks first.
  Queued files count with the size the drive listed, so the bar is right
  before they start. Folders downloaded with 1.0.6 that are still in the
  download history are grouped the same way after the update
- The download center's order: on every level folders come first and files
  below them, and each group runs downloading, failed, queued, paused,
  finished. Rows of the same status keep newest first at the top level and go
  by name inside a folder (Track 2 before Track 10). A row whose status
  changes shows the new status where it is and moves about 4 seconds later,
  rather than jumping away at once; a paused row stays where it was. The
  cursor stays on its row when the row moves
- The summary line counts downloading, failed, queued, paused and finished
  files separately; queued files used to count as downloading

### Fixed
- The download center did not scroll (#1, reported by @sushl15). With more
  tasks than fit, the terminal cut the overflow off the top and the
  highlighted row went off screen with it. The list now draws only the rows
  that fit and follows the cursor, PgUp/PgDn/Home/End move by page and to the
  ends, and long names are cut instead of wrapping
- A click in the download center selected the row above the one clicked
- Several files or folders ticked in the drive were queued in random order;
  they now queue in the order they are listed
- The download history kept the newest 200 finished files, so after a
  restart a folder of more than that showed only part of its files. It now
  keeps the newest 200 downloads, a folder counting as one however many files
  it holds, and drops older downloads whole; a folder with a file still
  unfinished is always kept in full

## [1.0.6] — 2026-10-25

### Added
- TUI downloads accept folders. Files and folders can be picked together and
  are fetched recursively, keeping the selected folder's own name, every
  subdirectory and the empty ones. Each path component is sanitised on its
  own, so a name the drive sends cannot climb out of the download directory,
  and a destination held by a running job, or occupied by anything that is
  not a plain file, is refused rather than written through. Two names that
  differ only in case or in Unicode composition count as one destination,
  because macOS and Windows both resolve them to a single file. At most 3
  files download at once and the rest queue; a link is fetched only once its
  job starts, so links no longer expire while a long queue drains. Scanning
  runs in the background and is dropped on quit or logout. The gap was
  pointed out by a fork from @WenchaoZeng; the implementation here is ours
- The update progress dialog has a 取消更新 button, and Esc aborts too. Nothing
  is installed until the download has been verified, so cancelling during it
  leaves the running version untouched. Once the update is in, the dialog has
  no cancel button left to press

### Fixed
- The part map of a multi-part download was read by the job dispatcher and
  by the periodic flush while the workers were writing it. Ranging over a
  map that is being written to is a hard runtime fault, not a stale read.
  Every access now goes through a mutex, and the flush encodes inside it
  rather than from a copy
- The part dispatcher could stay parked for the life of the process once
  every worker had given up on an error, with nothing left to drain the
  queue it was waiting on
- Clicking 立即更新 made the progress dialog flash and disappear, leaving the
  download running with nothing on screen. A mouse click is a press and then
  a release: the press started the update and swapped in the progress dialog,
  and the release was read by that new dialog as "click anywhere to dismiss".
  The release that belongs to a press already acted on is now dropped, which
  also stops a single click from activating two dialogs in a row
- A stray click no longer dismisses the update progress dialog either

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
