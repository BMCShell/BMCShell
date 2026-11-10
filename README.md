# BMCShell

BMCShell 3.0 主程序仓库。当前开发版包含 Go 核心、Bash 会话适配、本地包管理、帮助、设置和数据查询。各扩展使用自己的仓库和发布版本。

- [框架介绍](doc/README.md)
- 源码仓打包入口 `scripts/pack-release.ps1`
- 插件脚手架在独立的 `BMCShell-Scaffold` 仓库构建和发布

在 MSYS2 UCRT64 中，从本仓库构建并加载。

```bash
go test ./...
go build -trimpath -o BMCShell.exe ./cmd/bmcshell
source ./shell/bmcshell.sh
bmc help
bmc use 192.0.2.10
bmc status
bmc exit
```

默认的 `generic` 解析器仅设置 IP。示例插件使用演示数据，可以在临时 `BMCSHELL_HOME` 中验证。开源许可证待维护者确认。
