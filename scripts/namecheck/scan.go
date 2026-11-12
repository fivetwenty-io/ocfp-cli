package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/unicode/norm"
)

// errTermsUnavailable marks every reason the term list cannot be used. Its
// text never carries a term.
var errTermsUnavailable = errors.New("CLIENT_TERMS_UNAVAILABLE")

type term struct {
	n    int // position among the list's term lines, counting from one
	text []rune
	part bool
}

// finding locates a match without quoting it.
type finding struct {
	line, col, term int
	// digest is the SHA-256 of the line in hex. It is never printed, and an
	// entry in the allow file matches on it.
	digest string
}

// allowFileName is the file, at the root of the scanned tree, that clears
// false positives.
const allowFileName = ".namecheck-allow"

var errAllowUnreadable = errors.New("NAMECHECK_ALLOW_UNREADABLE")

// allowList maps a path to the digests of the lines cleared in it.
type allowList map[string]map[string]bool

// loadAllow reads the allow file in dir, or in the current directory when dir
// is empty. A missing file is an empty list. Each line holds a path, the
// SHA-256 digest of the matching line in hex, and a reason, separated by
// white space, so a path that holds white space cannot be listed and the
// run stops instead. A blank line or one that starts with # is skipped. The error
// never quotes a line.
func loadAllow(dir string) (allowList, error) {
	data, err := os.ReadFile(filepath.Join(dir, allowFileName)) //nolint:gosec // the allow file sits at a fixed name in the tree the developer is checking
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, errAllowUnreadable
	}

	list := allowList{}

	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		f := strings.Fields(line)
		if len(f) < 3 || len(f[1]) != 64 {
			return nil, fmt.Errorf("%w: line %d needs a path, a digest, and a reason", errAllowUnreadable, i+1)
		}

		if _, err := hex.DecodeString(f[1]); err != nil {
			return nil, fmt.Errorf("%w: line %d has a digest that is not hex", errAllowUnreadable, i+1)
		}

		if list[f[0]] == nil {
			list[f[0]] = map[string]bool{}
		}

		list[f[0]][strings.ToLower(f[1])] = true
	}

	return list, nil
}

// termsPath returns where the term list lives for the given environment. It
// returns false when neither XDG_CONFIG_HOME nor HOME is set, so the program
// never falls back to a path relative to the current directory.
func termsPath(getenv func(string) string) (string, bool) {
	base := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(base) { // the XDG rule: a relative value counts as unset
		base = ""
	}

	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", false
		}

		base = filepath.Join(home, ".config")
	}

	return filepath.Join(base, "fivetwenty", "client-terms"), true
}

// loadTerms reads the list. It fails closed: a missing, unreadable, empty,
// or other-user-readable file, and a list with a line that folds to no
// letter or digit, all yield errTermsUnavailable.
func loadTerms(path string) ([]term, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errTermsUnavailable
	}

	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errTermsUnavailable
	}

	data, err := os.ReadFile(path) //nolint:gosec // the term list path comes from the environment of the developer running the check
	if err != nil {
		return nil, errTermsUnavailable
	}

	var terms []term

	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		part := false

		if f := strings.Fields(line); len(f) > 1 {
			flag := strings.ToLower(f[len(f)-1])
			if flag == "part" || flag == "word" {
				part = flag == "part"
				line = strings.TrimSpace(line[:len(line)-len(f[len(f)-1])])
			}
		}

		runes, _ := normalize(line)
		if !hasAlnum(runes) {
			return nil, errTermsUnavailable
		}

		terms = append(terms, term{n: len(terms) + 1, text: runes, part: part})
	}

	if len(terms) == 0 {
		return nil, errTermsUnavailable
	}

	return terms, nil
}

func hasAlnum(rs []rune) bool {
	for _, r := range rs {
		if isAlnum(r) {
			return true
		}
	}

	return false
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func isSep(r rune) bool { return r == '.' || r == '-' || r == '_' || r == ' ' }

// normalize folds the whole of s with NFKC, lowercases it, and drops
// separators that sit between two single letters. It returns the folded
// runes and, for each, the one-based column in s of the start of the input
// segment that produced it.
func normalize(s string) ([]rune, []int) {
	var (
		runes []rune
		cols  []int
		it    norm.Iter
	)
	it.InitString(norm.NFKC, s)

	for !it.Done() {
		start := it.Pos()
		col := utf8.RuneCountInString(s[:start]) + 1

		for _, r := range string(it.Next()) {
			runes = append(runes, unicode.ToLower(r))
			cols = append(cols, col)
		}
	}

	n := len(runes)
	keepR := runes[:0:0]
	keepC := cols[:0:0]

	for i, r := range runes {
		if isSep(r) && i > 0 && i+1 < n && isAlnum(runes[i-1]) && isAlnum(runes[i+1]) &&
			(i < 2 || !isAlnum(runes[i-2])) && (i+2 >= n || !isAlnum(runes[i+2])) {
			continue
		}

		keepR = append(keepR, r)
		keepC = append(keepC, cols[i])
	}

	return keepR, keepC
}

// scanLine reports each term at most once for the line, at its first match.
func scanLine(terms []term, text string) []finding {
	runes, cols := normalize(text)

	var out []finding

	for _, t := range terms {
		for i := 0; i+len(t.text) <= len(runes); i++ {
			if !runesEqual(runes[i:i+len(t.text)], t.text) {
				continue
			}

			if !t.part {
				if i > 0 && isAlnum(runes[i-1]) {
					continue
				}

				if j := i + len(t.text); j < len(runes) && isAlnum(runes[j]) {
					continue
				}
			}

			out = append(out, finding{col: cols[i], term: t.n})

			break
		}
	}

	sort.SliceStable(out, func(a, b int) bool { return out[a].col < out[b].col })

	return out
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// maskPath replaces every path component that holds a match of any term
// with ***, so that no output line can quote a term through a file name.
func maskPath(terms []term, path string) string {
	var b strings.Builder

	start := 0
	flush := func(end int) {
		comp := path[start:end]
		if len(scanLine(terms, comp)) > 0 {
			b.WriteString("***")
		} else {
			b.WriteString(comp)
		}
	}

	for i := range len(path) {
		if path[i] == '/' || path[i] == filepath.Separator {
			flush(i)
			b.WriteByte(path[i])
			start = i + 1
		}
	}

	flush(len(path))

	return b.String()
}

// scanContent scans a block of text and numbers its lines from firstLine.
func scanContent(terms []term, content string, firstLine int) []finding {
	var out []finding

	line := firstLine
	start := 0
	emit := func(end int) {
		text := strings.TrimSuffix(content[start:end], "\r")
		for _, f := range scanLine(terms, text) {
			f.line = line
			d := digestOf([]byte(text))
			f.digest = hex.EncodeToString(d[:])
			out = append(out, f)
		}

		line++
	}

	for i := range len(content) {
		if content[i] == '\n' || content[i] == 0 {
			emit(i)
			start = i + 1
		}
	}

	emit(len(content))

	return out
}

type scanner struct {
	terms  []term
	dir    string // working directory for git, empty for the process's own
	out    func(string)
	failed bool
	allow  allowList
	// seen, when set, drops a line already printed for the current commit,
	// because a merge is diffed against each parent in turn.
	seen map[string]bool
}

func (s *scanner) emit(line string) {
	if s.seen != nil {
		if s.seen[line] {
			return
		}

		s.seen[line] = true
	}

	s.out(line)
}

func (s *scanner) report(path string, fs []finding) {
	masked := maskPath(s.terms, path)
	for _, f := range fs {
		if s.allow[path][f.digest] {
			continue
		}

		s.failed = true
		s.emit(fmt.Sprintf("CLIENT_NAME_FOUND %s:%d:%d term %d", masked, f.line, f.col, f.term))
	}
}

func (s *scanner) reportCommit(hash string, fs []finding) {
	for _, f := range fs {
		s.failed = true
		s.emit(fmt.Sprintf("CLIENT_NAME_FOUND commit %s message term %d", hash, f.term))
	}
}

// scanName scans a path's own text. The finding prints the path masked.
func (s *scanner) scanName(path string) {
	masked := maskPath(s.terms, path)
	for _, f := range scanLine(s.terms, path) {
		s.failed = true
		s.emit(fmt.Sprintf("CLIENT_NAME_FOUND path %s term %d", masked, f.term))
	}
}

func (s *scanner) scanBytes(path string, b []byte) {
	s.report(path, scanContent(s.terms, string(b), 1))
}

// pathErr rewrites an error so that it quotes the masked path only.
func (s *scanner) pathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s %s: %w", pe.Op, maskPath(s.terms, pe.Path), pe.Err)
	}

	return err
}

func (s *scanner) scanPaths(paths []string) error {
	if len(paths) == 0 {
		paths = []string{"."}
	}

	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return s.pathErr(err)
			}

			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}

				return nil
			}

			if d.Type()&fs.ModeSymlink != 0 {
				// Scan the link's name and its target text. Never follow it.
				s.scanName(p)

				target, err := os.Readlink(p)
				if err != nil {
					return s.pathErr(err)
				}

				s.scanBytes(p, []byte(target))

				return nil
			}

			if !d.Type().IsRegular() {
				return nil
			}

			s.scanName(p)

			b, err := os.ReadFile(p) //nolint:gosec // the path comes from walking a root the developer named on the command line
			if err != nil {
				return s.pathErr(err)
			}

			s.scanBytes(p, b)

			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// git runs a git command. On failure the error names only the subcommand
// and its exit status, because git's own message can quote a path or a
// revision argument that holds a term.
func (s *scanner) git(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"--no-replace-objects"}, args...)...) //nolint:gosec // the arguments are git revisions and paths from the developer's own flags and from git
	cmd.Dir = s.dir

	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("git %s failed with exit status %d", args[0], ee.ExitCode())
		}

		return nil, fmt.Errorf("git %s failed", args[0])
	}

	return out, nil
}

func (s *scanner) scanStaged(paths []string) error {
	args := []string{"diff", "--cached", "--ignore-submodules=none", "--raw", "-z", "--no-renames", "--diff-filter=ACMRT"}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}

	raw, err := s.git(args...)
	if err != nil {
		return err
	}
	// The output alternates a ":oldmode newmode oldsha newsha status"
	// record with the NUL-terminated name it describes.
	toks := strings.Split(string(raw), "\x00")
	for i := 0; i+1 < len(toks); i += 2 {
		meta := strings.Fields(toks[i])

		name := toks[i+1]
		if len(meta) < 2 || name == "" {
			return errors.New("unreadable index listing")
		}

		s.scanName(name)

		if meta[1] == "160000" {
			continue // a submodule entry holds a commit id and no content
		}

		b, err := s.git("cat-file", "blob", ":0:"+name)
		if err != nil {
			return err
		}

		s.scanBytes(name, b)
	}

	return nil
}

func (s *scanner) scanMessageFile(path string) error {
	b, err := os.ReadFile(path) //nolint:gosec // the path is the message file the developer named on the command line
	if err != nil {
		return s.pathErr(err)
	}

	s.scanBytes(path, b)

	return nil
}

// scanLog scans every commit that git rev-list reports for the revision
// arguments. Each commit object, each list of changed names, and each patch
// comes from its own git command, so no text inside a commit can pose as the
// start of the next record.
func (s *scanner) scanLog(revs ...string) error {
	listing, err := s.git(append([]string{"rev-list", "--reverse", "--end-of-options"}, revs...)...)
	if err != nil {
		return err
	}

	for _, hash := range strings.Fields(string(listing)) {
		s.seen = map[string]bool{}

		obj, err := s.git("cat-file", "commit", hash)
		if err != nil {
			return err
		}

		text, unreadable := commitText(string(obj))
		s.reportCommit(hash, scanContent(s.terms, text, 1))

		for _, why := range unreadable {
			s.failed = true
			s.emit(fmt.Sprintf("CLIENT_NAME_UNREADABLE commit %s encoding %s", hash, why))
		}

		nameList, err := s.git("diff-tree", "--ignore-submodules=none", "-r", "-z", "--name-only", "-m", "--root", "--no-commit-id", "-M", hash)
		if err != nil {
			return err
		}

		names := map[string]bool{}
		for _, n := range strings.Split(string(nameList), "\x00") {
			if n != "" && !names[n] {
				names[n] = true
				s.scanName(n)
			}
		}

		diff, err := s.git("diff-tree", "--ignore-submodules=none", "-p", "--text", "--no-ext-diff", "--no-textconv",
			"--src-prefix=a/", "--dst-prefix=b/",
			"-m", "--root", "--no-commit-id", "-M", "-U0", "--no-color", hash)
		if err != nil {
			return err
		}

		if err := s.scanDiff(string(diff), names); err != nil {
			return err
		}
	}

	s.seen = nil

	return nil
}

// maxTagChain bounds how many tags a chain of tags is followed through.
const maxTagChain = 32

// scanTag scans the tagger and message of the annotated tag that sha names,
// and of every tag it points at in turn. A finding names the pushed ref
// masked. An object that is not a tag has no text of its own to scan. A
// chain of more than maxTagChain tags is an error, because its later tags
// would go unscanned.
func (s *scanner) scanTag(ref, sha string) error {
	masked := maskPath(s.terms, ref)

	s.seen = map[string]bool{}
	defer func() { s.seen = nil }()

	for n := 0; ; n++ {
		kind, err := s.git("cat-file", "-t", sha)
		if err != nil {
			return err
		}

		if strings.TrimSpace(string(kind)) != "tag" {
			return nil
		}

		if n == maxTagChain {
			return fmt.Errorf("a chain of tags is longer than %d, so it was not scanned", maxTagChain)
		}

		obj, err := s.git("cat-file", "tag", sha)
		if err != nil {
			return err
		}

		for _, f := range scanContent(s.terms, tagText(string(obj)), 1) {
			s.failed = true
			s.emit(fmt.Sprintf("CLIENT_NAME_FOUND tag %s message term %d", masked, f.term))
		}

		next := ""

		head, _, _ := strings.Cut(string(obj), "\n\n")
		for _, l := range strings.Split(head, "\n") {
			if rest, ok := strings.CutPrefix(l, "object "); ok {
				next = strings.TrimSpace(rest)

				break
			}
		}

		if next == "" {
			return nil
		}

		sha = next
	}
}

// tagText keeps the parts of a raw tag object that name a person, name the
// tag, and carry the message. It skips the object and type lines, where a
// short term could match inside a hash. It keeps the whole body, signature
// block included. A block cannot be told from message text that imitates one,
// so dropping it would let a person hide a term inside a fake block. A
// signature that happens to spell a term is refused, never let through.
func tagText(obj string) string {
	head, body, _ := strings.Cut(obj, "\n\n")

	var keep []string

	for _, l := range strings.Split(head, "\n") {
		if rest, ok := strings.CutPrefix(l, "tag "); ok {
			keep = append(keep, rest)
		}

		if rest, ok := strings.CutPrefix(l, "tagger "); ok {
			if i := strings.LastIndex(rest, ">"); i >= 0 {
				rest = rest[:i+1]
			}

			keep = append(keep, rest)
		}
	}

	return strings.Join(keep, "\n") + "\n" + body
}

// commitText keeps the parts of a raw commit object that name people and
// carry the message. It skips the tree, parent, and signature lines, where a
// short term could match inside a hash. It also keeps the tag inside each
// mergetag header, which holds a tagger and a message of its own. A commit
// that declares a message encoding other than UTF-8 is read twice, once as
// the raw bytes and once decoded from the declared encoding, so a term spelled
// with bytes that are invalid in UTF-8 cannot hide. Only decoders that cannot
// lose bytes are trusted, which are UTF-8, US-ASCII, the ISO-8859 family, and
// the windows-125x family. Any other declared encoding, a decode that yields a
// replacement character, and invalid UTF-8 under a UTF-8 or absent header are
// each returned as a reason that the message cannot be read, because it may
// hold a term the raw bytes do not show. A reason never carries any of the
// header text, which is free and could hold a term. The caller treats each
// reason as a failure.
func commitText(obj string) (string, []string) {
	head, body, _ := strings.Cut(obj, "\n\n")

	var (
		keep, encodings []string
		tag             []string
		inTag           bool
	)

	flushTag := func() {
		if inTag {
			keep = append(keep, tagText(strings.Join(tag, "\n")))
		}

		inTag, tag = false, nil
	}

	for _, l := range strings.Split(head, "\n") {
		if rest, ok := strings.CutPrefix(l, " "); ok && inTag {
			tag = append(tag, rest)

			continue
		}

		flushTag()

		if rest, ok := strings.CutPrefix(l, "mergetag "); ok {
			inTag, tag = true, []string{rest}
		}

		if rest, ok := strings.CutPrefix(l, "encoding "); ok {
			encodings = append(encodings, rest)
		}

		if strings.HasPrefix(l, "author ") || strings.HasPrefix(l, "committer ") {
			// Keep the name and the mail address only. The keyword before
			// them and the timestamp and zone after them are left out.
			_, rest, _ := strings.Cut(l, " ")
			if i := strings.LastIndex(rest, ">"); i >= 0 {
				rest = rest[:i+1]
			}

			keep = append(keep, rest)
		}
	}

	flushTag()

	text := strings.Join(keep, "\n") + "\n" + body

	var (
		out        strings.Builder
		unreadable []string
	)

	out.WriteString(text)

	if len(encodings) == 0 && !utf8.ValidString(text) {
		unreadable = append(unreadable, "<absent>")
	}

	for _, header := range encodings {
		dec, ok := decodeAs(strings.TrimSpace(header), text)
		if !ok {
			unreadable = append(unreadable, fmt.Sprintf("<withheld, %d bytes>", len(header)))

			continue
		}

		if dec != text {
			out.WriteString("\n")
			out.WriteString(dec)
		}
	}

	return out.String(), unreadable
}

// decodeAs turns text from the named character encoding into UTF-8. It
// reports false when the name is not one it knows, when its decoder is not on
// the allowlist, or when the decode could have lost bytes.
func decodeAs(name, text string) (string, bool) {
	enc := lookupEncoding(name)
	if enc == nil {
		return "", false
	}

	canon, err := ianaindex.IANA.Name(enc)
	if err != nil || !safeEncoding.MatchString(strings.ToLower(canon)) {
		return "", false
	}

	dec, err := enc.NewDecoder().String(text)
	if err != nil {
		return "", false
	}

	if strings.EqualFold(canon, "utf-8") {
		return dec, utf8.ValidString(text)
	}

	return dec, !strings.ContainsRune(dec, utf8.RuneError)
}

// safeEncoding matches the IANA names of the decoders that cannot lose bytes.
var safeEncoding = regexp.MustCompile(`^(utf-8|us-ascii|windows-125[0-9]|iso[-_]8859-[0-9]+(:[0-9]{4})?)$`)

// lookupEncoding finds an encoding by its IANA or HTML name, also with
// underscores written as hyphens, and returns nil when neither index knows it.
func lookupEncoding(name string) encoding.Encoding {
	for _, n := range []string{name, strings.ReplaceAll(name, "_", "-")} {
		if enc, err := ianaindex.IANA.Encoding(n); err == nil && enc != nil {
			return enc
		}

		if enc, err := htmlindex.Get(n); err == nil && enc != nil {
			return enc
		}
	}

	return nil
}

// unquoteName turns a name from a patch header back into the real name.
// Git appends one TAB to a header name that holds whitespace, after the
// closing quote when the name is quoted. It writes a name that holds a
// control character, a double quote, a backslash, or a byte above 0x7f as a
// C-quoted string. The one TAB is removed before the unquote, so a name that
// itself ends in a TAB (always quoted, so written \t) keeps it. The leading
// b/ is removed last.
func unquoteName(h string) (string, error) {
	h = strings.TrimSuffix(h, "\t")
	if strings.HasPrefix(h, `"`) {
		u, err := strconv.Unquote(h)
		if err != nil {
			return "", errors.New("unreadable quoted name in patch header")
		}

		h = u
	}

	if h == "/dev/null" {
		return h, nil
	}

	return strings.TrimPrefix(h, "b/"), nil
}

// scanDiff scans the added lines of one commit's patch. The names of the
// changed files come from names, which git lists without quoting. A "+++ "
// header counts only before a section's first hunk and says which file the
// following added lines belong to, and a name that is not in names stops the
// run, because the two readings of the patch then disagree. After the first
// hunk, every line that starts with a plus sign is added text, including one
// that begins with "++ ".
func (s *scanner) scanDiff(diff string, names map[string]bool) error {
	var path string

	inHunk := false
	line := 0

	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inHunk = false
			path = ""
		case !inHunk && strings.HasPrefix(l, "+++ "):
			p, err := unquoteName(l[4:])
			if err != nil {
				return err
			}

			if p == "/dev/null" {
				path = ""

				continue
			}

			if !names[p] {
				return errors.New("patch header disagrees with the list of changed names")
			}

			path = p
		case strings.HasPrefix(l, "@@"):
			inHunk = true
			line = newStart(l)
		case inHunk && strings.HasPrefix(l, "+") && path != "":
			for _, f := range scanContent(s.terms, strings.TrimSuffix(l[1:], "\r"), line) {
				f.line = line
				s.report(path, []finding{f})
			}

			line++
		}
	}

	return nil
}

// newStart reads the new-file start line from a hunk header.
func newStart(h string) int {
	i := strings.Index(h, "+")
	if i < 0 {
		return 0
	}

	rest := h[i+1:]

	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return 0
	}

	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}

	return n
}
