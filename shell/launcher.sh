# Source the portable installation's bin/bmcshell.sh from Bash.
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
    printf 'Load BMCShell with: source %q\n' "${BASH_SOURCE[0]}" >&2
    exit 2
fi

_bmcshell_resolve_portable_adapter() {
    local launcher=${BASH_SOURCE[0]} bin version trailing release adapter
    local version_pattern='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?([+][0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
    [[ $launcher == */* ]] || launcher=./$launcher
    bin=$(builtin cd -P -- "${launcher%/*}" && builtin pwd -P) || return
    if [[ ! -f $bin/active || -L $bin/active ]] ||
        ! { IFS= read -r -n 130 version && ! IFS= read -r -n 1 trailing; } < "$bin/active" ||
        [[ ${#version} -gt 128 || ! $version =~ $version_pattern ]]; then
        printf 'BMCShell: invalid active release marker: %s\n' "$bin/active" >&2
        printf 'Repair the launcher and marker with a verified release ZIP, or restore bin/ as described in the installation guide.\n' >&2
        return 2
    fi
    release=$bin/releases/$version
    adapter=$release/shell/bmcshell.sh
    if [[ -L $bin/releases || -L $release || -L $release/shell ||
        ! -s $adapter || -L $adapter || ! -s $release/BMCShell.exe || -L $release/BMCShell.exe ]]; then
        printf 'BMCShell: active release %s is incomplete. Restore bin/ using a verified release ZIP.\n' "$version" >&2
        return 2
    fi
    builtin printf '%s\n' "$adapter"
}

if ! _bmcshell_portable_adapter=$(_bmcshell_resolve_portable_adapter); then
    builtin unset -f _bmcshell_resolve_portable_adapter
    builtin unset _bmcshell_portable_adapter
    return 2
fi
builtin unset -f _bmcshell_resolve_portable_adapter
if builtin source "$_bmcshell_portable_adapter"; then
    builtin unset _bmcshell_portable_adapter
    return 0
fi
builtin unset _bmcshell_portable_adapter
return 2
