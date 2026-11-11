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
fixture_id() {
    sed -n 's/^[[:space:]]*"id": "\([^"]*\)",*$/\1/p' "$fixture/bmcshell-$1-smoke/bmc-package.json"
}
tool_id=$(fixture_id tool)
[[ -n $tool_id ]]
"$scaffold" create --name bmcshell-bundle-smoke -o "$native_fixture" --member "$tool_id"
bmc plugin install --file "$native_fixture/bmcshell-bundle-smoke"
source ./shell/bmcshell.sh

book_id=$(fixture_id book)
library_id=$(fixture_id library)
theme_id=$(fixture_id theme)
bundle_id=$(fixture_id bundle)
[[ -f "$home/framework.db" && ! -d "$home/packages/.sets" ]]
[[ -f "$home/books/$book_id/1.0.0/book/smoke.book.db" ]]
[[ -f "$home/libraries/$library_id/releases/1.0.0/bmc-package.json" ]]
for library_file in work.library.db baseline.library.db audit.jsonl; do
    [[ -n $(find "$home/libraries/$library_id" -maxdepth 2 -path "*/gen-*/$library_file" -print -quit) ]]
done
[[ -f "$home/plugins/tool/$tool_id/1.0.0/bin/tool.exe" ]]
[[ -f "$home/plugins/theme/$theme_id/1.0.0/bmc-package.json" ]]
[[ -f "$home/plugins/bundle/$bundle_id/1.0.0/bmc-package.json" ]]

[[ $(bmc_bmcshell_tool_smoke_global) == 'BMCShell global tool is ready.' ]]
if declare -F bmc_bmcshell_tool_smoke_session >/dev/null; then exit 1; fi
[[ $(bmc book smoke -s 示例) == *'示例条目'* ]]
[[ $(bmc book smoke -v) == '1.0.0' ]]
[[ $(bmc library smoke) == *'结构版本'* ]]
[[ $(bmc tool bmc_bmcshell_tool_smoke_session --help) == *'示例'* ]]
[[ $(bmc plugin list tool) == *'bmcshell-tool-smoke'* ]]

bmc use 192.0.2.10
[[ $(bmc_bmcshell_tool_smoke_session) == 'Current BMC: 192.0.2.10' ]]
[[ $(bmc_bmcshell_tool_smoke_shell) == 'Current BMC: 192.0.2.10' ]]
if bmc use invalid-ip; then
    printf 'Expected the resolver to reject an invalid target.\n' >&2
    exit 1
fi
[[ $(bmc status) == '192.0.2.10' ]]

bmc setting --set theme.active bmcshell-theme-smoke
[[ $(_bmcshell_bridge prompt) == '[BMC 192.0.2.10] ' ]]
bmc use 192.0.2.50 --resolver generic
[[ $(bmc status) == '192.0.2.50' ]]
bmc exit
if declare -F bmc_bmcshell_tool_smoke_session >/dev/null; then exit 1; fi
[[ $(bmc status) == 'No active BMC target.' ]]
[[ $(bmc_bmcshell_tool_smoke_global) == 'BMCShell global tool is ready.' ]]

printf 'PASS: scaffolded package types, help, settings, native and Bash tools, target rollback and exit.\n'
