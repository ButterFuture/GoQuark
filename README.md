# GoQuark

[English](README.en.md) · 中文

<p align="center">
  <strong>非官方</strong> 夸克网盘 CLI / TUI / MCP 客户端
</p>

<p align="center">
  扫码登录 · 高速下载 · 交互式网盘 · Agent 友好
</p>

<p align="center">
  <img alt="version" src="https://img.shields.io/badge/version-1.0.0-blue">
  <img alt="go" src="https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go">
  <img alt="license" src="https://img.shields.io/badge/license-AGPL--3.0-blue">
  <img alt="platform" src="https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey">
</p>

---

## 这是什么？

GoQuark 是用 **Go** 写的夸克网盘客户端，面向命令行与自动化场景：

| 场景 | 能力 |
|------|------|
| 登录 | 夸克 APP **扫码登录** |
| 下载 | **贴近真实客户端** 的下载速度；队列、暂停/继续、进度与 ETA |
| 浏览 | 终端 **TUI**：点选、多选、右键菜单、下载中心 |
| 脚本 | CLI + 管道 / `--json` 输出 |
| Agent | **MCP**（stdio），可接入 Claude Desktop、Cursor 等 |

> **非官方项目。** 与夸克、UC、阿里巴巴集团及其关联公司无隶属、授权或合作关系。  
> 仅供学习与研究，风险自负。详见 [免责声明](#免责声明) 与 [DISCLAIMER.md](docs/DISCLAIMER.md)。

---

## 功能一览

- 扫码登录，设备信息本地固定、可自定义名称  
- 多文件下载、下载中心、历史记录  
- 暂停 / 继续 / 取消；退出时安全收尾  
- 目录浏览、用户信息（`ls` / `whoami`）  
- 终端 UI（支持鼠标）  
- MCP 工具：设备、用户、列目录、下载  
- 版本号由构建注入（见 [版本](#版本)）

---

## 安装

**依赖：** Go 1.22+

```bash
git clone https://github.com/ButterFuture/GoQuark.git
cd GoQuark
go mod tidy

# 推荐：从 VERSION 注入版本号
./scripts/build.sh
# 产物：./bin/goquark

# 或普通构建（version 显示为 dev）
go build -o goquark ./cmd/goquark
```

交叉编译示例：

```bash
./scripts/build.sh linux arm64
# → dist/goquark-linux-arm64
```

正式二进制将通过 GitHub Releases 发布（含校验和）。

---

## 快速开始

```bash
./bin/goquark login          # 扫码登录（终端出码 + qrcode.png）
./bin/goquark device         # 设备信息
./bin/goquark whoami         # 当前用户
./bin/goquark ls /           # 列目录
./bin/goquark download /云盘路径/文件.mkv
./bin/goquark downloads      # 任务列表
./bin/goquark downloads --watch
./bin/goquark tui            # 交互界面
./bin/goquark mcp            # MCP（stdio）
./bin/goquark version
```

| 配置项 | 说明 |
|--------|------|
| 配置文件 | `~/.config/goquark/config.json` |
| 覆盖方式 | 环境变量 `GOQUARK_CONFIG` 或参数 `-config` |
| 默认下载目录 | `~/Downloads/GoQuark`（可用 `download_dir` / `GOQUARK_DOWNLOAD_DIR`） |

登录态与 Cookie **仅保存在本机**。请勿把含 Cookie 的配置提交到 Git 或发给他人。

---

## TUI 快捷键（摘要）

| 按键 | 作用 |
|------|------|
| 单击 / 空格 | 选择 |
| `d` | 下载所选 |
| `t` | 下载中心 |
| 右键 / `m` | 菜单 |
| `p` / `r` / `x` / `c` | 暂停 / 恢复 / 取消 / 清除已完成（中心内） |
| `q` | 退出（确认后安全暂停进行中的任务） |

---

## MCP

通过 **stdio** 提供服务：宿主进程启动 `goquark mcp`。

1. 构建并安装到固定路径  
2. 先执行一次 `goquark login`  
3. 在宿主中填写**绝对路径**

### Claude Desktop

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`  
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "goquark": {
      "command": "/path/to/goquark",
      "args": ["mcp"]
    }
  }
}
```

### Cursor / 其它 stdio 宿主

```json
{
  "mcpServers": {
    "goquark": {
      "command": "/path/to/goquark",
      "args": ["mcp"]
    }
  }
}
```

| Tool | 说明 |
|------|------|
| `goquark_device` | 设备信息 |
| `goquark_whoami` | 当前用户 |
| `goquark_ls` | 列目录（`path`，默认 `/`） |
| `goquark_download` | 下载（`remote`、`local`） |

---

## 版本

| 项目 | 说明 |
|------|------|
| 唯一来源 | 仓库根目录 [`VERSION`](VERSION)（当前 **1.0.0**） |
| 构建注入 | `scripts/build.sh` / CI 通过 `-ldflags` 写入二进制 |
| 本地 `go run` | 未注入时显示 `dev` |

```bash
./scripts/version.sh   # 打印版本
./scripts/build.sh     # 带版本构建
goquark version        # 查看已注入版本
```

发版建议：改功能 → 确认 → 更新 `VERSION` → 打 tag `v1.0.0` → CI / Release。

---

## 获取

请从本仓库的 [Releases](https://github.com/ButterFuture/GoQuark/releases) 下载二进制，或 clone 本仓库从源码构建。


## 技术栈

| 层级 | 选型 |
|------|------|
| 语言 | Go |
| TUI | Bubble Tea、Lip Gloss |
| 网络 | Go `net/http`、TLS |
| Agent | MCP（stdio） |
| 配置 | JSON（`~/.config/goquark/`） |

下载侧使用多连接 HTTP 与本地任务管理（队列、暂停/继续、进度 UI），目标是 **贴近真实客户端的下载速度**。

---

## 开源协议

本仓库原创代码采用 **[GNU Affero General Public License v3.0 (AGPL-3.0)](LICENSE)**。

- **要求保留版权与许可证声明**（署名）  
- **Copyleft**：修改后的版本若对外提供服务（含网络服务）或分发，须以 AGPL-3.0 提供对应源码  
- 第三方依赖各自许可证不变  
- 第三方致谢与许可证清单见 [`docs/ACKNOWLEDGMENTS.md`](docs/ACKNOWLEDGMENTS.md)  

```text
Copyright (c) 2026 ButterFuture
```

---

## 免责声明

**使用前请完整阅读。使用本软件即视为接受下列条款。**

1. **非官方** — 非夸克 / UC / 阿里产品，无任何官方担保。  
2. **学习研究** — 请勿用于违反法律或平台服务条款的行为。  
3. **风险自负** — 账号、数据、隐私、会员权益等后果由使用者承担。  
4. **版权** — 你下载与传播的内容合法性由你负责。  
5. **按现状提供** — 无适销性等担保；接口可能变更，无强制维护义务。  
6. **第三方库** — 遵守各自许可证。  

摘要见 [DISCLAIMER.md](docs/DISCLAIMER.md)。

### 权利通知

邮箱：`ButterFuture@proton.me`  
标题建议：`[GoQuark] 权利通知 / Takedown`  
请尽量提供：主体与联系方式、具体路径/提交、权利依据、希望的处理方式。

---

## 安全提示

- 勿在聊天、截图、CI 日志中泄露 `config.json` 中的 Cookie  
- 仅在可信环境运行 CLI / MCP  
- 安装二进制时请核对 Release 校验和  

---


## 文档目录

| 文档 | 说明 |
|------|------|
| [`docs/DISCLAIMER.md`](docs/DISCLAIMER.md) | 免责声明 |
| [`docs/CHANGELOG.md`](docs/CHANGELOG.md) | 更新日志 |
| [`docs/ACKNOWLEDGMENTS.md`](docs/ACKNOWLEDGMENTS.md) | 第三方依赖与许可证声明 |
| [`docs/README.md`](docs/README.md) | 文档索引 |
| [`LICENSE`](LICENSE) | AGPL-3.0 全文 |

## 状态

**v1.0.0** — 登录、浏览、TUI 下载中心、多文件下载（暂停/继续）、MCP 可用。

后续计划：公开仓正式发版通道、应用内检查更新（可选）、更丰富的 MCP 能力。

---

<p align="center">
  Made with Go · AGPL-3.0 · Unofficial
</p>
