# 从源码编译（可选）

> 一般用户请用 [README 安装说明](../README.md#安装)：一键脚本或 GitHub Release，**无需安装 Go**。  
> 本节给需要改代码、交叉编译或离线构建的开发者。

## 依赖

- Go **1.22+**（推荐 1.23）
- `git`、`make` 可选

## 克隆与构建

公开仓库：

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

## 交叉编译

```bash
./scripts/build.sh linux amd64
./scripts/build.sh linux arm64
./scripts/build.sh darwin arm64
./scripts/build.sh windows amd64
# → dist/goquark-<os>-<arch>
```

`scripts/build.sh` 会读取仓库根目录 [`VERSION`](../VERSION)，并通过 `-ldflags` 写入：

- `main.Version`
- `main.Commit`（若可得）
- `main.BuildDate`

## 校验

```bash
./bin/goquark version
./bin/goquark help
```

自编译产物若无法匹配 Release 资产命名/校验，**应用内「自动更新」可能不可用**，但仍可通过 `goquark update` 查看是否有新版本。可继续用本页方式重新编译，或改用 [Release / 安装脚本](../README.md#安装)。

## 相关

- 更新日志：[CHANGELOG.md](CHANGELOG.md)
- 第三方许可：[ACKNOWLEDGMENTS.md](ACKNOWLEDGMENTS.md)
- 协议：根目录 [LICENSE](../LICENSE)（AGPL-3.0）

---

# Building from source (optional)

> Prefer the [README install section](../README.en.md#install): one-liner or GitHub Releases — **no Go required**.  
> This page is for contributors and custom builds.

## Requirements

- Go **1.22+** (1.23 recommended)
- `git`

## Clone & build

```bash
git clone https://github.com/ButterFuture/GoQuark.git
cd GoQuark
go mod tidy
./scripts/build.sh          # → ./bin/goquark
# or: go build -o goquark ./cmd/goquark
```

## Cross-compile

```bash
./scripts/build.sh linux arm64
# → dist/goquark-linux-arm64
```

Version comes from the root `VERSION` file via `-ldflags`.

## Note on self-update

Self-built binaries may not match Release asset names; in-app auto-update can be unavailable. Use Releases or rebuild from this guide when upgrading.
