# shellcheck shell=bash
# /etc/bash/bashrc.d/30-bentoo-safety.bash
# Bentoo: safety guards for destructive commands (interactive shells only).
# Functions (not exported), so scripts, cron jobs and systemd units are NOT
# affected. Deliberate bypass: BENTOO_NO_GUARD=1 <command>
#
# Commands guarded by a confirmation code:
#   power : reboot poweroff halt shutdown
#   rm    : recursive (-r, WITH or WITHOUT -f) and targets in critical dirs
#   disk  : dd mkfs[.*] mkswap wipefs blkdiscard shred fdisk gdisk sgdisk
#           parted truncate cryptsetup(luksFormat/erase/reencrypt)
#   perms : chmod -R / chown -R on a critical directory
#   gentoo: emerge -C/--unmerge/--depclean/--prune
#
# The same rules apply when the command is elevated through sudo/doas/run0/
# pkexec (e.g. `sudo rm -rf /something`). Without that, the elevator would run
# the real binary and slip past the guard -- precisely for the commands that
# need root (dd, mkfs, emerge -C, ...). The elevation wrapper NEVER changes the
# arguments handed to the elevator; it only asks for the confirmation (or
# blocks) before running it.
#
# Messages are translated through the bentoolkit gettext catalog (installed
# with app-portage/bentoolkit's L10N flags), following LANGUAGE, LC_ALL,
# LC_MESSAGES and LANG; without gettext or a catalog they stay English.

# Interactive shells only. Everything below (the elevation wrappers included)
# does not exist in scripts, cron jobs and systemd units, which are not
# interactive.
[[ $- == *i* ]] || return 0

# _bentoo_t MSGID: the translation of MSGID, or MSGID itself. MSGID is always
# a single-quoted literal, so misc/i18n-extract can collect it.
_bentoo_t() {
    if command -v gettext >/dev/null 2>&1; then
        TEXTDOMAIN=bentoolkit TEXTDOMAINDIR=${BENTOO_LOCALEDIR:-/usr/share/locale} gettext -- "$1"
    else
        printf '%s' "$1"
    fi
}

# Directories whose content the system cannot do without (a code is asked
# even without recursion).
_bentoo_critical_paths='/etc /boot /usr /bin /sbin /lib /lib64 /var /opt /root /dev /proc /sys /run'

_bentoo_is_critical_dir() {
    local p=${1%/} c
    [[ -z $p ]] && return 1
    for c in $_bentoo_critical_paths; do
        [[ $p == "$c" || $p == "$c"/* ]] && return 0
    done
    return 1
}

# _bentoo_blocked FORMAT ARG: sets _BENTOO_LABEL to the red BLOCKED badge
# followed by the translated FORMAT applied to ARG (%q-quoted).
_bentoo_blocked() {
    local badge reason
    badge=$(_bentoo_t 'BLOCKED')
    # shellcheck disable=SC2059 # the format is a translated message
    printf -v reason "$1" "$2"
    printf -v _BENTOO_LABEL '\033[1;41m %s \033[0m %s' "$badge" "$reason"
}

_bentoo_delay() {
    local label=$1
    [[ -n $BENTOO_NO_GUARD ]] && return 0
    printf '\033[1;31m⚠️  %s\033[0m\n' "$label"

    # Freeze terminal input during the countdown: with no echo and no
    # canonical mode, keys typed go to the raw buffer (neither shown nor
    # confirming anything). Any key during the countdown cancels.
    local stty_saved=
    stty_saved=$(stty -g 2>/dev/null) || stty_saved=

    # Restore the terminal on ANY exit (Ctrl-C included). Without the trap,
    # SIGINT would abort the function before stty is restored and leave the
    # terminal stuck in raw mode (you type and nothing appears).
    local _bentoo_cancel=
    trap '_bentoo_cancel=1' INT
    [[ -n $stty_saved ]] && stty -echo -icanon 2>/dev/null

    local countdown cancelled wait_hint
    countdown=$(_bentoo_t '%ds to cancel (Ctrl-C or any key)...')
    cancelled=$(_bentoo_t 'Cancelled.')
    wait_hint=$(_bentoo_t 'Wait for the countdown to finish before typing.')

    # Countdown: Ctrl-C OR any key cancels.
    local i
    for ((i=3; i>0; i--)); do
        # shellcheck disable=SC2059 # the format is a translated message
        printf "\r\033[33m   ${countdown} \033[0m" "$i"
        read -rsn1 -t 1 _ && _bentoo_cancel=1   # a key typed => cancel
        if [[ -n $_bentoo_cancel ]]; then
            trap - INT
            [[ -n $stty_saved ]] && stty "$stty_saved" 2>/dev/null
            printf '\r\033[K\n\033[32m%s\033[0m %s\n' "$cancelled" "$wait_hint"
            return 1
        fi
    done
    printf '\r\033[K'

    # Normal input again only now, to read the code being typed.
    [[ -n $stty_saved ]] && stty "$stty_saved" 2>/dev/null

    # Confirmation with a 4-character alphanumeric code (no ambiguous characters)
    local chars='ABCDEFGHJKLMNPQRSTUVWXYZ23456789' code='' answer prompt
    for ((i=0; i<4; i++)); do code+=${chars:RANDOM%${#chars}:1}; done

    # shellcheck disable=SC2059 # the format is a translated message
    printf -v prompt "$(_bentoo_t 'Type the code %s to confirm:')" $'\033[1;33m'"$code"$'\033[1;37m'
    printf '\033[1;37m%s \033[0m' "$prompt"
    _bentoo_cancel=
    if ! read -r answer || [[ -n $_bentoo_cancel ]]; then
        trap - INT
        printf '\n\033[32m%s\033[0m\n' "$cancelled"; return 1
    fi
    trap - INT
    if [[ ${answer^^} != "$code" ]]; then
        printf '\033[1;41m %s \033[0m %s\n' "$(_bentoo_t 'Cancelled')" "$(_bentoo_t 'wrong code.')"
        return 1
    fi
    return 0
}

# --- decision core -----------------------------------------------------------
# Assesses (command + args) and returns the policy in _BENTOO_LABEL:
#   rc 0 => confirm (label = text shown at the confirmation)
#   rc 1 => allow   (nothing to do)
#   rc 2 => block   (label = blocking message, already coloured)
# It is the ONLY source of the rules: direct guards and elevation wrappers use it.

_bentoo_assess_rm() {
    local recursive='' force='' no_preserve='' end_opts=''
    local -a targets=()
    local a
    for a in "$@"; do
        if [[ -z $end_opts && $a == -- ]]; then end_opts=1; continue; fi
        if [[ -z $end_opts && $a == --* ]]; then
            case "$a" in
                --recursive|--dir) recursive=1 ;;
                --force) force=1 ;;
                --no-preserve-root) no_preserve=1 ;;
            esac
            continue
        fi
        if [[ -z $end_opts && $a == -* ]]; then
            [[ $a == *[rRd]* ]] && recursive=1
            [[ $a == *f* ]] && force=1
            continue
        fi
        targets+=("$a")
    done

    # Catastrophic targets are blocked OUTRIGHT (even without -rf)
    local t
    for t in "${targets[@]}"; do
        case "$t" in
            / | /\* | '~' | '.' | '..' | '*' )
                _bentoo_blocked "$(_bentoo_t 'critical target: %q')" "$t"
                return 2 ;;
        esac
        if [[ $t == "$HOME" || $t == "$HOME/" ]]; then
            _bentoo_blocked "$(_bentoo_t 'deleting HOME: %q')" "$t"
            return 2
        fi
    done
    [[ -n $no_preserve ]] && {
        _bentoo_blocked '%s' "$(_bentoo_t '--no-preserve-root refused.')"
        return 2
    }

    # A code when: recursive (with or WITHOUT -f) OR a target in a critical dir
    local crit=
    for t in "${targets[@]}"; do
        _bentoo_is_critical_dir "$t" && { crit=1; break; }
    done
    if [[ -n $recursive || -n $crit ]]; then
        _BENTOO_LABEL="rm${recursive:+ -r}${force:+f}: ${targets[*]}"
        return 0
    fi
    return 1
}

_bentoo_assess_perm() {
    local real=$1; shift
    local a rec=
    for a in "$@"; do
        [[ $a == --recursive || ( $a == -* && $a == *R* ) ]] && { rec=1; break; }
    done
    [[ -z $rec ]] && return 1
    for a in "$@"; do
        [[ $a == -* ]] && continue
        if _bentoo_is_critical_dir "$a"; then
            # shellcheck disable=SC2059 # the format is a translated message
            printf -v _BENTOO_LABEL "$(_bentoo_t '%s -R on %s')" "$real" "$a"
            return 0
        fi
    done
    return 1
}

_bentoo_assess() {
    local cmd=$1; shift
    _BENTOO_LABEL=
    case "$cmd" in
        reboot|poweroff|halt|shutdown)
            # shellcheck disable=SC2059 # the format is a translated message
            printf -v _BENTOO_LABEL "$(_bentoo_t '%s requested')" "$cmd"; return 0 ;;

        dd|mkswap|wipefs|blkdiscard|shred|fdisk|gdisk|sgdisk|parted|truncate)
            _BENTOO_LABEL="$cmd ${*}"; return 0 ;;
        mkfs|mkfs.*)
            _BENTOO_LABEL="$cmd ${*}"; return 0 ;;

        cryptsetup)
            case "$1" in
                luksFormat|luksErase|erase|reencrypt)
                    _BENTOO_LABEL="cryptsetup ${*}"; return 0 ;;
            esac
            return 1 ;;

        chmod|chown)
            _bentoo_assess_perm "$cmd" "$@"; return $? ;;

        emerge)
            local a
            for a in "$@"; do
                case "$a" in
                    -C|--unmerge|--depclean|-c|--prune|-P)
                        # shellcheck disable=SC2059 # the format is a translated message
                        printf -v _BENTOO_LABEL "$(_bentoo_t 'emerge (removal): %s')" "${*}"; return 0 ;;
                    --) break ;;
                esac
            done
            return 1 ;;

        rm)
            _bentoo_assess_rm "$@"; return $? ;;
    esac
    return 1
}

# Applies the policy to (command + args). Returns 0 when it may proceed, 1 when
# it must abort (blocked, or the confirmation was refused or cancelled).
_bentoo_gate() {
    _bentoo_assess "$@"
    case $? in
        2) printf '%s\n' "$_BENTOO_LABEL" >&2; return 1 ;;
        0) _bentoo_delay "$_BENTOO_LABEL" || return 1 ;;
    esac
    return 0
}

# --- direct guards ------------------------------------------------------------
# Each wrapper assesses in the shell and, when allowed, calls the real binary.
_bentoo_c=
for _bentoo_c in reboot poweroff halt shutdown \
                 dd mkswap wipefs blkdiscard shred fdisk gdisk sgdisk \
                 parted truncate \
                 mkfs mkfs.ext2 mkfs.ext3 mkfs.ext4 mkfs.btrfs mkfs.xfs \
                 mkfs.vfat mkfs.fat mkfs.f2fs mkfs.ntfs \
                 rm chmod chown cryptsetup emerge; do
    eval "${_bentoo_c}() { _bentoo_gate ${_bentoo_c} \"\$@\" || return 1; command ${_bentoo_c} \"\$@\"; }"
done
unset _bentoo_c

# --- privilege elevation (sudo/doas/run0/pkexec) -----------------------------
# Extracts the real command behind the elevator. Result in _BENTOO_REAL (name)
# and _bentoo_cmd_args[] (the command's arguments). rc 0 = found; 1 = there is
# no command (a shell session, --help, options only) and nothing is intercepted.
#
# Parsing is deliberately conservative: when wrong, it fails "open" (finds no
# command => intercepts nothing). The original arguments are NEVER changed;
# the elevator always runs as `command "$elev" "$@"` in _bentoo_guard_elev.
_bentoo_parse_elev() {
    local elev=$1; shift
    _BENTOO_REAL=
    _bentoo_cmd_args=()

    # Short options that take the NEXT token as their value (`-x val` form).
    local shortarg
    case "$elev" in
        sudo)   shortarg='CDghpRrtTUu' ;;
        doas)   shortarg='aCu' ;;
        run0)   shortarg='puMHGE' ;;
        *)      shortarg='' ;;   # pkexec and the like: no short option with a separate value
    esac
    # Long options (`--opt val` form, no '=') that take the next token.
    local longarg=' user group chdir chroot role type host prompt setenv unit slice machine property command-timeout close-from other-user '

    local a skipnext='' seen_dd=''
    while (( $# )); do
        a=$1; shift
        if [[ -n $skipnext ]]; then skipnext=; continue; fi
        if [[ -n $seen_dd ]]; then
            _BENTOO_REAL=$a; _bentoo_cmd_args=("$@"); return 0
        fi
        case "$a" in
            --)      seen_dd=1; continue ;;
            --*=*)   continue ;;                      # --opt=val: value embedded
            --*)     [[ $longarg == *" ${a#--} "* ]] && skipnext=1; continue ;;
            -[!-]*)  # short option; takes the next token only in the exact `-x` form
                     [[ ${#a} -eq 2 && $shortarg == *"${a:1:1}"* ]] && skipnext=1
                     continue ;;
            *=*)     # an environment assignment before the command (e.g. sudo FOO=bar cmd)
                     [[ $a == [A-Za-z_]*=* ]] && continue
                     _BENTOO_REAL=$a; _bentoo_cmd_args=("$@"); return 0 ;;
            *)       _BENTOO_REAL=$a; _bentoo_cmd_args=("$@"); return 0 ;;
        esac
    done
    return 1
}

_bentoo_guard_elev() {
    local elev=$1; shift
    if (( $# )); then
        _bentoo_parse_elev "$elev" "$@"
        [[ -n $_BENTOO_REAL ]] && { _bentoo_gate "$_BENTOO_REAL" "${_bentoo_cmd_args[@]}" || return 1; }
    fi
    command "$elev" "$@"
}

# Defined through eval to keep a single body (and to avoid the definition
# literal that trips "command injection" false positives in linters).
_bentoo_e=
for _bentoo_e in sudo doas run0 pkexec; do
    eval "${_bentoo_e}() { _bentoo_guard_elev ${_bentoo_e} \"\$@\"; }"
done
unset _bentoo_e
