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
    rm -f -- "$fixture"/{defaults.json,stdout} "$source"/{bmc-package.json,help.json,changelog.json,env.csv,read.sh,write.sh,credential.sh}
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

if command -v cygpath >/dev/null; then
    package=$(cygpath -aw -- "$package")
fi
bmc plugin install --file "$package"
bmc use 192.0.2.10
if smoke_credential; then exit 1; fi
_BMCSHELL_SESSION_STATE[v2.password]=test-only
smoke_credential
[[ $(BMCSHELL_TARGET_IP=192.0.2.99 smoke_read 'argument with spaces') == '192.0.2.10|package default|argument with spaces' ]]
[[ $(printf '%s\n' 'standard input' | smoke_read --stdin) == '192.0.2.10|package default|standard input' ]]

defaults="$fixture/defaults.json"
printf '%s\n' '{"SMOKE_TARGET_MODEL":"installation default"}' > "$defaults"
if command -v cygpath >/dev/null; then
    defaults=$(cygpath -aw -- "$defaults")
fi
bmc setting --import-env "$defaults"
[[ $(smoke_read read) == '192.0.2.10|installation default|read' ]]
if smoke_write reject > "$fixture/stdout"; then exit 1; else [[ $? == 7 ]]; fi
[[ $(cat "$fixture/stdout") == 'tool stdout' ]]
[[ $(smoke_read read) == '192.0.2.10|installation default|read' ]]
smoke_write > "$fixture/stdout"
[[ $(cat "$fixture/stdout") == 'tool stdout' ]]
[[ $(smoke_read read) == '192.0.2.10|runtime "quoted" model|read' ]]
bmc use 192.0.2.20
[[ $(smoke_read read) == '192.0.2.20|installation default|read' ]]
if smoke_credential; then exit 1; fi
bmc exit

printf 'PASS: managed Bash environment, defaults, session write and rollback.\n'
