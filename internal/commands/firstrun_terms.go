package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
)

// Refusal codes of 'ocfp first-run accept-terms' that the command makes
// itself. A refusal from the core keeps the core's own code.
const (
	codeFirstRunCodeSource     = "FIRST_RUN_CODE_SOURCE_INVALID"
	codeFirstRunCodeUnreadable = "FIRST_RUN_CODE_UNREADABLE"
	codeFirstRunAddress        = "FIRST_RUN_ADDRESS_INVALID"
	codeFirstRunCoreUnreached  = "FIRST_RUN_CORE_UNREACHABLE"
	codeFirstRunCoreAnswer     = "FIRST_RUN_CORE_ANSWER_INVALID"
	codeFirstRunServerChanged  = "FIRST_RUN_SERVER_KEY_CHANGED"
	codeFirstRunTermsNotWait   = "FIRST_RUN_TERMS_NOT_WAITING"
	codeFirstRunTermsInvalid   = "FIRST_RUN_TERMS_INVALID"
	codeFirstRunCAInvalid      = "FIRST_RUN_CA_INVALID"
)

// maxTermsCode bounds the text read for the code. A code is 24 characters,
// and anything longer than a few of them is never a code.
const maxTermsCode = 1 << 10

// termsCodeShape is the shape of a refusal code the command will name
// when it hasn't a sentence for the code.
var termsCodeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// acceptTermsOptions is what the command's flags say.
type acceptTermsOptions struct {
	// bloc names the bloc whose vault holds the code and the core's
	// address, when the code isn't read from standard input.
	bloc string
	// address is the core's https address. When empty, the address the
	// bloc's deploy recorded stands in.
	address string
	// codeSource is "-" to read the code from standard input, and empty to
	// read it from the bloc's vault.
	codeSource string
	// caFile names the CA certificates to trust for the core. When empty,
	// the system roots stand in.
	caFile string
	// assumeYes skips the question and accepts the terms.
	assumeYes bool
}

// acceptTermsDeps is what the command reaches outside itself. The
// first-run dependencies supply the vault, input, and output. Tests swap
// each one for a fake.
type acceptTermsDeps struct {
	firstRunDeps

	// stdinTerminal reports whether standard input is a terminal.
	stdinTerminal func() bool
	// readHidden reads one line typed at that terminal, with its echo off.
	readHidden func() ([]byte, error)
}

func liveAcceptTermsDeps(cmd *cobra.Command) acceptTermsDeps {
	return acceptTermsDeps{
		firstRunDeps:  liveFirstRunDeps(cmd),
		stdinTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },              // #nosec G115 -- a file descriptor fits in int
		readHidden:    func() ([]byte, error) { return term.ReadPassword(int(os.Stdin.Fd())) }, // #nosec G115 -- a file descriptor fits in int
	}
}

func newFirstRunAcceptTermsCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "accept-terms",
		Short: "Accept the ACME directory's terms of service for the managing system",
		Long: `Accept the ACME directory's terms of service for the managing system.

When the managing system orders its certificate from an ACME directory
whose terms changed, it waits for a person to accept the new terms. This
command reads the terms link from the managing system, shows it, asks you
to accept it, and then signs the acceptance with the bloc's first-run key.

The signature names the managing system's address and the key of the
certificate it presents, so the acceptance can't be replayed anywhere
else. The command never prints the first-run code, the key, or the
signature.

By default the command reads the code and the managing system's address
from the bloc's vault. With --code - it reads the code from standard
input instead, and then --url must name the address. Skip the question
with --yes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := acceptTermsOptionsOf(cmd)
			if err != nil {
				return err
			}

			return runFirstRunAcceptTerms(cmd.Context(), liveAcceptTermsDeps(cmd), opts)
		},
	}

	addFirstRunBlocFlag(cmd)
	cmd.Flags().String("url", "", "The managing system's https address (default: the address its deploy recorded)")
	cmd.Flags().String("code", "", `"-" reads the first-run code from standard input (default: the bloc's vault)`)
	cmd.Flags().String("ca", "", "CA certificates to trust for the managing system (default: the system roots)")
	cmd.Flags().BoolP("yes", "y", false, "Accept the terms without asking")

	return cmd
}

func acceptTermsOptionsOf(cmd *cobra.Command) (acceptTermsOptions, error) {
	var opts acceptTermsOptions

	opts.bloc = firstRunBloc(cmd)

	for name, dst := range map[string]*string{"url": &opts.address, "code": &opts.codeSource, "ca": &opts.caFile} {
		value, err := cmd.Flags().GetString(name)
		if err != nil {
			return opts, fmt.Errorf("reading --%s: %w", name, err)
		}

		*dst = value
	}

	yes, err := cmd.Flags().GetBool("yes")
	if err != nil {
		return opts, fmt.Errorf("reading --yes: %w", err)
	}

	opts.assumeYes = yes

	return opts, nil
}

// runFirstRunAcceptTerms reads the waiting terms from the core, asks the
// operator to accept them, and sends the acceptance signed by the bloc's
// first-run key. Nothing but the status read leaves this host before the
// operator accepts.
func runFirstRunAcceptTerms(ctx context.Context, deps acceptTermsDeps, opts acceptTermsOptions) error {
	code, address, err := termsCodeAndAddress(deps, opts)
	defer firstrun.Wipe(code[:cap(code)])

	if err != nil {
		return err
	}

	base, err := normalizeCoreURL(address)
	if err != nil {
		return &firstrun.Error{Code: codeFirstRunAddress, Detail: err.Error()}
	}

	_, priv, err := firstrun.KeyPairBytes(code)
	if err != nil {
		return err
	}

	defer firstrun.Wipe(priv)

	httpClient, err := newTermsHTTPClient(base, opts.caFile)
	if err != nil {
		return &firstrun.Error{Code: codeFirstRunCAInvalid, Detail: "the CA file can't be used: " + safeTermsText(err.Error())}
	}

	defer httpClient.CloseIdleConnections()

	core := &termsCore{base: base, http: httpClient}

	terms, err := core.waitingTerms(ctx)
	if err != nil {
		return err
	}

	err = confirmTerms(deps, opts, terms)
	if err != nil {
		return err
	}

	challenge, mac, serverKey, err := core.challenge(ctx)
	if err != nil {
		return err
	}

	statement, err := termsStatement(challenge, terms, termsBinding{Audience: base, ServerKey: serverKey})
	if err != nil {
		return &firstrun.Error{
			Code:   codeFirstRunCoreAnswer,
			Detail: "the core sent a challenge this command can't sign, so nothing was sent; check that the address is the core's",
		}
	}

	return core.accept(ctx, deps.out, acceptRequest{
		Statement: termsStatementLabel,
		Challenge: challenge,
		MAC:       mac,
		Audience:  base,
		ServerKey: serverKey,
		Terms:     terms,
		Signature: ed25519.Sign(priv, statement),
	})
}

// termsCodeAndAddress reads the first-run code and the core's address
// from where the options say. The caller wipes the code's buffer.
func termsCodeAndAddress(deps acceptTermsDeps, opts acceptTermsOptions) ([]byte, string, error) {
	switch opts.codeSource {
	case "-":
		if opts.address == "" {
			return nil, "", &firstrun.Error{
				Code:   codeFirstRunAddress,
				Detail: "--code - needs --url, because the code on standard input names no managing system",
			}
		}

		code, err := readTermsCode(deps)

		return code, opts.address, err
	case "":
		return termsCodeFromVault(deps, opts)
	default:
		return nil, "", &firstrun.Error{
			Code:   codeFirstRunCodeSource,
			Detail: `--code takes only "-", which reads the code from standard input; a code never goes on the command line`,
		}
	}
}

// termsCodeFromVault reads the code, and the address when none was given,
// from the bloc's vault.
func termsCodeFromVault(deps acceptTermsDeps, opts acceptTermsOptions) ([]byte, string, error) {
	if opts.bloc == "" {
		return nil, "", ErrBlocFlagOrEnvVarRequired
	}

	state, err := readFirstRunCode(deps.firstRunDeps, opts.bloc)
	if err != nil {
		return nil, "", err
	}

	code := []byte(state.Code)
	address := opts.address

	if address != "" {
		return code, address, nil
	}

	address, addrErr := firstRunAddress(state.URL)
	if addrErr != nil {
		return code, "", &firstrun.Error{
			Code: codeFirstRunNotDeployed,
			Detail: "the bloc has no recorded address for its managing system; " +
				"name it with --url, or deploy the managing system first",
		}
	}

	return code, address, nil
}

// readTermsCode reads the code from standard input. Piped input is read
// whole, and a code typed at a terminal is read as one line with its echo
// off. The text lives in one buffer the caller wipes, and it never reaches
// a string or an error.
func readTermsCode(deps acceptTermsDeps) ([]byte, error) {
	var (
		text []byte
		err  error
	)

	switch {
	case deps.stdinTerminal != nil && deps.stdinTerminal():
		if deps.readHidden == nil {
			return nil, &firstrun.Error{
				Code:   codeFirstRunCodeUnreadable,
				Detail: "this platform can't turn off a terminal's echo, so pipe the first-run code in",
			}
		}

		_, err = fmt.Fprint(deps.out, "First-run code (it does not show as you type): ")
		if err != nil {
			return nil, fmt.Errorf("printing the prompt: %w", err)
		}

		text, err = deps.readHidden()

		_, _ = fmt.Fprintln(deps.out)
	default:
		text, err = readFixedBytes(deps.in, maxTermsCode+1)
	}

	if err != nil {
		firstrun.Wipe(text[:cap(text)])

		return nil, &firstrun.Error{Code: codeFirstRunCodeUnreadable, Detail: "the first-run code couldn't be read"}
	}

	if len(text) > maxTermsCode {
		firstrun.Wipe(text[:cap(text)])

		return nil, &firstrun.Error{Code: firstrun.CodeMalformed, Detail: "the input is longer than a first-run code"}
	}

	trimmed := bytes.TrimSpace(text)
	if len(trimmed) == 0 {
		firstrun.Wipe(text[:cap(text)])

		return nil, &firstrun.Error{Code: firstrun.CodeMalformed, Detail: "the input holds no first-run code"}
	}

	return trimmed, nil
}

// readFixedBytes reads r to its end, or until it has read size bytes, into
// one buffer of size bytes that never grows, so no outgrown copy of the
// input stays behind. On a failed read it wipes what it had read.
func readFixedBytes(r io.Reader, size int) ([]byte, error) {
	buf := make([]byte, size)
	n := 0

	for n < size {
		m, err := r.Read(buf[n:])
		n += m

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			firstrun.Wipe(buf)

			return buf[:0], err
		}
	}

	return buf[:n], nil
}

// confirmTerms shows the terms link and asks the operator to accept it,
// unless they passed --yes. Accepting legal terms is a person's decision,
// so end of input declines.
func confirmTerms(deps acceptTermsDeps, opts acceptTermsOptions, terms string) error {
	_, err := fmt.Fprintf(deps.out, "The ACME directory's terms of service are at %s\n", terms)
	if err != nil {
		return fmt.Errorf("printing the terms link: %w", err)
	}

	if opts.assumeYes {
		return nil
	}

	_, err = fmt.Fprint(deps.out, "Read them, and accept them for this managing system? [y/N]: ")
	if err != nil {
		return fmt.Errorf("printing the question: %w", err)
	}

	answer, err := readAnswerLine(deps.in)
	if err != nil {
		return err
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}

	detail := "the terms were not accepted, and nothing was sent"
	if opts.codeSource == "-" {
		detail += "; the code came from standard input, so pass --yes to accept without a question"
	}

	return &firstrun.Error{Code: codeFirstRunNotConfirmed, Detail: detail}
}

// readAnswerLine reads one line, a byte at a time so it takes nothing
// that follows it. End of input gives what was read.
func readAnswerLine(in io.Reader) (string, error) {
	var line []byte

	one := make([]byte, 1)

	for len(line) < maxTermsCode {
		n, err := in.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}

			line = append(line, one[0])
		}

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return "", fmt.Errorf("reading the answer: %w", err)
		}
	}

	return string(line), nil
}

// acceptRequest is the body of the acceptance. Each []byte travels as
// standard base64 with padding, which is how encoding/json writes it.
type acceptRequest struct {
	Statement string `json:"statement"`
	Challenge []byte `json:"challenge"`
	MAC       []byte `json:"mac"`
	Audience  string `json:"audience"`
	ServerKey []byte `json:"server_key"`
	Terms     string `json:"terms"`
	Signature []byte `json:"signature"`
}

// termsCore talks to one core.
type termsCore struct {
	base string
	http *http.Client
}

// termsAnswer is one answer from the core.
type termsAnswer struct {
	status int
	body   []byte
	tls    *tls.ConnectionState
}

// call makes one request and reads a bounded answer. A server key that
// changed since the run's first connection fails before any request is
// sent, and call says so.
func (c *termsCore) call(ctx context.Context, method, path string, payload []byte) (termsAnswer, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return termsAnswer{}, &firstrun.Error{Code: codeFirstRunCoreUnreached, Detail: safeTermsText(err.Error())}
	}

	req.Header.Set("Accept", "application/json")

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return termsAnswer{}, termsTransportError(err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTermsAnswer))
	if err != nil {
		return termsAnswer{}, &firstrun.Error{Code: codeFirstRunCoreAnswer, Detail: "the core's answer was cut off"}
	}

	return termsAnswer{status: resp.StatusCode, body: data, tls: resp.TLS}, nil
}

// termsTransportError describes a request that got no answer.
func termsTransportError(err error) error {
	if errors.Is(err, errTermsServerChanged) {
		return &firstrun.Error{
			Code: codeFirstRunServerChanged,
			Detail: "the address presented another server key than on its first connection, so nothing more was sent; " +
				"check that nothing between here and the core answers in its place",
		}
	}

	return &firstrun.Error{
		Code:   codeFirstRunCoreUnreached,
		Detail: "the core could not be reached or verified: " + safeTermsText(err.Error()),
	}
}

// waitingTerms reads the terms the core waits for a person to accept. It
// refuses when none wait, or when the link isn't one this command will
// show.
func (c *termsCore) waitingTerms(ctx context.Context) (string, error) {
	answer, err := c.call(ctx, http.MethodGet, termsPathStatus, nil)
	if err != nil {
		return "", err
	}

	if answer.status != http.StatusOK {
		return "", termsRefusal(answer, "reading the setup status")
	}

	var status struct {
		TLSReason string `json:"tlsReason"`
		TLSTerms  string `json:"tlsTerms"`
	}

	err = json.Unmarshal(answer.body, &status)
	if err != nil {
		return "", &firstrun.Error{Code: codeFirstRunCoreAnswer, Detail: "the core's setup status can't be read"}
	}

	if status.TLSReason != termsWaitingReason {
		return "", &firstrun.Error{
			Code:   codeFirstRunTermsNotWait,
			Detail: "the core isn't waiting for anyone to accept terms, so there is nothing to accept",
		}
	}

	if checkTermsURL(status.TLSTerms) != nil {
		return "", &firstrun.Error{
			Code:   codeFirstRunTermsInvalid,
			Detail: "the core named terms that aren't an https link this command will show, so nothing was sent",
		}
	}

	return status.TLSTerms, nil
}

// challenge fetches a challenge, and the server key of the certificate the
// core presented on the connection that brought it.
func (c *termsCore) challenge(ctx context.Context) ([]byte, []byte, []byte, error) {
	answer, err := c.call(ctx, http.MethodGet, termsPathChallenge, nil)
	if err != nil {
		return nil, nil, nil, err
	}

	if answer.status != http.StatusOK {
		return nil, nil, nil, termsRefusal(answer, "asking for a challenge")
	}

	var reply struct {
		Challenge []byte `json:"challenge"`
		MAC       []byte `json:"mac"`
	}

	err = json.Unmarshal(answer.body, &reply)
	if err != nil || len(reply.Challenge) == 0 || len(reply.Challenge) > maxChallenge || len(reply.MAC) != macSize {
		return nil, nil, nil, &firstrun.Error{
			Code:   codeFirstRunCoreAnswer,
			Detail: "the core's challenge can't be read, so nothing was sent",
		}
	}

	serverKey, err := serverKeyOfConn(answer.tls)
	if err != nil {
		return nil, nil, nil, &firstrun.Error{
			Code:   codeFirstRunCoreAnswer,
			Detail: "the core's connection named no server certificate, so nothing was sent",
		}
	}

	return reply.Challenge, reply.MAC, serverKey, nil
}

// accept sends the acceptance and reports the core's answer. Once the
// request is sent, an answer that is neither an acceptance nor a refusal
// may hide an acceptance that landed, so the error says how to check.
func (c *termsCore) accept(ctx context.Context, out io.Writer, req acceptRequest) error {
	payload, err := json.Marshal(req)
	if err != nil || len(payload) >= maxTermsBody {
		return &firstrun.Error{Code: codeFirstRunCoreAnswer, Detail: "the acceptance is too large for the core to read, so nothing was sent"}
	}

	answer, err := c.call(ctx, http.MethodPost, termsPathClaim, payload)
	if err != nil {
		if firstrun.CodeOf(err) == codeFirstRunServerChanged {
			return err
		}

		return withLostAnswer(err)
	}

	if answer.status != http.StatusOK {
		return termsRefusal(answer, "sending the acceptance")
	}

	var reply struct {
		Code  string `json:"code"`
		Terms string `json:"terms"`
	}

	err = json.Unmarshal(answer.body, &reply)
	if err != nil || reply.Code != termsAcceptedCode || reply.Terms != req.Terms {
		return withLostAnswer(&firstrun.Error{Code: codeFirstRunCoreAnswer, Detail: "the core's answer to the acceptance can't be read"})
	}

	_, err = fmt.Fprintf(out, "The core accepted the terms at %s, and its certificate order runs now.\n", req.Terms)
	if err != nil {
		return fmt.Errorf("printing the result: %w", err)
	}

	return nil
}

// withLostAnswer adds to an error the advice for an acceptance whose
// answer never arrived.
func withLostAnswer(err error) error {
	var coded *firstrun.Error
	if !errors.As(err, &coded) {
		return err
	}

	return &firstrun.Error{
		Code: coded.Code,
		Detail: coded.Detail + "; the core may have taken the acceptance, so run 'ocfp first-run accept-terms' again, " +
			"and if it says no terms are waiting, the acceptance landed",
	}
}

// termsReasonSentences say, for each reason of SETUP_CLAIM_REFUSED, what
// it means for the operator. A reason that isn't here is never shown.
var termsReasonSentences = map[string]string{
	"mac": "the core did not recognize its own challenge, so run the command again, " +
		"and check that every core behind the address shares one secrets store",
	"time":       "the request reached the core more than a minute after its challenge, so run the command again",
	"server-key": "the core serves another certificate than the one the deployment names for its public address, so check that the deployment names the certificate that address presents",
	"signature":  "the core did not accept the signature, so check that the first-run code is this deployment's, as 'ocfp first-run show' prints it",
	"spent":      "another request already spent this challenge, so run the command again",
}

// termsRefusalSentences say what each refusal code the core sends means
// for the operator. The command never shows text the server sent along
// with a code.
var termsRefusalSentences = map[string]string{
	"REQUEST_INVALID":     "the core could not read the acceptance, so this ocfp and the managing system may not agree on its form; update whichever is older",
	"SETUP_CLAIM_REFUSED": "the core refused the acceptance",
	"TLS_TERMS_ACCEPTANCE_REFUSED": "the terms are not the directory's current ones, this challenge was already used, or no acceptance is waiting; " +
		"run the command again to read the current terms",
	"ALREADY_INITIALIZED": "setup is finished, so only an administrator who has signed in can accept terms; " +
		"sign in to the managing system and accept them there",
	"SETUP_CLAIM_WRONG_ADDRESS": "the address is not the core's public address",
	"RATE_LIMITED":              "too many attempts came from this address, so wait a few minutes and run the command again",
	"SETUP_CLAIM_NO_SERVER_KEY": "the core can't name the key of its own certificate, so it can't take an acceptance bound to it; " +
		"check that the deployment names the certificate its public address presents",
	"SETUP_CLAIM_UNAVAILABLE": "the core carries no first-run key, so it can't check the acceptance; redeploy the managing system with the bloc's first-run key",
	"SERVICE_UNAVAILABLE":     "the core can't keep the acceptance right now, so run the command again in a minute",
}

// termsRefusal turns a refusal the core sent into an error that names its
// code and says in a sentence what it means. It shows the core's reason
// only through termsReasonSentences, and its address only when the
// certificate it answered with covers that address. A body with no code
// reads as an unreadable answer, with the step that drew it.
func termsRefusal(answer termsAnswer, step string) error {
	var body struct {
		Code    string `json:"code"`
		Reason  string `json:"reason"`
		Address string `json:"address"`
	}

	if json.Unmarshal(answer.body, &body) != nil || body.Code == "" {
		return &firstrun.Error{
			Code:   codeFirstRunCoreAnswer,
			Detail: fmt.Sprintf("the core answered HTTP %d with no refusal code while %s", answer.status, step),
		}
	}

	sentence, known := termsRefusalSentences[body.Code]

	switch {
	case !known && termsCodeShape.MatchString(body.Code):
		sentence = "the core refused with a code this ocfp doesn't know, so update ocfp"
	case !known:
		return &firstrun.Error{Code: codeFirstRunCoreAnswer, Detail: "the core refused with a code this ocfp can't read"}
	}

	switch body.Code {
	case "SETUP_CLAIM_REFUSED":
		if hint, ok := termsReasonSentences[body.Reason]; ok {
			sentence = hint
		}
	case "SETUP_CLAIM_WRONG_ADDRESS":
		sentence += "; use " + termsWrongAddress(body.Address, answer.tls)
	}

	return &firstrun.Error{Code: body.Code, Detail: sentence}
}

// termsWrongAddress names the address a SETUP_CLAIM_WRONG_ADDRESS refusal
// gives, when it is in the one form normalizeCoreURL gives, fits on a
// line, and names a host the certificate on the same answer covers. That
// is another name of the certificate the operator already trusts here, so
// a server can't steer the operator to an address of its choosing.
func termsWrongAddress(address string, state *tls.ConnectionState) string {
	const unnamed = "the core's public address"

	norm, err := normalizeCoreURL(address)
	if err != nil || norm != address || len(address) > maxTermsDetail || state == nil || len(state.PeerCertificates) == 0 {
		return unnamed
	}

	parsed, err := url.Parse(address)
	if err != nil || state.PeerCertificates[0].VerifyHostname(parsed.Hostname()) != nil {
		return unnamed
	}

	return address
}
