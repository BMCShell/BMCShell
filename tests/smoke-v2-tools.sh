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
[[ ${IPMI_PASSWORD-} == 'test-only' ]] || exit 5
printf 'ipmi %s\n' "$*" >> "$BMC_TEST_LOG"
case " $* " in
    *' raw 0x3c 0xff 0x04 '*) printf '43 30 35\n' ;;
    *' raw 0x06 0x01 '*) printf '00 ff\n' ;;
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
shopt -s nullglob
for kind in IPMI Redfish SSH SNMP CID SMASH; do
    archives=("../BMCShell-$kind"/dist/*.zip)
    [[ ${#archives[@]} == 1 ]] || { printf 'Expected one ZIP for %s.\n' "$kind" >&2; exit 2; }
    bmc plugin install --file "${archives[0]}"
done

[[ $(bmc help tool ipmitool --usage) == *'二进制'* ]]
[[ $(bmc tool snmp3walk) == *'示例'* ]]
bmc use 192.0.2.10
# Inject only fixture values. Interactive secret input is covered separately.
_BMCSHELL_SESSION_STATE[v2.user]=tester
_BMCSHELL_SESSION_STATE[v2.password]=test-only
[[ $(ipmitool chassis status) == 'mock chassis' ]]
[[ $(ipmitool raw 0x06 0x01 --bin | od -An -tx1) == *'00 ff'* ]]
ipmitool -b 1 chassis status >/dev/null
if ipmitool -H 192.0.2.99 chassis status; then exit 1; fi
if ipmitool -vH192.0.2.99 chassis status; then exit 1; fi
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

bmc ipmi-proxy on >/dev/null
[[ $(bmc bridge proxy-address ipmi) == 127.0.0.1:1080 ]]
bmc proxy 127.0.0.1:1081 >/dev/null
[[ $(bmc bridge proxy-address ipmi) == 127.0.0.1:1081 ]]
redfish GET /redfish/v1/Managers >/dev/null
bssh --root --manual -- uptime >/dev/null
grep -q -- 'socks5h://127.0.0.1:1081' "$BMC_TEST_LOG"
grep -q -- 'bridge proxy-run ssh 192.0.2.10 %p' "$BMC_TEST_LOG"
bmc proxy off >/dev/null
[[ -z $(bmc bridge proxy-address ipmi) ]]

setcid C05 --now >/dev/null
[[ $(bmc status) == 'No active BMC target.' ]]
[[ -z ${_BMCSHELL_SESSION_STATE[v2.password]-} ]]
grep -q -- 'bmc reset cold' "$BMC_TEST_LOG"
if grep -q -- 'test-only' "$BMC_TEST_LOG"; then exit 1; fi
printf 'PASS: six V2 plugin packages, detailed help, local transport mocks, proxy settings and session cleanup.\n'
