package provision

import (
	"strconv"
	"strings"
)

// linuxbrewPrefix is where Homebrew lives on a bastion.
const linuxbrewPrefix = "/home/linuxbrew/.linuxbrew"

// fallbackShellPath is the PATH the snippet sets when the rebuilt PATH comes
// out empty, so a shell never ends up unable to find the base system tools.
const fallbackShellPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// rcLockWaitSeconds bounds how long the shell environment phase waits for
// another init on the same account to finish with the rc files.
const rcLockWaitSeconds = 30

// Marker lines around the managed PATH block in shell rc files. The shell
// environment phase finds every copy of the block by these exact lines.
const (
	pathBlockBegin = "# >>> ocfp PATH order (managed by ocfp) >>>"
	pathBlockEnd   = "# <<< ocfp PATH order (managed by ocfp) <<<"
)

// pathOrderSnippet returns POSIX sh that rebuilds PATH as $HOME/bin (when it
// exists), then brewPrefix/bin and brewPrefix/sbin (when brewPrefix/bin is a
// directory), then the rest of PATH in its existing order. Each entry is kept
// only at its first occurrence and empty entries are dropped.
//
// The snippet is sourced from ~/.profile and /etc/profile.d under dash and
// from ~/.bashrc under bash, so it uses POSIX syntax and shell builtins only:
// it must work even when PATH is too broken to find sed or tr. It is safe
// under set -eu, and it unsets its temporary variables before it returns.
func pathOrderSnippet(brewPrefix string) string {
	brewBin := shellSingleQuote(brewPrefix + "/bin")
	brewSbin := shellSingleQuote(brewPrefix + "/sbin")

	lines := []string{
		`_ocfp_rest="${PATH:-}"`,
		"if [ -d " + brewBin + " ]; then",
		`    _ocfp_rest=` + brewBin + `:` + brewSbin + `":${_ocfp_rest}"`,
		"fi",
		`if [ -n "${HOME:-}" ] && [ -d "${HOME}/bin" ]; then`,
		`    _ocfp_rest="${HOME}/bin:${_ocfp_rest}"`,
		"fi",
		`_ocfp_new=""`,
		`while [ -n "${_ocfp_rest}" ]; do`,
		`    case "${_ocfp_rest}" in`,
		`        *:*) _ocfp_dir="${_ocfp_rest%%:*}"; _ocfp_rest="${_ocfp_rest#*:}" ;;`,
		`        *) _ocfp_dir="${_ocfp_rest}"; _ocfp_rest="" ;;`,
		`    esac`,
		`    if [ -n "${_ocfp_dir}" ]; then`,
		`        case ":${_ocfp_new}:" in`,
		`            *":${_ocfp_dir}:"*) ;;`,
		`            *) _ocfp_new="${_ocfp_new:+${_ocfp_new}:}${_ocfp_dir}" ;;`,
		`        esac`,
		`    fi`,
		`done`,
		`PATH="${_ocfp_new:-` + fallbackShellPath + `}"`,
		`export PATH`,
		`unset _ocfp_rest _ocfp_new _ocfp_dir`,
	}

	return strings.Join(lines, "\n") + "\n"
}

// pathOrderBlock returns the PATH snippet for the bastion's Homebrew prefix,
// wrapped in its marker lines, as it appears in shell rc files.
func pathOrderBlock() string {
	return pathBlockBegin + "\n" + pathOrderSnippet(linuxbrewPrefix) + pathBlockEnd + "\n"
}

// generateRcConvergeScript returns bash that leaves exactly one copy of the
// PATH block at the end of ~/.bashrc (created when missing) and of
// ~/.profile, ~/.bash_profile, and ~/.bash_login when each exists. Copies
// anywhere else in a file are removed. When ~/.bashrc lacks the one-time OCFP
// configuration, bashrcAdditions goes in just before its PATH block.
//
// Each file is checked before anything is written to it. A file whose markers
// do not pair up is skipped with a warning, the one-time configuration
// included, rather than risk cutting off its tail. A symlinked file is
// resolved so the update lands in its target and the link survives.
//
// A file that already holds the desired content is not touched. Any other
// file is replaced atomically: the new content is staged in a copy of the
// file in the same directory, which keeps its mode, owner, and group, and is
// then renamed over it. If the copy cannot keep the owner and group, the file
// is skipped with a warning.
//
// The whole step holds a per-user flock on ~/.ocfp/rc-path.lock, so two inits
// on one account cannot interleave their updates. When the lock stays busy for
// lockWait seconds, the step warns and skips the rc files instead of failing
// the phase. Every temporary file is removed on every exit path, including
// an interrupt.
func generateRcConvergeScript(bashrcAdditions string, lockWait int) string {
	var script strings.Builder

	script.WriteString("# Keep the managed PATH block last in every shell rc file, and add the\n")
	script.WriteString("# one-time OCFP configuration to ~/.bashrc when it is missing\n")
	script.WriteString("ocfp_converge_rc_files() (\n")
	script.WriteString(rcConvergeStaging(bashrcAdditions))
	script.WriteString(rcConvergeLock(lockWait))
	script.WriteString(rcConvergeFileFunction())
	script.WriteString(`    if [ ! -e "$HOME/.bashrc" ] && [ ! -L "$HOME/.bashrc" ]; then
        : > "$HOME/.bashrc"
    fi
    ocfp_converge_rc "$HOME/.bashrc" "$ocfp_additions"
    ocfp_rc_discard
    for ocfp_rc in "$HOME/.profile" "$HOME/.bash_profile" "$HOME/.bash_login"; do
        if [ -f "$ocfp_rc" ]; then
            ocfp_converge_rc "$ocfp_rc" ""
            ocfp_rc_discard
        fi
    done
)
ocfp_converge_rc_files
unset -f ocfp_converge_rc_files
`)

	return script.String()
}

// rcConvergeStaging returns the part of the rc converge subshell that sets
// up cleanup traps and stages the PATH block and the one-time ~/.bashrc
// configuration in temporary files.
func rcConvergeStaging(bashrcAdditions string) string {
	var script strings.Builder

	script.WriteString(`    ocfp_block="" ocfp_additions="" ocfp_desired="" ocfp_staged="" ocfp_snap=""
    ocfp_rc_discard() {
        local f
        for f in "$ocfp_desired" "$ocfp_staged" "$ocfp_snap"; do
            if [ -n "$f" ]; then rm -f -- "$f"; fi
        done
        ocfp_desired="" ocfp_staged="" ocfp_snap=""
    }
    ocfp_rc_cleanup() {
        ocfp_rc_discard
        local f
        for f in "$ocfp_block" "$ocfp_additions"; do
            if [ -n "$f" ]; then rm -f -- "$f"; fi
        done
    }
    trap ocfp_rc_cleanup EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM

    ocfp_block=$(mktemp "${TMPDIR:-/tmp}/ocfp-path-block.XXXXXX")
    cat > "$ocfp_block" << 'OCFP_PATH_BLOCK_EOF'
`)
	script.WriteString(pathOrderBlock())
	script.WriteString("OCFP_PATH_BLOCK_EOF\n")
	script.WriteString(`    ocfp_additions=$(mktemp "${TMPDIR:-/tmp}/ocfp-bashrc.XXXXXX")
    cat > "$ocfp_additions" << 'BASHRC_EOF'
`)
	script.WriteString(bashrcAdditions)
	script.WriteString("\nBASHRC_EOF\n")

	return script.String()
}

// rcConvergeLock returns the part of the rc converge subshell that takes the
// per-user lock, waiting at most lockWait seconds, and leaves the subshell
// with a warning when it cannot.
func rcConvergeLock(lockWait int) string {
	wait := strconv.Itoa(lockWait)

	return `
    ocfp_lock="$HOME/.ocfp/rc-path.lock"
    if ! mkdir -p "$HOME/.ocfp" 2>/dev/null || ! { : >> "$ocfp_lock"; } 2>/dev/null; then
        log_warning "Skipping shell rc files: cannot open the lock file $ocfp_lock"
        exit 0
    fi
    exec 9>> "$ocfp_lock"
    if ! command -v flock >/dev/null 2>&1; then
        log_warning "flock is not installed; updating shell rc files without a lock"
    elif ! flock -w ` + wait + ` 9; then
        log_warning "Skipping shell rc files: another ocfp init held $ocfp_lock for ` + wait + ` seconds"
        exit 0
    fi

`
}

// rcSeamBeforeReplace is a comment line in the converge script, right before
// the rename. It does nothing on a bastion. A test replaces it with commands
// to change an rc file at that point without depending on timing.
const rcSeamBeforeReplace = "# ocfp-seam: before-replace"

// rcConvergeFileFunction returns the bash function that checks one rc file,
// builds its desired content, and replaces it atomically when it differs.
func rcConvergeFileFunction() string {
	return `    ocfp_rc_owner() {
        ls -lnd -- "$1" | awk '{ print $3 ":" $4 }'
    }

    ocfp_converge_rc() {
        local rc="$1" additions="$2" attempt=1 status
        while [ "$attempt" -le 3 ]; do
            status=0
            ocfp_converge_rc_once "$rc" "$additions" || status=$?
            ocfp_rc_discard
            if [ "$status" -ne 10 ]; then
                return 0
            fi
            attempt=$((attempt + 1))
        done
        log_warning "Skipping $rc: it kept changing while the PATH block was being updated"
    }

    ocfp_converge_rc_once() {
        local rc="$1" additions="$2" target dir status=0 configured=""
        if ! target=$(readlink -f -- "$rc") || [ ! -f "$target" ]; then
            log_warning "Skipping $rc: it does not resolve to a regular file"
            return 0
        fi
        ocfp_snap=$(mktemp "${TMPDIR:-/tmp}/ocfp-rc-snap.XXXXXX")
        if ! cp -- "$target" "$ocfp_snap" 2>/dev/null; then
            log_warning "Skipping $rc: could not read it"
            return 0
        fi
        ocfp_desired=$(mktemp "${TMPDIR:-/tmp}/ocfp-rc.XXXXXX")
        LC_ALL=C awk -v begin=` + shellSingleQuote(pathBlockBegin) + ` -v end=` + shellSingleQuote(pathBlockEnd) + ` '
            $0 == begin { if (inside) { bad = 1; exit } inside = 1; next }
            $0 == end   { if (!inside) { bad = 1; exit } inside = 0; next }
            !inside     { print }
            END         { if (bad || inside) exit 3 }
        ' "$ocfp_snap" > "$ocfp_desired" || status=$?
        if [ "$status" -eq 3 ]; then
            log_warning "Skipping $rc: its ocfp PATH block markers do not pair up"
            return 0
        elif [ "$status" -ne 0 ]; then
            log_warning "Skipping $rc: could not read it"
            return 0
        fi
        if [ -n "$additions" ] && ! grep -q "OCFP Environment Configuration" "$ocfp_desired"; then
            log_info 'Configuring shell environment'
            cat "$additions" >> "$ocfp_desired"
            configured=1
        elif [ -n "$additions" ]; then
            log_success 'Shell environment already configured'
        fi
        cat "$ocfp_block" >> "$ocfp_desired"
        if cmp -s "$ocfp_desired" "$ocfp_snap"; then
            log_success "PATH block in $rc is current"
            return 0
        fi
        dir=$(dirname -- "$target")
        if ! ocfp_staged=$(mktemp "$dir/.ocfp-rc.XXXXXX" 2>/dev/null); then
            ocfp_staged=""
            log_warning "Skipping $rc: cannot create a temporary file in $dir"
            return 0
        fi
        if ! cp -p -- "$target" "$ocfp_staged" 2>/dev/null || ! cat "$ocfp_desired" > "$ocfp_staged"; then
            log_warning "Skipping $rc: could not stage its new content"
            return 0
        fi
        if [ "$(ocfp_rc_owner "$ocfp_staged")" != "$(ocfp_rc_owner "$target")" ]; then
            log_warning "Skipping $rc: a replacement could not keep its owner and group"
            return 0
        fi
        ` + rcSeamBeforeReplace + `
        if ! cmp -s -- "$ocfp_snap" "$target"; then
            return 10
        fi
        if ! mv -f -- "$ocfp_staged" "$target"; then
            log_warning "Could not update $rc; left it unchanged"
            return 0
        fi
        ocfp_staged=""
        if [ -n "$configured" ]; then
            log_success 'Shell environment configured'
        fi
        log_success "PATH block in $rc updated"
    }

`
}
