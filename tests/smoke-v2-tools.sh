#!/usr/bin/env bash
set -euo pipefail

[[ -n ${BMCSHELL_TEST_ROOT-} ]] || {
    printf 'Set BMCSHELL_TEST_ROOT to a temporary Windows path.\n' >&2
    exit 2
}
scratch=$(cygpath -u "$BMCSHELL_TEST_ROOT")
[[ -d $scratch && -z $(find "$scratch" -mindepth 1 -maxdepth 1 -print -quit) ]] || exit 2
mkdir "$scratch/bin" "$scratch/config" "$scratch/state"
mkdir "$scratch/config/credentials"
BMCSHELL_HOME=$(cygpath -w "$scratch/state")
export BMCSHELL_HOME
export PATH="$scratch/bin:$PATH" BMC_CONFIG_DIR="$scratch/config" BMC_TEST_LOG="$scratch/commands.log"
printf '%s\n' 'test-read' > "$BMC_CONFIG_DIR/credentials/snmp-v2c-read"
printf '%s\n' 'test-write' > "$BMC_CONFIG_DIR/credentials/snmp-v2c-write"

cat > "$scratch/bin/ipmitool" <<'EOF'
#!/usr/bin/env bash
auth=none
if [[ ${IPMI_PASSWORD-} == 'test-only' ]]; then auth=session; fi
if [[ ${IPMITOOL_PASSWORD-} == 'explicit-fixture' ]]; then auth=explicit; fi
case " $* " in *' -P test-only '*) auth=session ;; esac
printf 'ipmi (%s) %s\n' "$auth" "$*" >> "$BMC_TEST_LOG"
case " $* " in
    ' -V ') printf 'mock version\n' ;;
    *' raw 0x3c 0xff 0x04 '*) printf '43 30 35\n' ;;
    *' raw 0x06 0x01 '*) printf '00\tff\r\n 0a 80\n' ;;
    *' raw 0x06 0x02 '*) printf '00 bad\n' ;;
    *' raw 0x06 0x03 '*) printf '00 ff\n'; exit 17 ;;
    *' raw 0x06 0x04 '*) printf '\n' ;;
    *' chassis status '*) printf 'mock chassis\n' ;;
esac
EOF
cat > "$scratch/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >> "$BMC_TEST_LOG"
while (($#)); do
    if [[ $1 == --output ]]; then output=$2; shift; fi
    shift
done
cat >/dev/null
printf '{"mock":true}' > "$output"
printf 200
EOF
cat > "$scratch/bin/ssh" <<'EOF'
#!/usr/bin/env bash
printf 'ssh %s\n' "$*" >> "$BMC_TEST_LOG"
printf 'mock ssh\n'
EOF
cat > "$scratch/bin/snmpget" <<'EOF'
#!/usr/bin/env bash
printf 'snmpget %s\n' "$*" >> "$BMC_TEST_LOG"
printf 'mock SNMP GET\n'
EOF
cp "$scratch/bin/snmpget" "$scratch/bin/snmpset"
chmod +x "$scratch/bin/"*

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
source ./shell/bmcshell.sh
for name in bmcshell-tool-ipmi bmcshell-tool-redfish sbmc-tool-bssh bmcshell-tool-snmp sbmc-tool-cidtool sbmc-tool-smash; do
    bmc plugin install --file "../$name"
done

mkdir "$scratch/resolver"
cat > "$scratch/resolver/bmc-package.json" <<'EOF'
{
  "schema": 1,
  "id": "72fd9147-a136-474a-a993-e317202b26f2",
  "type": "toolplugin",
  "publisher": "test",
  "shortName": "resolver",
  "name": "test-tool-resolver",
  "displayName": "Offline SBMC resolver fixture",
  "description": "Offline SBMC resolver fixture",
  "version": "1.0.0",
  "coreApi": 1,
  "help": "help.json",
  "changelog": "changelog.json",
  "provides": {"resolvers": {"sbmc": {"entry": "resolver.exe"}}}
}
EOF
printf '{}\n' > "$scratch/resolver/help.json"
printf '{}\n' > "$scratch/resolver/changelog.json"
cat > "$scratch/resolver.go" <<'EOF'
package main

import (
	"encoding/json"
	"os"
)

func main() {
	if os.Getenv("BMCSHELL_BRIDGE_RESOLVE_PRIVATE") != "" ||
		os.Getenv("BMCSHELL_AUTH_PASSWORD") != "" || os.Getenv("BMCSHELL_CONTEXT") != "" {
		os.Exit(8)
	}
	mode := os.Args[2]
	if mode == "fail" {
		os.Exit(7)
	}
	result := map[string]string{"target.ip": os.Args[1], "oem.sbmc.cid": "C05", "auth.username": "tester"}
	if mode != "partial" {
		result["auth.password"] = "test-only"
	}
	if mode == "enterprise" {
		result["snmp.enterprises"] = "0"
	}
	data, err := json.Marshal(result)
	if err != nil || os.WriteFile(os.Getenv("BMCSHELL_RESULT_FILE"), data, 0600) != nil {
		os.Exit(1)
	}
}
EOF
go build -o "$scratch/resolver/resolver.exe" "$scratch/resolver.go"
bmc plugin install --file "$scratch/resolver"
for name in sbmc-book-snmp-oid sbmc-book-oem-ipmi sbmc-book-redfish sbmc-library-cidmap; do
    bmc plugin install --file "../$name"
done
[[ $(bmc book oid -v) == "$(bmc book snmp-oid -v)" ]]
[[ $(bmc book oem_ipmi -v) == "$(bmc book oem-ipmi -v)" ]]
[[ $(bmc book redfish --id 3.1.1) == *'/redfish/v1/SessionService/Sessions'* ]]
[[ $(bmc library cidmap) == *'结构版本 2'* ]]

[[ $(bmc tool ipmitool --help) == *'二进制'* ]]
[[ $(bmc tool snmp3walk --help) == *'示例'* ]]
bmc use 192.0.2.10 --resolver generic
[[ $(ipmitool -V) == 'mock version' ]]
[[ $(ipmitool -I open chassis status) == 'mock chassis' ]]
[[ $(ipmitool -I lan -vH192.0.2.99 -U operator -P --bin -p 6623 -b 1 chassis status) == 'mock chassis' ]]
grep -Fqx -- 'ipmi (none) -I open chassis status' "$BMC_TEST_LOG"
grep -Fqx -- 'ipmi (none) -I lan -vH192.0.2.99 -U operator -P --bin -p 6623 -b 1 -- chassis status' "$BMC_TEST_LOG"
bmc use 192.0.2.10 --resolver sbmc -- success
[[ $(bmc status) == '192.0.2.10' ]]
[[ $(bmc status --env) == *'BMCSHELL_AUTH_PASSWORD = [redacted]'* ]]
[[ $(bmc status --env) != *test-only* ]]
[[ $(bmc book oid -s fan) == *1.3.6.1.4.1.54357* ]]
[[ $(BMCSHELL_SNMP_ENTERPRISES=0 bmc book oid --key 1.3.6.1.4.1.0.1.1.1.1.11.6.1) == *'Fan health Status'* ]]
[[ $(BMCSHELL_SNMP_ENTERPRISES=0 bmc book oid -s fanHealthAllStatus) == *'1.3.6.1.4.1.0.1.1.1.1.11.6.1'* ]]
bmc use 192.0.2.10 --resolver sbmc -- enterprise
[[ $(bmc status --env) == *'BMCSHELL_SNMP_ENTERPRISES = 0'* ]]
[[ $(bmc book oid -s fanHealthAllStatus) == *'1.3.6.1.4.1.0.1.1.1.1.11.6.1'* ]]
bmc use 192.0.2.10 --resolver sbmc -- success
[[ $(_bmcshell_bridge resolve 192.0.2.10 --resolver sbmc -- success) != *test-only* ]]
if bmc use 192.0.2.20 --resolver sbmc -- partial; then exit 1; fi
if bmc use 192.0.2.20 --resolver sbmc -- fail; then exit 1; fi
[[ $(bmc status) == '192.0.2.10' ]]
[[ $(ipmitool chassis status 2> "$scratch/ipmi-echo.log") == 'mock chassis' ]]
[[ $(cat "$scratch/ipmi-echo.log") == '==> ipmitool -I lanplus -U tester -P test-only -H 192.0.2.10 chassis status' ]]
[[ $(ipmitool -P 'test password' chassis status 2> "$scratch/ipmi-echo.log") == 'mock chassis' ]]
[[ $(cat "$scratch/ipmi-echo.log") == '==> ipmitool -I lanplus -U tester -H 192.0.2.10 -P test\ password chassis status' ]]
[[ $(IPMITOOL_PASSWORD=explicit-fixture IPMI_PASSWORD=unused-fixture ipmitool -E chassis status 2> "$scratch/ipmi-echo.log") == 'mock chassis' ]]
[[ $(cat "$scratch/ipmi-echo.log") == '==> IPMITOOL_PASSWORD=explicit-fixture ipmitool -I lanplus -U tester -H 192.0.2.10 -E chassis status' ]]
[[ $(ipmitool raw 0x06 0x01 --bin 2> "$scratch/ipmi-echo.log" | od -An -tx1) == *'00 ff 0a 80'* ]]
[[ ! -s $scratch/ipmi-echo.log ]]
ipmitool --bin raw 0x06 0x04 > "$scratch/raw.bin"
[[ ! -s $scratch/raw.bin ]]
rc=0
ipmitool raw 0x06 0x02 --bin > "$scratch/raw.bin" || rc=$?
[[ $rc == 65 && ! -s $scratch/raw.bin ]]
rc=0
ipmitool raw 0x06 0x03 --bin > "$scratch/raw.bin" || rc=$?
[[ $rc == 17 && ! -s $scratch/raw.bin ]]
ipmitool -b 1 chassis status >/dev/null
IPMITOOL_PASSWORD=explicit-fixture ipmitool chassis status >/dev/null
IPMITOOL_PASSWORD=explicit-fixture ipmitool -vE chassis status >/dev/null
ipmitool -H 192.0.2.99 chassis status >/dev/null
ipmitool -U operator chassis status >/dev/null
ipmitool -- chassis status --bin >/dev/null
grep -Fqx -- 'ipmi (session) -I lanplus -U tester -P test-only -H 192.0.2.10 -b 1 -- chassis status' "$BMC_TEST_LOG"
grep -Fqx -- 'ipmi (session) -I lanplus -U tester -P test-only -H 192.0.2.10 -- chassis status' "$BMC_TEST_LOG"
grep -Fqx -- 'ipmi (explicit) -I lanplus -U tester -H 192.0.2.10 -vE -- chassis status' "$BMC_TEST_LOG"
grep -Fqx -- 'ipmi (none) -I lanplus -H 192.0.2.99 -- chassis status' "$BMC_TEST_LOG"
grep -Fqx -- 'ipmi (none) -I lanplus -H 192.0.2.10 -U operator -- chassis status' "$BMC_TEST_LOG"
grep -Fqx -- 'ipmi (session) -I lanplus -U tester -P test-only -H 192.0.2.10 -- chassis status --bin' "$BMC_TEST_LOG"
(
    _bmcshell_bridge() {
        case $1:$2 in
            proxy-address:ipmi) printf '127.0.0.1:1080' ;;
            proxy-run:ipmi-native)
                if [[ $3 == 192.0.2.10 ]]; then
                    [[ $4 == 623 && -z $5 && -n $6 ]] || return 3
                    shift 6
                    [[ $* == '-I lanplus -U tester -P test-only -- raw 0x06 0x01' ]] || return 4
                    printf '00 ff\n'
                    return
                fi
                [[ $3 == 192.0.2.99 && $4 == 6623 && $5 == 4 && -n $6 ]] || return 3
                shift 6
                [[ $* == '-I lan -v -v -4 -U operator -E -- raw 0x06 0x01' ]] || return 4
                [[ ${IPMITOOL_PASSWORD-} == explicit-fixture && -z ${IPMI_PASSWORD-} ]] || return 5
                printf '00 ff\n' ;;
            *) _bmcshell_call bridge "$@" ;;
        esac
    }
    [[ $(IPMITOOL_PASSWORD=explicit-fixture ipmitool -I lan -vH192.0.2.99 -vp6623 -4 -U operator -E raw 0x06 0x01 2> "$scratch/ipmi-echo.log") == '00 ff' ]]
    [[ $(cat "$scratch/ipmi-echo.log") == '==> IPMITOOL_PASSWORD=explicit-fixture ipmitool -I lan -vH192.0.2.99 -vp6623 -4 -U operator -E raw 0x06 0x01' ]]
    [[ $(IPMITOOL_PASSWORD=explicit-fixture ipmitool -I lan -vH192.0.2.99 -vp6623 -4 -U operator -E raw 0x06 0x01 --bin 2> "$scratch/ipmi-echo.log" | od -An -tx1) == *'00 ff'* ]]
    [[ ! -s $scratch/ipmi-echo.log ]]
    [[ $(ipmitool raw 0x06 0x01 2> "$scratch/ipmi-echo.log") == '00 ff' ]]
    [[ $(cat "$scratch/ipmi-echo.log") == '==> ipmitool -I lanplus -U tester -P test-only -H 192.0.2.10 raw 0x06 0x01' ]]
    [[ $(ipmitool raw 0x06 0x01 --bin 2> "$scratch/ipmi-echo.log" | od -An -tx1) == *'00 ff'* ]]
    [[ ! -s $scratch/ipmi-echo.log ]]
)
[[ $(getcid) == C05 ]]
setcid c05 >/dev/null
smash off
[[ $(redfish GET /redfish/v1/Managers) == '{"mock":true}' ]]
[[ $(bssh --root --manual -- uptime) == 'mock ssh' ]]
[[ $(snmp2get .1.3.6.1.2.1.1.1.0) == 'mock SNMP GET' ]]
snmp2set .1.3.6.1.2.1.1.1.0 s fixture >/dev/null
grep -q -- 'raw 0x3a 0x55 0 1' "$BMC_TEST_LOG"
grep -q -- '-c test-read' "$BMC_TEST_LOG"
grep -q -- '-c test-write' "$BMC_TEST_LOG"

bmc proxy on >/dev/null
[[ $(_bmcshell_bridge proxy-address ipmi) == 127.0.0.1:1080 ]]
bmc proxy -m 127.0.0.1:1081 >/dev/null
[[ $(_bmcshell_bridge proxy-address ipmi) == 127.0.0.1:1081 ]]
redfish GET /redfish/v1/Managers >/dev/null
bssh --root --manual -- uptime >/dev/null
grep -q -- 'socks5h://127.0.0.1:1081' "$BMC_TEST_LOG"
grep -q -- 'bridge proxy-run ssh 192.0.2.10 %p' "$BMC_TEST_LOG"
bmc proxy off >/dev/null
[[ -z $(_bmcshell_bridge proxy-address ipmi) ]]

setcid C05 --now >/dev/null
[[ $(bmc status) == 'No active BMC target.' ]]
[[ -z ${_BMCSHELL_SESSION_STATE[v2.password]-} ]]
grep -q -- 'bmc reset cold' "$BMC_TEST_LOG"
if grep -- 'test-only' "$BMC_TEST_LOG" | grep -qv '^ipmi '; then exit 1; fi
printf 'PASS: shipped Tool, Book and CIDmap packages, resolver credentials, proxy mocks and session cleanup.\n'
