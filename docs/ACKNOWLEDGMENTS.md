# Acknowledgments / Third-party notices

GoQuark includes or depends on open-source software.  
This file is the project **compliance notice** for third-party components  
(similar to an “Open Source Acknowledgments” / NOTICE list).

**GoQuark original code:** [AGPL-3.0](../LICENSE) · Copyright (c) 2026 ButterFuture  

Third-party packages retain **their own** licenses. Redistribution of GoQuark  
binaries or source must continue to respect those licenses in addition to AGPL-3.0  
for GoQuark’s own code.

Generated from `go.mod` / module LICENSE headers (verify against module cache when updating deps).

---

## Direct dependencies

| Module | License (as declared) | Notes |
|--------|------------------------|--------|
| [github.com/charmbracelet/bubbles](https://github.com/charmbracelet/bubbles) | MIT | TUI components |
| [github.com/charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) | MIT | Terminal UI framework |
| [github.com/charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss) | MIT | Terminal styles |
| [github.com/mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) | MIT | MCP server |
| [github.com/refraction-networking/utls](https://github.com/refraction-networking/utls) | BSD-3-Clause | TLS (Go Authors / uTLS) |
| [github.com/skip2/go-qrcode](https://github.com/skip2/go-qrcode) | MIT | QR code generation |

---

## Indirect / transitive (selected)

These appear via `go.mod` `// indirect` and are linked into typical builds:

| Module | License (as declared) |
|--------|------------------------|
| github.com/andybalholm/brotli | MIT |
| github.com/atotto/clipboard | BSD-3-Clause |
| github.com/aymanbagabas/go-osc52/v2 | MIT |
| github.com/charmbracelet/x/ansi | MIT |
| github.com/charmbracelet/x/term | MIT |
| github.com/cloudflare/circl | BSD-3-Clause |
| github.com/erikgeiser/coninput | MIT |
| github.com/google/uuid | BSD-3-Clause |
| github.com/klauspost/compress | BSD-3-Clause / multi (see module) |
| github.com/lucasb-eyer/go-colorful | MIT |
| github.com/mattn/go-isatty | MIT |
| github.com/mattn/go-localereader | No LICENSE file in module (tiny helper; see upstream README) |
| github.com/mattn/go-runewidth | MIT |
| github.com/muesli/ansi | MIT |
| github.com/muesli/cancelreader | MIT |
| github.com/muesli/termenv | MIT |
| github.com/rivo/uniseg | MIT |
| github.com/sahilm/fuzzy | MIT |
| github.com/spf13/cast | MIT |
| github.com/yosida95/uritemplate/v3 | BSD-style (see module LICENSE) |
| golang.org/x/crypto | BSD-3-Clause |
| golang.org/x/sync | BSD-3-Clause |
| golang.org/x/sys | BSD-3-Clause |
| golang.org/x/text | BSD-3-Clause |

---

## Standard library

Go standard library components are © The Go Authors and used under the  
[Go license](https://go.dev/LICENSE) (BSD-style).

---

## How to regenerate

When upgrading dependencies:

```bash
# Inspect declared licenses in module cache
go list -m all
# Review LICENSE files under $(go env GOMODCACHE)/...
```

Update this document when **direct** dependencies or their licenses change.

---

## Notices

- This list is provided in good faith for **attribution and compliance**.  
- It is **not** legal advice. For redistribution (especially commercial), review each license text.  
- Full AGPL-3.0 obligations for GoQuark itself are in [`LICENSE`](../LICENSE) and [`COPYING`](COPYING).  
- Product disclaimer: [`DISCLAIMER.md`](DISCLAIMER.md).
