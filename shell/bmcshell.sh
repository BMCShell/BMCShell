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

_bmcshell_release_bridge() {
    [[ -z ${BMCSHELL_EXE-} ]] || return 0
    local source_file=${BASH_SOURCE[0]} root release bridge
    [[ $source_file == */* ]] || source_file=./$source_file
    root=$(builtin cd -P -- "${source_file%/*}/.." && builtin pwd -P) || return
    [[ -f $root/.active-release ]] || return 0
    if ! IFS= read -r release < "$root/.active-release" ||
        [[ ! $release =~ ^release-[0-9]+$ ]]; then
        printf 'BMCShell: invalid active release marker.\n' >&2
        return 2
    fi
    bridge=$root/.releases/$release/shell/bmcshell.sh
    if [[ ! -f $bridge ]]; then
        printf 'BMCShell: active release adapter not found.\n' >&2
        return 2
    fi
    builtin printf '%s\n' "$bridge"
}

_bmcshell_active_bridge=$(_bmcshell_release_bridge) || return
if [[ -n $_bmcshell_active_bridge ]]; then
    builtin source "$_bmcshell_active_bridge"
    return
fi
builtin unset _bmcshell_active_bridge

_bmcshell_executable() {
    local path=${BMCSHELL_EXE-} directory source_file=${BASH_SOURCE[0]}
    if [[ -z $path ]]; then
        [[ $source_file == */* ]] || source_file=./$source_file
        path=${source_file%/*}/../build/BMCShell.exe
        [[ -f $path ]] || path=${source_file%/*}/../BMCShell.exe
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
    if [[ ${_BMCSHELL_PROXY_ADDRESS+x} ]]; then
        builtin export "BMCSHELL_BRIDGE_PROXY=$_BMCSHELL_PROXY_ADDRESS"
    fi
    if [[ -t 0 ]]; then
        builtin export BMCSHELL_BRIDGE_INTERACTIVE=1
    else
        builtin export BMCSHELL_BRIDGE_INTERACTIVE=0
    fi
    if [[ ${1-} == book ]]; then
        builtin unset BMCSHELL_BOOK_TTY BMCSHELL_BOOK_STTY BMCSHELL_BOOK_BASH
        if [[ -t 0 && -t 1 ]] && command -v cygpath >/dev/null; then
            local book_tty book_stty book_bash
            if book_tty=$(command tty 2>/dev/null) && [[ $book_tty == /dev/pty* ]] &&
                book_stty=$(builtin type -P stty) && book_bash=$(command cygpath -aw -- "$BASH"); then
                builtin export "BMCSHELL_BOOK_TTY=$book_tty" "BMCSHELL_BOOK_STTY=$book_stty"
                builtin export "BMCSHELL_BOOK_BASH=$book_bash"
            fi
        fi
    fi
    if [[ ${1-} == doctor ]]; then
        builtin unset BMCSHELL_BRIDGE_BASH
        if command -v cygpath >/dev/null; then
            local native_bash
            if native_bash=$(command cygpath -aw -- "$BASH"); then
                builtin export "BMCSHELL_BRIDGE_BASH=$native_bash"
            fi
        fi
    fi
    builtin export BMCSHELL_CONTEXT
    # Convert only actual file operands, never URLs or resolver arguments.
    builtin export MSYS2_ARG_CONV_EXCL='*'
    builtin exec "$_BMCSHELL_EXE" "$@"
)

_bmcshell_bridge() {
    _bmcshell_call bridge "$@"
}

_bmcshell_tools() {
    local scope=$1 output name reserved
    local -A seen=()
    output=$(_bmcshell_bridge tools "$scope") || return
    reserved=$(builtin compgen -b; builtin compgen -k)
    while IFS= read -r name; do
        name=${name%$'\r'}
        [[ -n $name ]] || continue
        if [[ ! $name =~ ^[a-z][a-z0-9_-]*$ || $name == bmc ||
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
    # V2 same-shell entries query the target through the old bridge spelling.
    local _bmcshell_legacy_tool=1
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
            $name =~ ^[a-z][a-z0-9_-]*$ && -z $rest ]]; then
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
    local response candidate credentials username password
    if [[ $# == 1 && ( $1 == -h || $1 == --help ) ]]; then
        _bmcshell_call help use
        return
    fi
    if [[ $- == *x* || $- == *a* || -o functrace || -n $(builtin trap -p DEBUG) ]]; then
        printf 'BMCShell: disable tracing and automatic export before selecting a target.\n' >&2
        return 2
    fi
    _bmcshell_check_context || return
    response=$(BMCSHELL_BRIDGE_RESOLVE_PRIVATE=1 _bmcshell_bridge resolve "$@") || return
    candidate=${response%%$'\n'*}
    if [[ $candidate != \{*\} ]]; then
        printf 'BMCShell: invalid resolver response.\n' >&2
        return 2
    fi
    if [[ $response == *$'\n'* ]]; then
        credentials=${response#*$'\n'}
        username=${credentials%%$'\n'*}
        password=${credentials#*$'\n'}
        if [[ $credentials != *$'\n'* || -z $username || -z $password || $password == *$'\n'* ]]; then
            printf 'BMCShell: invalid private resolver response.\n' >&2
            return 2
        fi
    fi
    if [[ -z ${BMCSHELL_CONTEXT-} ]]; then
        _bmcshell_activate_session_tools || return
    fi
    _BMCSHELL_SESSION_STATE=()
    if [[ -n ${password-} ]]; then
        _BMCSHELL_SESSION_STATE[v2.user]=$username
        _BMCSHELL_SESSION_STATE[v2.password]=$password
    fi
    BMCSHELL_CONTEXT=$candidate
    builtin export -n BMCSHELL_CONTEXT
    _bmcshell_render_prompt || :
}

_bmcshell_exit() {
    if (( $# )); then
        if (( $# == 1 )) && [[ $1 == -h || $1 == --help ]]; then
            _bmcshell_call help exit
            return
        fi
        printf 'Usage: bmc exit\n' >&2
        return 2
    fi
    if [[ -z ${BMCSHELL_CONTEXT-} ]]; then
        printf 'No active BMC target.\n'
        return 0
    fi
    _bmcshell_check_context || return
    _bmcshell_restore_session_tools || return
    builtin unset BMCSHELL_CONTEXT || return
    _BMCSHELL_SESSION_STATE=()
    _bmcshell_restore_prompt || :
}

_bmcshell_check_proxy_environment() {
    local name declaration
    for name in "${_BMCSHELL_PUBLIC_PROXY_NAMES[@]}" BMCSHELL_PROXY_ADDRESS; do
        declaration=$(builtin declare -p "$name" 2>/dev/null) || continue
        case $declaration in
            'declare -- '*|'declare -x '*) ;;
            *) printf 'BMCShell: cannot manage proxy variable %s with its current Bash attributes.\n' "$name" >&2
               return 2 ;;
        esac
    done
}

_bmcshell_apply_proxy() {
    local address=$1 name declaration
    if [[ -n $address ]]; then
        if [[ -z ${_BMCSHELL_PROXY_ADDRESS-} ]]; then
            for name in "${_BMCSHELL_PUBLIC_PROXY_NAMES[@]}"; do
                if [[ ${!name+x} ]]; then
                    _BMCSHELL_PROXY_SAVED_VALUES[$name]=${!name}
                    declaration=$(builtin declare -p "$name") || return
                    if [[ $declaration == 'declare -x '* ]]; then
                        _BMCSHELL_PROXY_SAVED_EXPORT[$name]=1
                    fi
                fi
            done
        fi
        for name in "${_BMCSHELL_PUBLIC_PROXY_NAMES[@]}"; do
            case $name in
                no_proxy|NO_PROXY) builtin unset "$name" || return ;;
                *) builtin export "$name=socks5h://$address" || return ;;
            esac
        done
        builtin export "BMCSHELL_PROXY_ADDRESS=$address" || return
    elif [[ -n ${_BMCSHELL_PROXY_ADDRESS-} ]]; then
        for name in "${_BMCSHELL_PUBLIC_PROXY_NAMES[@]}"; do
            if [[ ${_BMCSHELL_PROXY_SAVED_VALUES[$name]+set} ]]; then
                builtin printf -v "$name" '%s' "${_BMCSHELL_PROXY_SAVED_VALUES[$name]}" || return
                if [[ ${_BMCSHELL_PROXY_SAVED_EXPORT[$name]+set} ]]; then
                    builtin export "$name" || return
                else
                    builtin export -n "$name" || return
                fi
            else
                builtin unset "$name" || return
            fi
        done
        _BMCSHELL_PROXY_SAVED_VALUES=() _BMCSHELL_PROXY_SAVED_EXPORT=()
    fi
    if [[ -z $address ]]; then
        builtin unset BMCSHELL_PROXY_ADDRESS || return
    fi
    _BMCSHELL_PROXY_ADDRESS=$address
}

_bmcshell_proxy() {
    local state address
    if [[ $# == 1 && ( $1 == -h || $1 == --help ) ]]; then
        _bmcshell_call help proxy
        return
    fi
    if [[ ${1-} == on || ( $# -gt 0 && -n ${_BMCSHELL_PROXY_ADDRESS-} ) ]]; then
        _bmcshell_check_proxy_environment || return
    fi
    state=$(_bmcshell_bridge proxy-config "$@") || return
    state=${state%$'\r'}
    address=${state%%|*}
    if (( $# )); then
        _bmcshell_apply_proxy "$address" || return
    fi
    if [[ -z $address ]]; then
        printf 'BMC 全局代理：关闭（已配置 %s）\n' "${state#*|}"
    else
        printf 'BMC SOCKS5：%s\n' "$address"
    fi
}

_bmcshell_public_command() {
    if command -v cygpath >/dev/null; then
        if [[ $# == 4 && $1 == plugin && $2 == install && $3 == --file ]]; then
            local path
            path=$(command cygpath -aw -- "$4") || return
            _bmcshell_call "$1" "$2" "$3" "$path"
            return
        fi
        if [[ $# == 3 && $1 == update && $2 == --file ]]; then
            local path
            path=$(command cygpath -aw -- "$3") || return
            _bmcshell_call "$1" "$2" "$path"
            return
        fi
    fi
    _bmcshell_call "$@"
}

_bmcshell_complete() {
    local current=${COMP_WORDS[COMP_CWORD]} command=${COMP_WORDS[1]-}
    local previous=${COMP_WORDS[COMP_CWORD-1]} words category= value extra
    local commands='help doctor book library tool plugin use status exit proxy setting version update'
    COMPREPLY=()
    if (( COMP_CWORD == 1 )); then
        words="$commands -h --help"
    else
        words='-h --help'
        case $command in
            help) words=$commands ;;
            doctor) words+=' -d --detial -f --filter -a --auto' ;;
            tool)
                if (( COMP_CWORD == 2 )); then
                    words+=' -l --list -a --all'
                    category=tools
                fi
                ;;
            book)
                if (( COMP_CWORD == 2 )); then
                    words+=' -l --list'
                    category=books
                else
                    words+=' -l --list -s --search -v --version'
                fi
                ;;
            library) words+=' -l --list' ;;
            plugin)
                if (( COMP_CWORD == 2 )); then
                    words+=' list update search install upgrade remove'
                elif [[ $previous == install ]]; then
                    words+=' --file'
                fi
                ;;
            use)
                if [[ $previous == --resolver ]]; then
                    words=
                    category=resolvers
                elif (( COMP_CWORD == 3 )); then
                    words+=' --resolver --'
                fi
                ;;
            status|tool) words+=' --env' ;;
            proxy) words+=' on off -m' ;;
            setting) words+=' --get --set --reset --check --insecure' ;;
            update) words+=' --file --version' ;;
        esac
    fi
    while IFS= read -r value; do
        [[ -n $value ]] && COMPREPLY+=("$value")
    done < <(builtin compgen -W "$words" -- "$current")
    if [[ -n $category ]]; then
        extra=$(_bmcshell_bridge completion "$category" 2>/dev/null) || return
        while IFS= read -r value; do
            value=${value%$'\r'}
            [[ -n $value && $value == "$current"* ]] && COMPREPLY+=("$value")
        done <<< "$extra"
    fi
    return 0
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
        builtin declare -A _BMCSHELL_SESSION_STATE _BMCSHELL_PROXY_SAVED_VALUES _BMCSHELL_PROXY_SAVED_EXPORT || return
    else
        builtin declare -gA _BMCSHELL_SESSION_STATE _BMCSHELL_PROXY_SAVED_VALUES _BMCSHELL_PROXY_SAVED_EXPORT || return
    fi
    _BMCSHELL_SESSION_STATE=()
    builtin export -n _BMCSHELL_SESSION_STATE
    _BMCSHELL_PUBLIC_PROXY_NAMES=(http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY no_proxy NO_PROXY)
    builtin unset _BMCSHELL_PROXY_ADDRESS BMCSHELL_BRIDGE_PROXY
    _BMCSHELL_BASH_RUNTIME=1
fi

_bmcshell_load_proxy() {
    local state address
    state=$(_bmcshell_bridge proxy-config) || return
    address=${state%%|*}
    if [[ -n $address ]]; then
        _bmcshell_check_proxy_environment || return
    fi
    _bmcshell_apply_proxy "$address"
}

if [[ ! ${_BMCSHELL_PROXY_ADDRESS+x} ]]; then
    _bmcshell_load_proxy || return
fi

function bmc {
    case ${1-} in
        use) shift; _bmcshell_use "$@" ;;
        exit) shift; _bmcshell_exit "$@" ;;
        proxy) shift; _bmcshell_proxy "$@" ;;
        status|tool)
            BMCSHELL_BRIDGE_STATUS_USER="${_BMCSHELL_SESSION_STATE[v2.user]-}" \
            BMCSHELL_BRIDGE_STATUS_PASSWORD="${_BMCSHELL_SESSION_STATE[v2.password]+set}" \
            BMCSHELL_BRIDGE_STATUS_SNMP_COMMUNITY="${_BMCSHELL_SESSION_STATE[v2.snmp_community]+set}" \
            BMCSHELL_BRIDGE_STATUS_SNMP_VERSION="${_BMCSHELL_SESSION_STATE[v2.snmp_version]-}" \
            BMCSHELL_BRIDGE_STATUS_TLS="${_BMCSHELL_SESSION_STATE[v2.tls]-}" \
                _bmcshell_call "$@"
            ;;
        bridge)
            if [[ ${_bmcshell_legacy_tool-} == 1 && $# == 3 && $2 == field ]]; then
                shift
                _bmcshell_bridge "$@"
            else
                printf 'BMCShell: unknown command bridge; see bmc help.\n' >&2
                return 2
            fi
            ;;
        *) _bmcshell_public_command "$@" ;;
    esac
}

if [[ $- == *i* ]]; then
    builtin complete -F _bmcshell_complete bmc
fi
_bmcshell_refresh_globals
