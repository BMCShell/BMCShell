#!/usr/bin/env bash
set -euo pipefail

: "${BMCSHELL_HOME:?Set BMCSHELL_HOME to a new empty test directory.}"
home=$BMCSHELL_HOME
if command -v cygpath >/dev/null; then
    home=$(cygpath -u -- "$home")
fi
[[ -d $home && -z $(find "$home" -mindepth 1 -maxdepth 1 -print -quit) ]] || exit 2

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
source ./shell/bmcshell.sh
fixture=$(mktemp -d)
package="$fixture/plugin with spaces"
mkdir -- "$package"
cleanup() {
    local source="$fixture/plugin with spaces"
    rm -f -- "$fixture/stdout" "$fixture/stderr" "$source"/{bmc-package.json,help.json,changelog.json,env.csv,read.sh,write.sh,credential.sh}
    rmdir -- "$source" "$fixture"
}
trap cleanup EXIT
cat > "$package/bmc-package.json" <<'EOF'
{
  "schema": 1,
  "id": "2ab8646f-bc47-44be-a315-f295c333c289",
  "type": "toolplugin",
  "publisher": "smoke",
  "name": "smoke-tool-env",
  "summary": "Managed environment integration fixture",
  "version": "1.0.0",
  "coreApi": 1,
  "help": "help.json",
  "changelog": "changelog.json",
  "envRegistry": "env.csv",
  "provides": {
    "tools": {
      "smoke_read": {
        "scope": "session",
        "runtime": "bash",
        "entry": "read.sh",
        "envReads": [
          {"name": "BMCSHELL_TARGET_IP", "required": true},
          {"name": "BMCSHELL_TARGET_MODEL", "required": false},
          {"name": "SMOKE_TARGET_MODEL", "required": true}
        ]
      },
      "smoke_write": {
        "scope": "session",
        "runtime": "bash",
        "entry": "write.sh",
        "envWrites": [{"name": "SMOKE_TARGET_MODEL"}]
      },
      "smoke_credential": {
        "scope": "session",
        "runtime": "bash",
        "entry": "credential.sh",
        "envReads": [{"name": "BMCSHELL_AUTH_PASSWORD", "required": true}]
      }
    }
  }
}
EOF
printf '{}\n' > "$package/help.json"
printf '{}\n' > "$package/changelog.json"
printf '%s\n' 'name,key,sensitive,default' 'SMOKE_TARGET_MODEL,oem.smoke.model,false,package default' > "$package/env.csv"
cat > "$package/read.sh" <<'EOF'
set -euo pipefail
[[ ! ${BMCSHELL_TARGET_MODEL+x} && ! ${BMCSHELL_BRIDGE_PASSWORD+x} && ! ${BMCSHELL_AUTH_PASSWORD+x} ]]
argument=$1
if [[ $argument == --stdin ]]; then IFS= read -r argument; fi
printf '%s|%s|%s\n' "$BMCSHELL_TARGET_IP" "$SMOKE_TARGET_MODEL" "$argument"
EOF
cat > "$package/write.sh" <<'EOF'
set -euo pipefail
printf '%s\n' '{"SMOKE_TARGET_MODEL":"runtime \"quoted\" model"}' > "$BMCSHELL_RESULT_FILE"
printf '%s\n' 'tool stdout'
[[ ${1-} != reject ]] || exit 7
EOF
cat > "$package/credential.sh" <<'EOF'
set -euo pipefail
[[ ${BMCSHELL_AUTH_PASSWORD-} == 'test-only' && ! ${BMCSHELL_BRIDGE_PASSWORD+x} ]]
EOF

bmc plugin install --file "$package"
COMP_WORDS=(bmc tool smoke_) COMP_CWORD=2
_bmcshell_complete
[[ " ${COMPREPLY[*]} " == *' smoke_read '* ]]
bmc use 192.0.2.10 > "$fixture/stdout" 2> "$fixture/stderr"
[[ ! -s $fixture/stdout && $(< "$fixture/stderr") == '已选择 BMC 目标 192.0.2.10。' ]]
if bmc use invalid-ip 2> "$fixture/stderr"; then exit 1; fi
[[ $(< "$fixture/stderr") != *'已选择 BMC 目标'* ]]
[[ $(bmc status) == '192.0.2.10' ]]
if smoke_credential; then exit 1; fi
_BMCSHELL_SESSION_STATE[v2.password]=test-only
smoke_credential
[[ $(BMCSHELL_TARGET_IP=192.0.2.99 smoke_read 'argument with spaces') == '192.0.2.10|package default|argument with spaces' ]]
[[ $(printf '%s\n' 'standard input' | smoke_read --stdin) == '192.0.2.10|package default|standard input' ]]

# Previously saved installation defaults remain readable.
printf '%s\n' '{"env.defaults":{"SMOKE_TARGET_MODEL":"installation default"}}' > "$home/settings.json"
status=$(bmc status --env)
[[ $status == *'BMCSHELL_AUTH_PASSWORD = [已隐藏]'* && $status != *test-only* ]]
[[ $(bmc status) == '192.0.2.10' ]]
[[ $(smoke_read read) == '192.0.2.10|installation default|read' ]]
if smoke_write reject > "$fixture/stdout" 2> "$fixture/stderr"; then exit 1; else [[ $? == 7 ]]; fi
[[ $(< "$fixture/stderr") == *'子进程退出码 7'* ]]
[[ $(cat "$fixture/stdout") == 'tool stdout' ]]
[[ $(smoke_read read) == '192.0.2.10|installation default|read' ]]
smoke_write > "$fixture/stdout"
[[ $(cat "$fixture/stdout") == 'tool stdout' ]]
[[ $(smoke_read read) == '192.0.2.10|runtime "quoted" model|read' ]]
bmc use 192.0.2.20
[[ $(smoke_read read) == '192.0.2.20|installation default|read' ]]
if smoke_credential; then exit 1; fi

unset http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY no_proxy NO_PROXY
http_proxy='http://previous.example:1234'
export https_proxy=''
export NO_PROXY='localhost,192.0.2.1'
bmc proxy -m 127.0.0.1:1081 >/dev/null
[[ ! ${BMCSHELL_PROXY_ADDRESS+x} && $http_proxy == http://previous.example:1234 ]]
bmc proxy on >/dev/null
[[ $BMCSHELL_PROXY_ADDRESS == 127.0.0.1:1081 && $http_proxy == socks5h://127.0.0.1:1081 ]]
[[ $ALL_PROXY == "$http_proxy" && ! ${no_proxy+x} && ! ${NO_PROXY+x} ]]
bmc proxy -m 127.0.0.1:1082 >/dev/null
[[ $BMCSHELL_PROXY_ADDRESS == 127.0.0.1:1082 && $https_proxy == socks5h://127.0.0.1:1082 ]]
bmc exit
[[ $BMCSHELL_PROXY_ADDRESS == 127.0.0.1:1082 ]]
status=$(BMCSHELL_SNMP_ENTERPRISES=0 bmc status --env)
[[ $status == *'BMCSHELL_PROXY_ADDRESS = 127.0.0.1:1082'* && $status == *'BMCSHELL_SNMP_ENTERPRISES = 0'* ]]
[[ $(bmc status) == '尚未选中 BMC 目标。请先运行 bmc use <IP>。' ]]
bmc proxy off >/dev/null
[[ ! ${BMCSHELL_PROXY_ADDRESS+x} && ! ${ALL_PROXY+x} && $NO_PROXY == localhost,192.0.2.1 ]]
[[ $(declare -p http_proxy) == 'declare -- http_proxy="http://previous.example:1234"' ]]
[[ $(declare -p https_proxy) == 'declare -x https_proxy=""' ]]
bmc proxy on >/dev/null
[[ $BMCSHELL_PROXY_ADDRESS == 127.0.0.1:1082 ]]
bmc proxy off >/dev/null

printf 'PASS: managed Bash environment, session writes, secret redaction and proxy restoration.\n'
