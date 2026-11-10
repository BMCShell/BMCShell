# Source this file from Bash to keep BMC session state in the current terminal.

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
    printf 'Load BMCShell with: source %q\n' "${BASH_SOURCE[0]}" >&2
    exit 2
fi
if (( BASH_VERSINFO[0] < 4 )); then
    printf 'BMCShell requires Bash 4 or newer.\n' >&2
    return 2
fi
if [[ ${_BMCSHELL_LOADED-} != 1 ]] &&
    { builtin declare -F bmc >/dev/null || builtin alias bmc >/dev/null 2>&1; }; then
    printf 'BMCShell: bmc is already defined in this terminal. Open a new terminal to load BMCShell.\n' >&2
    return 2
fi

_bmcshell_executable() {
    local path=${BMCSHELL_EXE-} directory source_file=${BASH_SOURCE[0]}
    if [[ -z $path ]]; then
        [[ $source_file == */* ]] || source_file=./$source_file
        path=${source_file%/*}/../BMCShell.exe
    fi
    if [[ $path =~ ^[A-Za-z]: ]] && command -v cygpath >/dev/null; then
        path=$(command cygpath -u "$path") || return
    fi
    [[ $path == */* ]] || path=./$path
    if [[ ! -f $path || ! -x $path ]]; then
        printf 'BMCShell executable not found: %s\n' "$path" >&2
        return 2
    fi
    directory=$(builtin cd -P -- "${path%/*}" && builtin pwd -P) || return
    builtin printf '%s/%s\n' "$directory" "${path##*/}"
}

_bmcshell_call() (
    BMCSHELL_IPMI_PROXY=${_BMCSHELL_IPMI_PROXY-}
    builtin export BMCSHELL_CONTEXT BMCSHELL_IPMI_PROXY
    builtin exec "$_BMCSHELL_EXE" "$@"
)

_bmcshell_bridge() {
    # Keep resolver and tool arguments literal when MSYS2 invokes the native executable.
    MSYS2_ARG_CONV_EXCL='*' _bmcshell_call bridge "$@"
}

_bmcshell_tools() {
    local scope=$1 output name reserved
    local -A seen=()
    output=$(_bmcshell_bridge tools "$scope") || return
    reserved=$(builtin compgen -b; builtin compgen -k)
    while IFS= read -r name; do
        name=${name%$'\r'}
        [[ -n $name ]] || continue
        if [[ ! $name =~ ^[a-z][a-z0-9_]*$ || $name == bmc ||
            $'\n'$reserved$'\n' == *$'\n'"$name"$'\n'* ]]; then
            printf 'BMCShell: invalid or reserved tool name: %q\n' "$name" >&2
            return 2
        fi
        [[ ! ${seen[$name]+set} ]] || continue
        seen[$name]=1
        builtin printf '%s\n' "$name"
    done <<< "$output"
    return 0
}

_bmcshell_define_tool() {
    # The registry validated this identifier; argument values are never evaluated.
    builtin eval -- "function $1 { _bmcshell_dispatch_tool $1 \"\$@\"; }"
}

_bmcshell_run_tool_bridge() (
    local result native_result bash_path=$BASH
    result=$(command mktemp -t bmcshell-tool.XXXXXXXX) || return
    trap 'command rm -f -- "$result"' EXIT
    native_result=$result
    if command -v cygpath >/dev/null; then
        native_result=$(command cygpath -aw -- "$result") || return
        bash_path=$(command cygpath -aw -- "$bash_path") || return
    fi
    # Leave tool stdout connected to the caller while returning the context separately.
    BMCSHELL_BRIDGE_BASH="$bash_path" BMCSHELL_BRIDGE_RESULT_FILE="$native_result" \
        BMCSHELL_BRIDGE_USERNAME="${_BMCSHELL_SESSION_STATE[v2.user]-}" \
        BMCSHELL_BRIDGE_PASSWORD="${_BMCSHELL_SESSION_STATE[v2.password]-}" \
        BMCSHELL_BRIDGE_SNMP_COMMUNITY="${_BMCSHELL_SESSION_STATE[v2.snmp_community]-}" \
        BMCSHELL_BRIDGE_SNMP_VERSION="${_BMCSHELL_SESSION_STATE[v2.snmp_version]-}" \
        BMCSHELL_BRIDGE_TLS="${_BMCSHELL_SESSION_STATE[v2.tls]-}" \
        _bmcshell_bridge run "$@" 1>&3 3>&- || return
    command cat -- "$result"
)

_bmcshell_run_tool() {
    local candidate
    if [[ $- == *x* || $- == *a* || -o functrace || -n $(builtin trap -p DEBUG) ]]; then
        printf 'BMCShell: disable tracing and automatic export before running tools.\n' >&2
        return 2
    fi
    { candidate=$(_bmcshell_run_tool_bridge "$@" 3>&4 4>&-); } 4>&1 || return
    candidate=${candidate%$'\r'}
    [[ -n $candidate ]] || return 0
    if [[ $candidate != \{*\} || $candidate == *$'\n'* ]]; then
        printf 'BMCShell: invalid tool session update.\n' >&2
        return 2
    fi
    _bmcshell_check_context || return
    BMCSHELL_CONTEXT=$candidate
    builtin export -n BMCSHELL_CONTEXT
    _bmcshell_render_prompt || :
}

_bmcshell_dispatch_tool() {
    local _bmcshell_tool_name=$1 _bmcshell_tool_entry
    shift
    _bmcshell_tool_entry=$(_bmcshell_bridge shell-entry "$_bmcshell_tool_name") || return
    _bmcshell_tool_entry=${_bmcshell_tool_entry%$'\r'}
    if [[ -z $_bmcshell_tool_entry ]]; then
        _bmcshell_run_tool "$_bmcshell_tool_name" "$@"
        return
    fi
    if [[ $- == *x* || $- == *a* || ( $- == *T* && -n $(builtin trap -p DEBUG) ) ]]; then
        printf 'BMCShell: disable tracing and automatic export before using Bash tools.\n' >&2
        return 2
    fi
    if command -v cygpath >/dev/null; then
        _bmcshell_tool_entry=$(command cygpath -u -- "$_bmcshell_tool_entry") || return
    fi
    if [[ $_bmcshell_tool_entry != /* || ! -f $_bmcshell_tool_entry ]]; then
        printf 'BMCShell: invalid Bash tool entry.\n' >&2
        return 2
    fi
    # Preserve the caller's conditional status handling when Bash uses set -e.
    command source "$_bmcshell_tool_entry" "$_bmcshell_tool_name" "$@"
}

_bmcshell_refresh_globals() {
    local output name
    output=$(_bmcshell_tools global) || return
    while IFS= read -r name; do
        [[ -n $name ]] || continue
        if builtin type -t "$name" >/dev/null; then
            if [[ " ${_BMCSHELL_GLOBAL_TOOLS[*]} " != *" $name "* ]]; then
                printf 'BMCShell: global tool %s is already defined.\n' "$name" >&2
            fi
            continue
        fi
        _bmcshell_define_tool "$name" || return
        _BMCSHELL_GLOBAL_TOOLS+=("$name")
    done <<< "$output"
}

_bmcshell_check_tool_functions() {
    local declarations word flags name rest
    local -A locked=()
    declarations=$(builtin readonly -pf) || return
    while IFS=' ' read -r word flags name rest; do
        if [[ $word == declare && $flags == -*r* && $flags == -*f* &&
            $name =~ ^[a-z][a-z0-9_]*$ && -z $rest ]]; then
            locked[$name]=1
        fi
    done <<< "$declarations"
    for name in "$@"; do
        if [[ ${locked[$name]+set} ]]; then
            printf 'BMCShell: tool function %s is read-only.\n' "$name" >&2
            return 2
        fi
    done
}

_bmcshell_restore_session_tools() {
    local name index
    _bmcshell_check_tool_functions "${_BMCSHELL_SESSION_TOOLS[@]}" || return
    for index in "${!_BMCSHELL_SESSION_TOOLS[@]}"; do
        name=${_BMCSHELL_SESSION_TOOLS[index]}
        builtin unalias "$name" 2>/dev/null || :
        builtin unset -f "$name" || return
        if [[ -n ${_BMCSHELL_SAVED_FUNCTIONS[index]} ]]; then
            builtin eval -- "${_BMCSHELL_SAVED_FUNCTIONS[index]}" || return
        fi
        if [[ -n ${_BMCSHELL_SAVED_ALIASES[index]} ]]; then
            builtin eval -- "${_BMCSHELL_SAVED_ALIASES[index]}" || return
        fi
    done
    _BMCSHELL_SESSION_TOOLS=() _BMCSHELL_SAVED_FUNCTIONS=() _BMCSHELL_SAVED_ALIASES=()
}

_bmcshell_activate_session_tools() {
    local output name saved_function saved_alias
    local -a names=()
    output=$(_bmcshell_tools session) || return
    while IFS= read -r name; do
        [[ -n $name ]] && names+=("$name")
    done <<< "$output"
    _bmcshell_check_tool_functions "${names[@]}" || return
    for name in "${names[@]}"; do
        saved_function=$(builtin declare -f "$name") || saved_function=''
        saved_alias=$(builtin alias "$name" 2>/dev/null) || saved_alias=''
        _BMCSHELL_SESSION_TOOLS+=("$name")
        _BMCSHELL_SAVED_FUNCTIONS+=("$saved_function")
        _BMCSHELL_SAVED_ALIASES+=("$saved_alias")
        builtin unalias "$name" 2>/dev/null || :
        if ! _bmcshell_define_tool "$name"; then
            _bmcshell_restore_session_tools
            return 2
        fi
    done
}

_bmcshell_restore_prompt() {
    if [[ -n ${_BMCSHELL_RENDERED_PS1-} && ${PS1-} == "$_BMCSHELL_RENDERED_PS1" ]]; then
        PS1=$_BMCSHELL_BASE_PS1 || return
    fi
    builtin unset _BMCSHELL_BASE_PS1 _BMCSHELL_RENDERED_PS1
}

_bmcshell_render_prompt() {
    local prefix
    [[ $- == *i* ]] || return 0
    _bmcshell_restore_prompt || return
    prefix=$(_bmcshell_bridge prompt 2>/dev/null) || return
    # PS1 is expanded by Bash. Treat the prefix as display text only.
    if [[ -n $prefix && $prefix != *'$'* && $prefix != *'`'* &&
        $prefix != *'\'* && ! $prefix =~ [[:cntrl:]] ]]; then
        _BMCSHELL_BASE_PS1=${PS1-}
        _BMCSHELL_RENDERED_PS1="$prefix $_BMCSHELL_BASE_PS1"
        PS1=$_BMCSHELL_RENDERED_PS1
    fi
}

_bmcshell_check_context() {
    local declaration
    declaration=$(builtin declare -p BMCSHELL_CONTEXT 2>/dev/null) || declaration=''
    if [[ $declaration =~ ^declare\ -[^[:space:]]*r ]]; then
        printf 'BMCShell: BMCSHELL_CONTEXT is read-only.\n' >&2
        return 2
    fi
}

_bmcshell_use() {
    local candidate
    if [[ ${1-} == -h || ${1-} == --help ]]; then
        _bmcshell_call help use --usage
        return
    fi
    _bmcshell_check_context || return
    candidate=$(_bmcshell_bridge resolve "$@") || return
    if [[ $candidate != \{*\} || $candidate == *$'\n'* ]]; then
        printf 'BMCShell: invalid resolver response.\n' >&2
        return 2
    fi
    if [[ -z ${BMCSHELL_CONTEXT-} ]]; then
        _bmcshell_activate_session_tools || return
    fi
    _BMCSHELL_SESSION_STATE=()
    BMCSHELL_CONTEXT=$candidate
    builtin export -n BMCSHELL_CONTEXT
    _bmcshell_render_prompt || :
}

_bmcshell_exit() {
    if (( $# )); then
        if (( $# == 1 )) && [[ $1 == -h || $1 == --help ]]; then
            _bmcshell_call help exit --usage
            return
        fi
        printf 'Usage: bmc exit\n' >&2
        return 2
    fi
    _bmcshell_check_context || return
    _bmcshell_restore_session_tools || return
    builtin unset BMCSHELL_CONTEXT || return
    _BMCSHELL_SESSION_STATE=()
    _bmcshell_restore_prompt || :
}

_bmcshell_ipmi_proxy() {
    local port
    if (( $# == 1 )); then
        case $1 in
            -h|--help) _bmcshell_call help ipmi-proxy --usage; return ;;
            on) _BMCSHELL_IPMI_PROXY=127.0.0.1:1080 ;;
            off) _BMCSHELL_IPMI_PROXY='' ;;
            *)
                if [[ $1 =~ ^127\.0\.0\.1:([0-9]{1,5})$ ]]; then
                    port=$((10#${BASH_REMATCH[1]}))
                fi
                if [[ -z ${port-} ]] || (( port < 1 || port > 65535 )); then
                    printf '用法：bmc ipmi-proxy [on|off|127.0.0.1:端口]\n' >&2
                    return 2
                fi
                _BMCSHELL_IPMI_PROXY=127.0.0.1:$port
                ;;
        esac
    elif (( $# > 1 )); then
        printf '用法：bmc ipmi-proxy [on|off|127.0.0.1:端口]\n' >&2
        return 2
    fi
    builtin export -n _BMCSHELL_IPMI_PROXY
    if [[ -n ${_BMCSHELL_IPMI_PROXY-} ]]; then
        printf 'IPMI SOCKS5：%s\n' "$_BMCSHELL_IPMI_PROXY"
    else
        printf 'IPMI：直连\n'
    fi
}

_BMCSHELL_EXE=$(_bmcshell_executable) || return
if [[ ${_BMCSHELL_LOADED-} != 1 ]]; then
    _bmcshell_check_context || return
    builtin unset BMCSHELL_CONTEXT || return
    _BMCSHELL_GLOBAL_TOOLS=() _BMCSHELL_SESSION_TOOLS=()
    _BMCSHELL_SAVED_FUNCTIONS=() _BMCSHELL_SAVED_ALIASES=()
    _BMCSHELL_LOADED=1
fi
if [[ ${_BMCSHELL_BASH_RUNTIME-} != 1 ]]; then
    if (( BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] < 2 )); then
        builtin declare -A _BMCSHELL_SESSION_STATE || return
    else
        builtin declare -gA _BMCSHELL_SESSION_STATE || return
    fi
    _BMCSHELL_SESSION_STATE=()
    builtin export -n _BMCSHELL_SESSION_STATE
    _BMCSHELL_IPMI_PROXY=''
    builtin export -n _BMCSHELL_IPMI_PROXY
    _BMCSHELL_BASH_RUNTIME=1
fi

function bmc {
    case ${1-} in
        use) shift; _bmcshell_use "$@" ;;
        exit) shift; _bmcshell_exit "$@" ;;
        ipmi-proxy) shift; _bmcshell_ipmi_proxy "$@" ;;
        proxy)
            _bmcshell_call "$@" || return
            if (( $# == 2 )) && [[ $2 == off ]]; then
                _BMCSHELL_IPMI_PROXY=''
                builtin export -n _BMCSHELL_IPMI_PROXY
            fi
            ;;
        *) _bmcshell_call "$@" ;;
    esac
}

_bmcshell_refresh_globals
