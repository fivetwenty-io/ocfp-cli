package provision

// ubuntuDefaultPath is the PATH that Ubuntu ships in /etc/environment. The
// PATH transform starts from it when the file has no PATH line.
const ubuntuDefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games:/usr/local/games:/snap/bin"

// etcEnvironmentPathFunctionName names the bash function that
// etcEnvironmentPathFunction defines.
const etcEnvironmentPathFunctionName = "ocfp_etc_environment_path_order"

// etcEnvironmentPathAssignment is the awk pattern for a line that assigns
// PATH in the way pam_env reads it: optional leading whitespace, an optional
// export keyword, then PATH and an equals sign. Anything looser than the
// recognized forms still counts, so the transform can refuse it.
const etcEnvironmentPathAssignment = `/^[ \t]*(export[ \t]+)?PATH[ \t]*=/`

// etcEnvironmentPathFunction returns bash defining a function that takes the
// path of an /etc/environment style file, plus an optional name to use for
// it in warnings, and rewrites its PATH lines in place so each holds
// brewPrefix/bin, then brewPrefix/sbin, then its existing entries in their
// existing order with duplicates and empty entries dropped.
//
// Only these PATH lines are recognized: optional leading whitespace, an
// optional "export ", then PATH= and a value that is bare (no whitespace,
// quotes, or #), fully double-quoted, or fully single-quoted with no embedded
// quote of the same kind, then optional trailing whitespace or CR. No value
// may hold a # or a backslash, since pam_env treats those as a comment and a
// line continuation. Every recognized line is rewritten, keeping its leading
// whitespace, its export prefix, its quote style, and its trailing
// whitespace. If any line assigns PATH in another form, or a recognized line
// has no entries, the file is left exactly as it was and a warning names the
// line and the reason. A file in which no line assigns PATH gets one built
// from Ubuntu's default.
//
// The function does nothing when brewPrefix/bin is not a directory, and a
// second run leaves the file as the first run wrote it. ~/bin has no place
// here because pam_env does not expand $HOME in this file.
func etcEnvironmentPathFunction(brewPrefix string) string {
	return etcEnvironmentPathFunctionName + `() {
    local file="$1" label="${2:-$1}" ordered status=0
    [ -d ` + shellSingleQuote(brewPrefix+"/bin") + ` ] || return 0
    ordered=$(mktemp "${TMPDIR:-/tmp}/ocfp-environment-path.XXXXXX")
    LC_ALL=C awk -v bin=` + shellSingleQuote(brewPrefix+"/bin") + ` -v sbin=` + shellSingleQuote(brewPrefix+"/sbin") + ` \
        -v fallback=` + shellSingleQuote(ubuntuDefaultPath) + ` -v sq="'" '
        function order(value,    count, parts, i, out, seen) {
            out = bin ":" sbin
            seen[bin] = 1
            seen[sbin] = 1
            kept = 0
            count = split(value, parts, ":")
            for (i = 1; i <= count; i++) {
                if (parts[i] == "") continue
                kept++
                if (parts[i] in seen) continue
                seen[parts[i]] = 1
                out = out ":" parts[i]
            }
            return out
        }
        function reject(why) {
            reason = "line " NR ": " why
            bad = 1
            exit
        }
        { lines[NR] = $0 }
        ` + etcEnvironmentPathAssignment + ` {
            found = 1
            line = $0
            lead = ""
            prefix = ""
            trail = ""
            if (match(line, /^[ \t]+/)) {
                lead = substr(line, 1, RLENGTH)
                line = substr(line, RLENGTH + 1)
            }
            if (substr(line, 1, 7) == "export ") {
                prefix = "export "
                line = substr(line, 8)
            }
            if (substr(line, 1, 5) != "PATH=") reject("PATH assignment is not in a recognized form")
            line = substr(line, 6)
            if (match(line, /[ \t\r]+$/)) {
                trail = substr(line, RSTART)
                line = substr(line, 1, RSTART - 1)
            }
            if (index(line, "#")) reject("PATH value holds a # comment")
            if (index(line, "\\")) reject("PATH value holds a backslash")
            q = substr(line, 1, 1)
            if (q == "\"" || q == sq) {
                inner = substr(line, 2, length(line) - 2)
                if (length(line) < 2 || substr(line, length(line), 1) != q || index(inner, q))
                    reject("PATH value has unbalanced or embedded " (q == sq ? "single" : "double") " quotes")
            } else {
                q = ""
                inner = line
                if (index(inner, "\"") || index(inner, sq)) reject("unquoted PATH value holds a quote")
                if (index(inner, " ") || index(inner, "\t") || index(inner, "\r")) reject("unquoted PATH value holds whitespace")
            }
            value = order(inner)
            if (kept == 0) reject("PATH value has no entries")
            lines[NR] = lead prefix "PATH=" q value q trail
        }
        END {
            if (bad) {
                print reason
                exit 4
            }
            for (i = 1; i <= NR; i++) print lines[i]
            if (!found) print "PATH=\"" order(fallback) "\""
        }
    ' "$file" > "$ordered" || status=$?
    if [ "$status" -eq 0 ]; then
        cat "$ordered" > "$file"
    elif [ "$status" -eq 4 ]; then
        log_warning "Left $label unchanged: $(cat "$ordered")"
    else
        log_warning "Left $label unchanged: could not read its PATH lines"
    fi
    rm -f "$ordered"
}`
}

// etcEnvironmentPathCheck returns bash that logs whether the last line that
// assigns PATH in file, the one pam_env ends up using, starts with
// brewPrefix/bin and brewPrefix/sbin, in any of the quote styles the
// transform writes.
func etcEnvironmentPathCheck(brewPrefix, file string) string {
	return `if LC_ALL=C awk -v want=` + shellSingleQuote(brewPrefix+"/bin:"+brewPrefix+"/sbin") + ` -v sq="'" '
        ` + etcEnvironmentPathAssignment + ` { last = $0; found = 1 }
        END {
            if (!found) exit 1
            value = last
            sub(/^[^=]*=/, "", value)
            q = substr(value, 1, 1)
            if (q == "\"" || q == sq) value = substr(value, 2)
            else q = ""
            if (substr(value, 1, length(want)) != want) exit 1
            next_char = substr(value, length(want) + 1, 1)
            if (next_char == "" || next_char == ":" || next_char == " " || next_char == "\t" || next_char == "\r") exit 0
            if (q != "" && next_char == q) exit 0
            exit 1
        }
    ' ` + shellSingleQuote(file) + `; then
        log_success 'PATH puts Linuxbrew first in ` + file + `'
    else
        log_warning 'PATH does not put Linuxbrew first in ` + file + `'
    fi`
}
