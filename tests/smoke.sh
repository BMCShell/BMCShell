#!/usr/bin/env bash
set -euo pipefail

if [[ -z ${BMCSHELL_HOME-} ]]; then
    printf 'Set BMCSHELL_HOME to a new, empty test directory.\n' >&2
    exit 2
fi
home=$(cygpath -u "$BMCSHELL_HOME")
if [[ ! -d $home || -n $(find "$home" -mindepth 1 -maxdepth 1 -print -quit) ]]; then
    printf 'BMCSHELL_HOME must be an empty test directory.\n' >&2
    exit 2
fi

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
scaffold=${BMCSHELL_SCAFFOLD:-../BMCShell-Scaffold/BMCShell-Scaffold.exe}
if [[ ! -f $scaffold ]]; then
    printf 'Build BMCShell-Scaffold in its repository or set BMCSHELL_SCAFFOLD.\n' >&2
    exit 2
fi
source ./shell/bmcshell.sh
fixture="$home/scaffold-fixtures"
mkdir -- "$fixture"
native_fixture=$(cygpath -aw "$fixture")
for kind in book library theme tool; do
    "$scaffold" create --name "bmcshell-$kind-smoke" -o "$native_fixture"
done
(cd "$fixture/bmcshell-tool-smoke" && mkdir bin && go build -o bin/tool.exe ./cmd/tool)
for kind in book library theme tool; do
    bmc plugin install --file "$native_fixture/bmcshell-$kind-smoke"
done
tool_id=$(bmc plugin list | awk '$2 == "bmcshell-tool-smoke" { print $4 }')
theme_id=$(bmc plugin list | awk '$2 == "bmcshell-theme-smoke" { print $4 }')
"$scaffold" create --name bmcshell-bundle-smoke -o "$native_fixture" --member "$tool_id"
bmc plugin install --file "$native_fixture/bmcshell-bundle-smoke"
source ./shell/bmcshell.sh

[[ $(bmc_bmcshell_tool_smoke_global) == 'BMCShell global tool is ready.' ]]
if declare -F bmc_bmcshell_tool_smoke_session >/dev/null; then exit 1; fi
[[ $(bmc book bmcshell-book-smoke -s 示例) == *'示例条目'* ]]
[[ $(bmc library query bmcshell-library-smoke DEMO01) == '示例设备 A' ]]
[[ $(bmc help tool bmc_bmcshell_tool_smoke_session --usage) == *'示例'* ]]
[[ $(bmc plugin show "$tool_id" --changelog) == *'1.0.0'* ]]

bmc use 192.0.2.10
[[ $(bmc_bmcshell_tool_smoke_session) == 'Current BMC: 192.0.2.10' ]]
[[ $(bmc_bmcshell_tool_smoke_shell) == 'Current BMC: 192.0.2.10' ]]
if bmc use invalid-ip; then
    printf 'Expected the resolver to reject an invalid target.\n' >&2
    exit 1
fi
[[ $(bmc status) == *'target.ip = 192.0.2.10'* ]]

bmc setting --set theme.active "$theme_id"
[[ $(bmc bridge prompt) == '[BMC 192.0.2.10] ' ]]
bmc use 192.0.2.50 --resolver generic
[[ $(bmc status) == *'target.resolver = generic'* ]]
bmc exit
if declare -F bmc_bmcshell_tool_smoke_session >/dev/null; then exit 1; fi
[[ $(bmc status) == 'No active BMC target.' ]]
[[ $(bmc_bmcshell_tool_smoke_global) == 'BMCShell global tool is ready.' ]]

printf 'PASS: scaffolded package types, help, settings, native and Bash tools, target rollback and exit.\n'
