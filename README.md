# BMCShell

BMCShell 3.0 主程序仓库。当前开发版包含 Go 核心、Bash 会话适配、本地包管理、帮助、设置和数据查询。各扩展使用自己的仓库和发布版本。

- [框架介绍](doc/README.md)
- 源码仓打包入口 `scripts/pack-release.ps1`
- 插件脚手架在独立的 `BMCShell-Scaffold` 仓库构建和发布

在 MSYS2 UCRT64 中，从本仓库构建并加载。

```bash
go test ./...
mkdir -p build
go build -trimpath -o build/BMCShell.exe ./cmd/bmcshell
source ./shell/bmcshell.sh
bmc help
bmc use 192.0.2.10
bmc status
bmc exit
```

编译产物放在 `build/`。在 PowerShell 7.4 或更新版本中运行 `./scripts/pack-release.ps1 -Version 3.0.0-dev.8`，将 Windows 发布 ZIP 和校验清单写入 `dist/`。

解压发布包后在 Bash 加载 `bin/bmcshell.sh`。[安装、自动加载与恢复](doc/README.md#安装更新与恢复)见框架文档。安装目录默认也用于保存用户数据。默认的 `generic` 解析器仅设置 IP。示例插件使用演示数据，可以在临时 `BMCSHELL_HOME` 中验证。开源许可证待维护者确认。
