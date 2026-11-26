package vault

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
)

// The bloc's first-run key lives in two leaves. The public half sits in
// the bloc's config tree, beside the values the kits already read, so a
// deployment's manifest can read it. The code sits under the bloc's
// operator tree, which only the admin policy reads.
const (
	// FirstRunLeaf is the name of both leaves.
	FirstRunLeaf = "first-run"
	// FirstRunPublicKey holds the public half in standard base64.
	FirstRunPublicKey = "public"
	// FirstRunURLKey holds the managing system's public address, which
	// its deployment records once it is up.
	FirstRunURLKey = "url"
	// FirstRunLinkKey holds the link prefix that the code is appended to,
	// which the deployment records beside the address.
	FirstRunLinkKey = "link"
	// FirstRunCodeKey holds the code, formatted in groups.
	FirstRunCodeKey = "code"
	// FirstRunShownKey records whether ocfp has shown the code to the
	// operator, as the text true or false.
	FirstRunShownKey = "shown"
	// operatorTreeSuffix names the bloc's operator tree.
	operatorTreeSuffix = "-operator"
)

// FirstRunCodeSentence is the one sentence printed with the code each
// time ocfp shows it.
const FirstRunCodeSentence = "This bloc's first-run code claims the first sign-in of a " +
	"managing system deployed into the bloc, so keep it somewhere safe."

var (
	// ErrFirstRunBlocInvalid refuses a bloc name that would not make a
	// single path segment.
	ErrFirstRunBlocInvalid = errors.New("the bloc name is not a single vault path segment")
	// ErrFirstRunNoVault refuses a call with no vault to read.
	ErrFirstRunNoVault = errors.New("no vault was given for the first-run key")
	// ErrFirstRunMarkShown reports that the code was shown but could not
	// be marked as shown, so ocfp may show it once more.
	ErrFirstRunMarkShown = errors.New("the first-run code was shown but not marked as shown")
)

// FirstRunVault is the part of the vault the first-run key needs. Safe
// implements it.
type FirstRunVault interface {
	GetAll(path string) (map[string]interface{}, error)
	SetMultiple(path string, data map[string]interface{}) error
}

// FirstRunPublicPath returns the leaf that holds the public half, the
// address, and the link prefix: secret/config/{bloc}/mgmt/first-run.
func FirstRunPublicPath(bloc string) string {
	return strings.Join([]string{SecretPrefix, ConfigPrefix, bloc, MgmtEnvType, FirstRunLeaf}, "/")
}

// FirstRunCodePath returns the leaf that holds the code and whether it
// was shown: secret/{bloc}-operator/first-run.
func FirstRunCodePath(bloc string) string {
	return strings.Join([]string{SecretPrefix, bloc + operatorTreeSuffix, FirstRunLeaf}, "/")
}

// FirstRunState is what the two leaves hold. Code is secret, and callers
// never log it.
type FirstRunState struct {
	// Code is the stored code, formatted in groups, or empty.
	Code string
	// Shown reports whether ocfp has shown the code.
	Shown bool
	// Public is the stored public half, or empty.
	Public string
	// Link is the link prefix the deployment recorded, or empty.
	Link string
	// URL is the address the deployment recorded, or empty.
	URL string
}

// FirstRunOutcome says what EnsureFirstRunKey found and wrote. Code is
// secret, and callers never log it.
type FirstRunOutcome struct {
	// WroteCode reports that a new code was made and written.
	WroteCode bool
	// WrotePublic reports that the public half was written.
	WrotePublic bool
	// Code is the bloc's code, formatted, when the bloc holds one.
	Code string
	// CodeMissing reports a public half with no code beside it. The code
	// can't be derived from the public half, so nothing was written, and
	// only a rotation gives the bloc a code again.
	CodeMissing bool
	// Mismatch reports a stored public half that is not the stored code's.
	// Neither leaf was changed.
	Mismatch bool
}

// checkFirstRunArgs refuses a missing vault or a bloc name that would not
// make one plain path segment.
func checkFirstRunArgs(store FirstRunVault, bloc string) error {
	if store == nil {
		return ErrFirstRunNoVault
	}

	if bloc == "" || bloc == "." || bloc == ".." {
		return ErrFirstRunBlocInvalid
	}

	for _, r := range bloc {
		if r <= ' ' || r == '/' || r == '*' || r == '+' || r == 0x7f {
			return ErrFirstRunBlocInvalid
		}
	}

	return nil
}

// readLeaf reads one leaf, giving an empty map for a leaf that holds
// nothing. Any other read error is returned, because an unreachable vault
// must never look like an empty one.
func readLeaf(store FirstRunVault, path string) (map[string]interface{}, error) {
	leaf, err := store.GetAll(path)
	if errors.Is(err, ErrSecretNotFound) {
		return map[string]interface{}{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if leaf == nil {
		return map[string]interface{}{}, nil
	}

	return leaf, nil
}

// leafString reads a key that should hold text. A missing key or an empty
// value reads as empty, and a value of another type is reported, because
// it is not something ocfp wrote.
func leafString(leaf map[string]interface{}, key string) (string, bool) {
	value, ok := leaf[key]
	if !ok || value == nil {
		return "", true
	}

	s, ok := value.(string)

	return s, ok
}

// leafBool reads a key that holds true or false as text or as a JSON
// boolean. Anything else reads as false.
func leafBool(leaf map[string]interface{}, key string) bool {
	switch v := leaf[key].(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(v)

		return err == nil && b
	default:
		return false
	}
}

// ReadFirstRunState reads both leaves. A stored code that isn't a code is
// refused with firstrun.CodeMalformed, without repeating it.
func ReadFirstRunState(store FirstRunVault, bloc string) (FirstRunState, error) {
	err := checkFirstRunArgs(store, bloc)
	if err != nil {
		return FirstRunState{}, err
	}

	codeLeaf, err := readLeaf(store, FirstRunCodePath(bloc))
	if err != nil {
		return FirstRunState{}, err
	}

	publicLeaf, err := readLeaf(store, FirstRunPublicPath(bloc))
	if err != nil {
		return FirstRunState{}, err
	}

	var state FirstRunState

	if _, present := codeLeaf[FirstRunCodeKey]; present {
		raw, ok := leafString(codeLeaf, FirstRunCodeKey)
		if !ok {
			return FirstRunState{}, &firstrun.Error{
				Code:   firstrun.CodeMalformed,
				Detail: "the bloc's stored first-run code is not text; rotate it",
			}
		}

		formatted, err := firstrun.Format(raw)
		if err != nil {
			return FirstRunState{}, &firstrun.Error{
				Code:   firstrun.CodeMalformed,
				Detail: "the bloc's stored first-run code is not a first-run code; rotate it",
			}
		}

		state.Code = formatted
	}

	state.Shown = leafBool(codeLeaf, FirstRunShownKey)
	state.Public, _ = leafString(publicLeaf, FirstRunPublicKey)
	state.Link, _ = leafString(publicLeaf, FirstRunLinkKey)
	state.URL, _ = leafString(publicLeaf, FirstRunURLKey)

	return state, nil
}

// EnsureFirstRunKey gives the bloc a first-run key when it lacks one. It
// writes only what is missing and never overwrites either leaf.
//
//   - With neither leaf, it makes a code and writes the code leaf, with
//     shown false, and then the public half. The code goes first, so a
//     failed public write is repaired by the next run.
//   - With a code and no public half, it writes the code's public half.
//   - With a public half and no code, it writes nothing and reports
//     CodeMissing, because a new code would not match the public half.
//   - With both, it writes nothing, and reports Mismatch when the public
//     half isn't the code's.
func EnsureFirstRunKey(store FirstRunVault, bloc string) (FirstRunOutcome, error) {
	state, err := ReadFirstRunState(store, bloc)
	if err != nil {
		return FirstRunOutcome{}, err
	}

	var out FirstRunOutcome

	switch {
	case state.Code == "" && state.Public != "":
		out.CodeMissing = true

		return out, nil
	case state.Code == "":
		raw, err := firstrun.Generate()
		if err != nil {
			return out, err
		}

		code, err := firstrun.Format(raw)
		if err != nil {
			return out, err
		}

		err = store.SetMultiple(FirstRunCodePath(bloc), map[string]interface{}{
			FirstRunCodeKey:  code,
			FirstRunShownKey: strconv.FormatBool(false),
		})
		if err != nil {
			return out, fmt.Errorf("writing the first-run code: %w", err)
		}

		out.WroteCode = true
		state.Code = code
	}

	out.Code = state.Code

	public, err := firstrun.PublicFor(state.Code)
	if err != nil {
		return out, err
	}

	if state.Public != "" {
		out.Mismatch = state.Public != public

		return out, nil
	}

	err = store.SetMultiple(FirstRunPublicPath(bloc), map[string]interface{}{FirstRunPublicKey: public})
	if err != nil {
		return out, fmt.Errorf("writing the first-run public half: %w", err)
	}

	out.WrotePublic = true

	return out, nil
}

// RotateFirstRunKey makes a new code and overwrites both leaves with it,
// the code leaf first. shown records whether the caller shows the new
// code. It returns the new code, formatted. The public leaf's address and
// link stay, because the managing system's address doesn't change.
func RotateFirstRunKey(store FirstRunVault, bloc string, shown bool) (string, error) {
	err := checkFirstRunArgs(store, bloc)
	if err != nil {
		return "", err
	}

	raw, err := firstrun.Generate()
	if err != nil {
		return "", err
	}

	code, err := firstrun.Format(raw)
	if err != nil {
		return "", err
	}

	public, err := firstrun.PublicFor(code)
	if err != nil {
		return "", err
	}

	err = store.SetMultiple(FirstRunCodePath(bloc), map[string]interface{}{
		FirstRunCodeKey:  code,
		FirstRunShownKey: strconv.FormatBool(shown),
	})
	if err != nil {
		return "", fmt.Errorf("writing the new first-run code: %w", err)
	}

	err = store.SetMultiple(FirstRunPublicPath(bloc), map[string]interface{}{FirstRunPublicKey: public})
	if err != nil {
		return "", fmt.Errorf("writing the new first-run public half: %w", err)
	}

	return code, nil
}

// PrintFirstRunCode writes the code with its sentence.
func PrintFirstRunCode(w io.Writer, code string) error {
	_, err := fmt.Fprintf(w, "\n%s\n\n    %s\n\n", FirstRunCodeSentence, code)
	if err != nil {
		return fmt.Errorf("printing the first-run code: %w", err)
	}

	return nil
}

// MarkFirstRunCodeShown records that ocfp showed the bloc's code.
func MarkFirstRunCodeShown(store FirstRunVault, bloc string) error {
	err := checkFirstRunArgs(store, bloc)
	if err != nil {
		return err
	}

	err = store.SetMultiple(FirstRunCodePath(bloc), map[string]interface{}{FirstRunShownKey: strconv.FormatBool(true)})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFirstRunMarkShown, err)
	}

	return nil
}

// ShowFirstRunCodeOnce prints the bloc's code with its sentence and marks
// it shown, when the bloc holds a code that ocfp hasn't shown yet and
// terminal reports that w is a terminal. It reports whether it printed
// the code. When the code was printed but couldn't be marked, it returns
// true with an error that wraps ErrFirstRunMarkShown.
func ShowFirstRunCodeOnce(w io.Writer, store FirstRunVault, bloc string, terminal bool) (bool, error) {
	if !terminal {
		return false, nil
	}

	state, err := ReadFirstRunState(store, bloc)
	if err != nil {
		return false, err
	}

	if state.Code == "" || state.Shown {
		return false, nil
	}

	err = PrintFirstRunCode(w, state.Code)
	if err != nil {
		return false, err
	}

	return true, MarkFirstRunCodeShown(store, bloc)
}

// OpenBlocFirstRunVault opens the bloc's own vault target, the one
// 'ocfp vault populate --bloc' writes to, and returns the function that
// closes it.
func OpenBlocFirstRunVault(bloc string) (FirstRunVault, func(), error) {
	client, err := NewClientForBloc(bloc)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the bloc's vault: %w", err)
	}

	return NewSafe(client), func() { _ = client.Close() }, nil
}
