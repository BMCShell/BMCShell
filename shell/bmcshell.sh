# Source this file from Bash to keep BMC session state in the current terminal.

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
    printf '请在当前 Bash 中加载 BMCShell：source %q\n' "${BASH_SOURCE[0]}" >&2
    exit 2
fi
if (( BASH_VERSINFO[0] < 4 )); then
    printf 'BMCShell 需要 Bash 4 或更新版本。\n' >&2
    return 2
fi
if [[ ${_BMCSHELL_LOADED-} != 1 ]] &&
    { builtin declare -F bmc >/dev/null || builtin alias bmc >/dev/null 2>&1; }; then
    printf '当前终端已定义 bmc。请打开新终端再加载 BMCShell。\n' >&2
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
        printf 'BMCShell 活动版本标记无效。\n' >&2
        return 2
    fi
    bridge=$root/.releases/$release/shell/bmcshell.sh
    if [[ ! -f $bridge ]]; then
        printf '找不到 BMCShell 活动版本的 Bash 适配脚本。\n' >&2
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
        printf '找不到 BMCShell 程序文件：%s\n' "$path" >&2
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
    builtin unset BMCSHELL_BRIDGE_PLUGIN_COLUMNS
    if [[ ${1-} == plugin && ( ${2-} == list || ${2-} == search ) && -t 1 && ${TERM-} != dumb ]]; then
        local size width
        size=$(command stty size 2>/dev/null </dev/tty) || size=
        width=${size##* }
        [[ $width =~ ^[1-9][0-9]*$ ]] || width=${COLUMNS:-80}
        builtin export "BMCSHELL_BRIDGE_PLUGIN_COLUMNS=$width"
    fi
    if [[ ${1-} == book || ${1-} == setting ]]; then
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
        builtin unset BMCSHELL_BRIDGE_BASH BMCSHELL_BRIDGE_BASHRC
        local bashrc=${HOME:+$HOME/.bashrc}
        if command -v cygpath >/dev/null; then
            local native_bash
            if native_bash=$(command cygpath -aw -- "$BASH"); then
                builtin export "BMCSHELL_BRIDGE_BASH=$native_bash"
            fi
            if [[ -n $bashrc ]]; then
                bashrc=$(command cygpath -aw -- "$bashrc") || bashrc=
            fi
        fi
        [[ -z $bashrc ]] || builtin export "BMCSHELL_BRIDGE_BASHRC=$bashrc"
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
            printf 'BMCShell 工具名无效或已保留：%q\n' "$name" >&2
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
        printf '运行 BMCShell 工具前，请关闭 Bash 跟踪和自动导出。\n' >&2
        return 2
    fi
    { candidate=$(_bmcshell_run_tool_bridge "$@" 3>&4 4>&-); } 4>&1 || return
    candidate=${candidate%$'\r'}
    [[ -n $candidate ]] || return 0
    if [[ $candidate != \{*\} || $candidate == *$'\n'* ]]; then
        printf 'BMCShell 工具返回的会话更新无效。\n' >&2
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
        printf '运行 BMCShell 的 Bash 工具前，请关闭 Bash 跟踪和自动导出。\n' >&2
        return 2
    fi
    if command -v cygpath >/dev/null; then
        _bmcshell_tool_entry=$(command cygpath -u -- "$_bmcshell_tool_entry") || return
    fi
    if [[ $_bmcshell_tool_entry != /* || ! -f $_bmcshell_tool_entry ]]; then
        printf 'BMCShell 工具的 Bash 入口无效。\n' >&2
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
                printf 'BMCShell 全局工具 %s 已定义。\n' "$name" >&2
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
            printf 'BMCShell 工具函数 %s 为只读。\n' "$name" >&2
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

_bmcshell_capture_prompt_status() {
    _BMCSHELL_PROMPT_STATUS=$?
    return "$_BMCSHELL_PROMPT_STATUS"
}

_bmcshell_prompt_command() {
    local status=$? verified=false proxy=off
    if [[ ${1-} == override ]]; then
        status=${_BMCSHELL_PROMPT_STATUS:-$status}
        builtin unset _BMCSHELL_PROMPT_STATUS
        if [[ ${PS1-} != "${_BMCSHELL_RENDERED_PS1-}" ]]; then
            _BMCSHELL_BASE_PS1=${PS1-}
        fi
    fi
    if [[ $_BMCSHELL_THEME_VERIFIED == true && ${_BMCSHELL_SESSION_STATE[v2.password]+set} == set ]]; then
        verified=true
    fi
    [[ -z ${_BMCSHELL_PROXY_ADDRESS-} ]] || proxy=on
    if _bmcshell_theme_render "$status" "$_BMCSHELL_THEME_IP" "$verified" "$proxy" "${_BMCSHELL_THEME_DRAWN-}" \
        ${_BMCSHELL_THEME_SETTINGS[@]+"${_BMCSHELL_THEME_SETTINGS[@]}"}; then
        _BMCSHELL_RENDERED_PS1=$PS1
        _BMCSHELL_THEME_DRAWN=1
    fi
    return "$status"
}

_bmcshell_prompt_array_supported() {
    (( BASH_VERSINFO[0] > 5 || ( BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] >= 1 ) )) &&
        [[ $(builtin declare -p PROMPT_COMMAND 2>/dev/null) =~ ^declare\ -[^[:space:]]*a ]]
}

_bmcshell_remove_prompt_hooks() {
    if _bmcshell_prompt_array_supported; then
        local index
        for index in "${!PROMPT_COMMAND[@]}"; do
            case ${PROMPT_COMMAND[index]} in
                _bmcshell_capture_prompt_status|_bmcshell_prompt_command|'_bmcshell_prompt_command override')
                    builtin unset "PROMPT_COMMAND[$index]"
                    ;;
            esac
        done
    else
        local hooks=${PROMPT_COMMAND-}
        hooks=${hooks#$'_bmcshell_capture_prompt_status\n'}
        hooks=${hooks%$'\n_bmcshell_prompt_command override'}
        hooks=${hooks#'_bmcshell_prompt_command; '}
        if [[ -z $hooks || $hooks == _bmcshell_prompt_command ]]; then
            builtin unset 'PROMPT_COMMAND[0]'
        else
            PROMPT_COMMAND=$hooks
        fi
    fi
}

_bmcshell_configure_prompt_hooks() {
    _bmcshell_remove_prompt_hooks
    if _bmcshell_prompt_array_supported; then
        if [[ $1 == override ]]; then
            PROMPT_COMMAND=(_bmcshell_capture_prompt_status ${PROMPT_COMMAND[@]+"${PROMPT_COMMAND[@]}"} '_bmcshell_prompt_command override')
        else
            PROMPT_COMMAND=(_bmcshell_prompt_command ${PROMPT_COMMAND[@]+"${PROMPT_COMMAND[@]}"})
        fi
    elif [[ $1 == override && -n ${PROMPT_COMMAND-} ]]; then
        PROMPT_COMMAND=$'_bmcshell_capture_prompt_status\n'"${PROMPT_COMMAND}"$'\n_bmcshell_prompt_command override'
    elif [[ -n ${PROMPT_COMMAND-} ]]; then
        PROMPT_COMMAND="_bmcshell_prompt_command; $PROMPT_COMMAND"
    else
        PROMPT_COMMAND=_bmcshell_prompt_command
    fi
}

_bmcshell_unload_theme() {
    [[ -n ${_BMCSHELL_THEME_ENTRY-} ]] || return 0
    _bmcshell_remove_prompt_hooks
    builtin unset -f _bmcshell_theme_render
    builtin unset _BMCSHELL_THEME_ENTRY _BMCSHELL_THEME_DRAWN _BMCSHELL_THEME_IP _BMCSHELL_THEME_VERIFIED
    builtin unset _BMCSHELL_THEME_SETTINGS
    _bmcshell_restore_prompt
}

_bmcshell_load_theme() {
    local entry=$1
    [[ ${_BMCSHELL_THEME_ENTRY-} != "$entry" ]] || return 0
    _bmcshell_unload_theme || return
    _bmcshell_restore_prompt || return
    if command -v cygpath >/dev/null; then
        entry=$(command cygpath -u -- "$entry") || return
    fi
    _BMCSHELL_BASE_PS1=${PS1-}
    if ! builtin source "$entry" || ! builtin declare -F _bmcshell_theme_render >/dev/null; then
        builtin unset -f _bmcshell_theme_render
        PS1=$_BMCSHELL_BASE_PS1
        builtin unset _BMCSHELL_BASE_PS1
        printf '无法加载活动主题脚本：%s\n' "$entry" >&2
        return 2
    fi
    _BMCSHELL_THEME_ENTRY=$1
}

_bmcshell_render_prompt() {
    local prefix
    [[ $- == *i* ]] || return 0
    if [[ ${1-} == strict ]]; then
        prefix=$(BMCSHELL_BRIDGE_THEME_SETTINGS=2 _bmcshell_bridge prompt) || return
    else
        prefix=$(BMCSHELL_BRIDGE_THEME_SETTINGS=2 _bmcshell_bridge prompt 2>/dev/null) || prefix=
    fi
    if [[ $prefix == $'entry:\t'* ]]; then
        local state=${prefix#*$'\t'}
        local -a entry
        # The first line is the script path. Remaining lines are literal key=value arguments.
        builtin mapfile -t entry <<< "${state#*$'\t'}"
        state=${state%%$'\t'*}
        _bmcshell_load_theme "${entry[0]}" || return
        _BMCSHELL_THEME_SETTINGS=("${entry[@]:1}")
        _BMCSHELL_THEME_IP=${state%%|*}
        state=${state#*|}
        _BMCSHELL_THEME_VERIFIED=${state%%|*}
        _bmcshell_configure_prompt_hooks "${state#*|}"
        return
    fi
    _bmcshell_unload_theme || return
    _bmcshell_restore_prompt || return
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
        printf 'BMCSHELL_CONTEXT 为只读，无法更新 BMC 会话。\n' >&2
        return 2
    fi
}

_bmcshell_use() {
    local response candidate credentials username password bash_path=''
    if [[ $# == 1 && ( $1 == -h || $1 == --help ) ]]; then
        _bmcshell_call help use
        return
    fi
    if [[ $- == *x* || $- == *a* || -o functrace || -n $(builtin trap -p DEBUG) ]]; then
        printf '选择 BMC 目标前，请关闭 Bash 跟踪和自动导出。\n' >&2
        return 2
    fi
    _bmcshell_check_context || return
    if [[ -t 0 ]]; then
        bash_path=$BASH
        if command -v cygpath >/dev/null; then
            bash_path=$(command cygpath -aw -- "$bash_path") || bash_path=''
        fi
    fi
    response=$(BMCSHELL_BRIDGE_BASH="$bash_path" BMCSHELL_BRIDGE_RESOLVE_PRIVATE=1 _bmcshell_bridge resolve "$@") || return
    candidate=${response%%$'\n'*}
    if [[ $candidate != \{*\} ]]; then
        printf 'BMC 解析器返回的会话信息无效。\n' >&2
        return 2
    fi
    if [[ $response == *$'\n'* ]]; then
        credentials=${response#*$'\n'}
        username=${credentials%%$'\n'*}
        password=${credentials#*$'\n'}
        if [[ $credentials != *$'\n'* || -z $username || -z $password || $password == *$'\n'* ]]; then
            printf 'BMC 解析器返回的凭据信息格式无效。\n' >&2
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
    builtin printf '已选择 BMC 目标 %s。\n' "$1" >&2 || :
}

_bmcshell_exit() {
    if (( $# )); then
        if (( $# == 1 )) && [[ $1 == -h || $1 == --help ]]; then
            _bmcshell_call help exit
            return
        fi
        printf 'bmc exit 不接受额外参数。请运行 bmc exit --help 查看用法。\n' >&2
        return 2
    fi
    if [[ -z ${BMCSHELL_CONTEXT-} ]]; then
        printf '当前没有可退出的 BMC 会话。\n'
        return 0
    fi
    _bmcshell_check_context || return
    _bmcshell_restore_session_tools || return
    builtin unset BMCSHELL_CONTEXT || return
    _BMCSHELL_SESSION_STATE=()
    _bmcshell_render_prompt || :
}

_bmcshell_check_proxy_environment() {
    local name declaration
    for name in "${_BMCSHELL_PUBLIC_PROXY_NAMES[@]}" BMCSHELL_PROXY_ADDRESS; do
        declaration=$(builtin declare -p "$name" 2>/dev/null) || continue
        case $declaration in
            'declare -- '*|'declare -x '*) ;;
            *) printf '代理变量 %s 的 Bash 属性不受支持，无法更新。\n' "$name" >&2
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
        printf 'BMC 全局代理：开启（SOCKS5 %s）\n' "$address"
    fi
}

_bmcshell_check_setting_env_name() {
    [[ $1 =~ ^[A-Z][A-Z0-9]*_[A-Z][A-Z0-9]*_[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$ ]] || return 1
    case $1 in
        BASH_*|LD_*|DYLD_*|MSYS*|CYGWIN*|BMCSHELL_BRIDGE_*|BMCSHELL_BOOK_*|\
        BMCSHELL_RESULT_FILE|BMCSHELL_RESOLVER_LIBRARIES|BMCSHELL_RESOLVER_BASH|BMCSHELL_RESOLVER_CORE_EXE|\
        BMCSHELL_UI_PAGER|BMCSHELL_PROXY_ADDRESS|BMCSHELL_AUTH_PASSWORD|BMCSHELL_SNMP_COMMUNITY) return 1 ;;
    esac
}

_bmcshell_apply_setting_environment() {
    local payload=$1 header row name value declaration flags
    local -A values=()
    {
        IFS= builtin read -r header
        header=${header%$'\r'}
        if [[ $header != $'settings-env-v1\t'* ]]; then
            printf '设置环境的适配响应无效。\n' >&2
            return 2
        fi
        while IFS= builtin read -r row; do
            row=${row%$'\r'}
            [[ -n $row ]] || continue
            name=${row%%=*}
            if [[ $row != *=* ]] || ! _bmcshell_check_setting_env_name "$name" ||
                [[ ${values[$name]+set} ]]; then
                printf '设置环境变量声明无效或重复：%q\n' "$name" >&2
                return 2
            fi
            values[$name]=${row#*=}
        done
    } <<< "$payload"

    # Check all affected variables before changing the shell.
    for name in "${!values[@]}" "${!_BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS[@]}"; do
        declaration=$(builtin declare -p "$name" 2>/dev/null) || continue
        case $declaration in
            'declare -- '*|'declare -x '*) ;;
            *) printf '设置环境变量 %s 的 Bash 属性不受支持，无法重载。\n' "$name" >&2
               return 2 ;;
        esac
    done
    local address=${header#*$'\t'}
    if [[ -n $address || -n ${_BMCSHELL_PROXY_ADDRESS-} ]]; then
        _bmcshell_check_proxy_environment || return
        _bmcshell_apply_proxy "$address" || return
    else
        _BMCSHELL_PROXY_ADDRESS=
    fi

    for name in "${!_BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS[@]}"; do
        [[ ! ${values[$name]+set} ]] || continue
        flags=${_BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS[$name]}
        if [[ ${_BMCSHELL_SETTING_ENV_ORIGINAL_VALUES[$name]+set} ]]; then
            builtin printf -v "$name" '%s' "${_BMCSHELL_SETTING_ENV_ORIGINAL_VALUES[$name]}" || return
            if [[ $flags == -x ]]; then
                builtin export "$name" || return
            else
                builtin export -n "$name" || return
            fi
        else
            builtin unset "$name" || return
            if [[ $flags == -x ]]; then
                builtin export "$name" || return
            fi
        fi
        builtin unset '_BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS[$name]' '_BMCSHELL_SETTING_ENV_ORIGINAL_VALUES[$name]'
    done
    for name in "${!values[@]}"; do
        if [[ ! ${_BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS[$name]+set} ]]; then
            declaration=$(builtin declare -p "$name" 2>/dev/null) || declaration=
            if [[ $declaration == 'declare -x '* ]]; then
                _BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS[$name]=-x
            else
                _BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS[$name]=--
            fi
            if [[ ${!name+x} ]]; then
                _BMCSHELL_SETTING_ENV_ORIGINAL_VALUES[$name]=${!name}
            fi
        fi
        value=${values[$name]}
        builtin export "$name=$value" || return
    done
}

_bmcshell_load_setting_environment() {
    local payload
    if ! payload=$(_bmcshell_bridge setting-environment 2>&1); then
        # An updated adapter can still launch an older executable.
        if [[ ${1-} == startup && $payload == *'未知的 Bash 适配操作 "setting-environment"'* ]]; then
            if [[ ! ${_BMCSHELL_PROXY_ADDRESS+x} ]]; then
                _bmcshell_load_proxy || return
            fi
            return 0
        fi
        printf '%s\n' "$payload" >&2
        return 2
    fi
    _bmcshell_apply_setting_environment "$payload"
}

_bmcshell_reload_settings() {
    _bmcshell_load_setting_environment || return
    _bmcshell_refresh_globals || return
    _bmcshell_render_prompt strict
}

_bmcshell_setting_menu_bridge() (
    local result native_result status
    result=$(command mktemp -t bmcshell-setting.XXXXXXXX) || return
    trap 'command rm -f -- "$result"' EXIT
    native_result=$result
    if command -v cygpath >/dev/null; then
        native_result=$(command cygpath -aw -- "$result") || return
    fi
    BMCSHELL_BRIDGE_SETTING_RESULT="$native_result" _bmcshell_call setting 1>&3 3>&- || return
    if IFS= builtin read -r status < "$result"; then
        builtin printf '%s' "$status"
    fi
)

_bmcshell_setting() {
    local reload=0 result
    if [[ ${!#} == --reload ]]; then
        case ${1-} in
            --reload) (( $# == 1 )) && reload=1 ;;
            --reset|--check) (( $# >= 2 )) && reload=1 ;;
            --set) (( $# >= 4 )) && reload=1 ;;
            --get) (( $# >= 3 )) && reload=1 ;;
        esac
        if (( reload )); then
            set -- "${@:1:$#-1}"
        fi
    fi
    if (( reload && $# == 0 )); then
        _bmcshell_reload_settings || return
        printf '已重载当前终端的设置环境。\n'
        return 0
    fi
    if (( $# == 0 )) && [[ -t 0 ]]; then
        { result=$(_bmcshell_setting_menu_bridge 3>&4 4>&-); } 4>&1 || return
        [[ $result != committed ]] || reload=1
    else
        BMCSHELL_BRIDGE_SETTING_RESULT= _bmcshell_call setting "$@" || return
    fi
    if (( reload )); then
        if ! _bmcshell_reload_settings; then
            printf '当前终端设置环境重载失败，可运行 bmc setting --reload 重试。\n' >&2
            return 2
        fi
        printf '已重载当前终端的设置环境。\n'
    fi
}

_bmcshell_public_command() {
    if command -v cygpath >/dev/null; then
        if [[ $# -ge 4 && $1 == plugin && $2 == install && $3 == --file ]]; then
            local path
            local -a paths=()
            shift 3
            for path in "$@"; do
                path=$(command cygpath -aw -- "$path") || return
                paths+=("$path")
            done
            _bmcshell_call plugin install --file "${paths[@]}"
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
            doctor) words+=' -d --detail -f --filter -a --auto' ;;
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
                elif [[ ${COMP_WORDS[2]} == list ]]; then
                    local installed_options=' -i --installed' detail_options=' -d --detail'
                    local plugin_types=' book library theme tool bundle' index
                    for (( index = 3; index < COMP_CWORD; index++ )); do
                        case ${COMP_WORDS[index]} in
                            -i|--installed) installed_options= ;;
                            -d|--detail) detail_options= ;;
                            book|library|theme|tool|bundle) plugin_types= ;;
                        esac
                    done
                    words+="$installed_options$detail_options$plugin_types"
                elif [[ ${COMP_WORDS[2]} == search ]]; then
                    local detail_options=' -d --detail' index
                    for (( index = 3; index < COMP_CWORD; index++ )); do
                        case ${COMP_WORDS[index]} in
                            -d|--detail) detail_options=; break ;;
                        esac
                    done
                    words+="$detail_options"
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
            setting) words+=' --get --set --reset --check --insecure --reload' ;;
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
if [[ ${_BMCSHELL_SETTING_ENV_READY-} != 1 ]]; then
    if (( BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] < 2 )); then
        builtin declare -A _BMCSHELL_SETTING_ENV_ORIGINAL_VALUES _BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS || return
    else
        builtin declare -gA _BMCSHELL_SETTING_ENV_ORIGINAL_VALUES _BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS || return
    fi
    _BMCSHELL_SETTING_ENV_ORIGINAL_VALUES=() _BMCSHELL_SETTING_ENV_ORIGINAL_FLAGS=()
    _BMCSHELL_SETTING_ENV_READY=1
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

_bmcshell_load_setting_environment startup || return

function bmc {
    case ${1-} in
        use) shift; _bmcshell_use "$@" ;;
        exit) shift; _bmcshell_exit "$@" ;;
        proxy) shift; _bmcshell_proxy "$@" ;;
        setting) shift; _bmcshell_setting "$@" ;;
        plugin)
            _bmcshell_public_command "$@" || return
            case ${2-} in
                install|upgrade|remove) _bmcshell_render_prompt || : ;;
            esac
            ;;
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
                printf '命令 bridge 为 BMCShell 内部接口。请运行 bmc help 查看可用命令。\n' >&2
                return 2
            fi
            ;;
        *) _bmcshell_public_command "$@" ;;
    esac
}

if [[ $- == *i* ]]; then
    builtin complete -F _bmcshell_complete bmc
    _bmcshell_render_prompt || :
fi
_bmcshell_refresh_globals || return

if [[ $- == *i* && ${_BMCSHELL_STARTUP_SHOWN-} != 1 ]]; then
    _bmcshell_startup_version=$("$_BMCSHELL_EXE" --version 2>/dev/null) || _bmcshell_startup_version=BMCShell
    builtin printf '%s 已加载。bmc use IP 连接设备；bmc help 查看帮助。\n' "${_bmcshell_startup_version%$'\r'}"
    _BMCSHELL_STARTUP_SHOWN=1
    builtin unset _bmcshell_startup_version
fi
