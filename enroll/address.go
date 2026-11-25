package enroll

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// AddressVersion is the version of the enrollment address's payload.
const AddressVersion = 1

// MaxAddress bounds an enrollment address's length in bytes.
const MaxAddress = 2 << 10

// addressPath sits between the UI host and the payload. The payload
// rides in the fragment, so no server or proxy log ever holds it.
const addressPath = "/#/enroll/v1/"

// The operations of the release read, and the last token of its answer
// subject.
const (
	ReleaseManifest = "manifest"
	ReleaseChunk    = "chunk"
	ReleaseStatus   = "status"
	releaseAnswer   = "rel"
)

// Address is the enrollment address: the one non-secret value a bastion
// needs to find its managing system. It is a URL on the managing
// system's UI, so a person who pastes it into a browser lands on the
// org's workers page, and its fragment holds the payload, the base64url
// of the canonical JSON of every field but UIHost.
//
// The subject roots ride in the address so that this package never
// names the managing system. CAPin, when set, is the lower-case hex
// SHA-256 of the bus's private root certificate. With no pin, the bus
// presents a certificate that public roots verify.
type Address struct {
	UIHost             string `json:"-"`
	Version            int    `json:"version"`
	BusURL             string `json:"busUrl"`
	Installation       string `json:"installation"`
	Org                string `json:"org"`
	AdmissionAccount   string `json:"admissionAccount"`
	RequestRoot        string `json:"requestRoot"`
	InboxRoot          string `json:"inboxRoot"`
	ReleaseRequestRoot string `json:"releaseRequestRoot"`
	ReleaseAnswerRoot  string `json:"releaseAnswerRoot"`
	CAPin              string `json:"caPin,omitempty"`
}

// Validate holds the address to its form, and refuses with
// CodeAddressInvalid.
func (a Address) Validate() error {
	bad := func(what string) error { return refuse(CodeAddressInvalid, "the enrollment address's "+what) }

	switch {
	case !validHost(a.UIHost):
		return bad("UI host is not a host")
	case a.Version != AddressVersion:
		return bad(fmt.Sprintf("version is not %d", AddressVersion))
	case !validBusURL(a.BusURL):
		return bad("bus URL is not a wss URL")
	case !ValidInstallation(a.Installation):
		return bad("installation is not in its form")
	case !ValidOrg(a.Org):
		return bad("org is not in its form")
	case !ValidAccountKey(a.AdmissionAccount):
		return bad("admission account is not an account key")
	case !validRoot(a.RequestRoot) || !validRoot(a.InboxRoot) ||
		!validRoot(a.ReleaseRequestRoot) || !validRoot(a.ReleaseAnswerRoot):
		return bad("subject roots are not literal subjects")
	case a.CAPin != "" && !pinForm.MatchString(a.CAPin):
		return bad("CA pin is not the hex of a SHA-256")
	}

	return nil
}

// String returns the address in its one form,
// https://{ui-host}/#/enroll/v1/{payload}. It returns the empty string
// for an address that does not validate.
func (a Address) String() string {
	s, err := a.Encode()
	if err != nil {
		return ""
	}

	return s
}

// Encode returns the address in its one form, and refuses an address
// that does not validate or is longer than MaxAddress.
func (a Address) Encode() (string, error) {
	payload, err := a.payload()
	if err != nil {
		return "", err
	}

	s := "https://" + a.UIHost + addressPath + base64.RawURLEncoding.EncodeToString(payload)
	if len(s) > MaxAddress {
		return "", refuse(CodeAddressInvalid, fmt.Sprintf("the enrollment address is longer than %d bytes", MaxAddress))
	}

	return s, nil
}

func (a Address) payload() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(a); err != nil {
		return nil, refuse(CodeAddressInvalid, "the enrollment address does not encode")
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ParseAddress reads an enrollment address. It takes surrounding white
// space, and it refuses with CodeAddressInvalid an address longer than
// MaxAddress, one that is not https, one whose payload is not the
// canonical encoding of a valid address, and anything else.
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	bad := func(what string) (Address, error) {
		return Address{}, refuse(CodeAddressInvalid, "the enrollment address "+what)
	}

	if len(s) > MaxAddress {
		return bad(fmt.Sprintf("is longer than %d bytes", MaxAddress))
	}

	rest, ok := strings.CutPrefix(s, "https://")
	if !ok {
		return bad("does not begin with https://")
	}

	host, encoded, ok := strings.Cut(rest, addressPath)
	if !ok {
		return bad("has no enrollment fragment")
	}

	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return bad("payload is not unpadded base64url")
	}

	a, err := decodeAddressPayload(raw)
	if err != nil {
		return bad("payload " + err.Error())
	}

	a.UIHost = host

	if err := a.Validate(); err != nil {
		return Address{}, err
	}

	canon, err := a.payload()
	if err != nil || !bytes.Equal(canon, raw) {
		return bad("payload is not in its canonical form")
	}

	return a, nil
}

func decodeAddressPayload(raw []byte) (Address, error) {
	var a Address

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&a); err != nil {
		return Address{}, errors.New("does not decode")
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Address{}, errors.New("has data after its value")
	}

	return a, nil
}

// RequestSubject returns the subject on which the holder of key asks to
// enroll a worker for bloc: the request root, the bloc, and the key.
func (a Address) RequestSubject(bloc, key string) (string, error) {
	if !ValidBloc(bloc) || !ValidUserKey(key) {
		return "", refuse(CodeRequestInvalid, "the bloc or the key is not in its form")
	}

	return a.RequestRoot + "." + bloc + "." + key, nil
}

// ParseRequestSubject returns the bloc and the key of a subject that
// RequestSubject built from this address, and refuses any other subject
// with CodeRequestInvalid.
func (a Address) ParseRequestSubject(subject string) (bloc, key string, err error) {
	rest, ok := strings.CutPrefix(subject, a.RequestRoot+".")
	if !ok {
		return "", "", refuse(CodeRequestInvalid, "the subject is not under the request root")
	}

	bloc, key, ok = strings.Cut(rest, ".")
	if !ok || !ValidBloc(bloc) || !ValidUserKey(key) {
		return "", "", refuse(CodeRequestInvalid, "the subject does not end in a bloc and a key")
	}

	return bloc, key, nil
}

// InboxPrefix returns the inbox prefix the holder of key sets on its
// connection: the inbox root and the key. The admission account lets a
// user hear replies under its own key's prefix alone.
func (a Address) InboxPrefix(key string) (string, error) {
	if !ValidUserKey(key) {
		return "", refuse(CodeRequestInvalid, "the key is not a user key")
	}

	return a.InboxRoot + "." + key, nil
}

// ReplyValid reports whether reply is an inbox subject under the prefix
// of key, which is the only reply subject the managing system answers.
func (a Address) ReplyValid(reply, key string) bool {
	prefix, err := a.InboxPrefix(key)

	return err == nil && strings.HasPrefix(reply, prefix+".") && len(reply) > len(prefix)+1
}

// ReleaseSubject returns the subject of a release read operation for the
// worker workerID of bloc, where op is ReleaseManifest, ReleaseChunk, or
// ReleaseStatus.
func (a Address) ReleaseSubject(bloc, worker, op string) (string, error) {
	if !ValidBloc(bloc) || !ValidWorkerID(worker) {
		return "", refuse(CodeRequestInvalid, "the bloc or the worker id is not in its form")
	}

	switch op {
	case ReleaseManifest, ReleaseChunk, ReleaseStatus:
	default:
		return "", refuse(CodeRequestInvalid, "the release operation is not manifest, chunk, or status")
	}

	return a.ReleaseRequestRoot + "." + bloc + "." + worker + "." + op, nil
}

// ReleaseInbox returns the subject on which the worker workerID of bloc
// hears the answers to its release reads.
func (a Address) ReleaseInbox(bloc, worker string) (string, error) {
	if !ValidBloc(bloc) || !ValidWorkerID(worker) {
		return "", refuse(CodeRequestInvalid, "the bloc or the worker id is not in its form")
	}

	return a.ReleaseAnswerRoot + "." + bloc + "." + worker + "." + releaseAnswer, nil
}

// ApprovalLink returns the link a person opens to approve the worker of
// bloc whose host key has the fingerprint fp:
// https://{ui-host}/#/blocs/{bloc}/workers?expect={fp}. It names the
// bloc and the fingerprint and never the confirmation code, and the
// fingerprint rides in the fragment.
func (a Address) ApprovalLink(bloc, fp string) (string, error) {
	if !ValidBloc(bloc) {
		return "", refuse(CodeRequestInvalid, "the bloc is not in its form")
	}

	if _, err := ParseFingerprint(fp); err != nil {
		return "", refuse(CodeRequestInvalid, "the fingerprint is not in its form")
	}

	return "https://" + a.UIHost + "/#/blocs/" + bloc + "/workers?expect=" + url.QueryEscape(fp), nil
}

func validHost(host string) bool {
	if host == "" || strings.ContainsAny(host, "/?#@\\ ") {
		return false
	}

	u, err := url.Parse("https://" + host)

	return err == nil && u.Host == host && u.Hostname() != ""
}

func validBusURL(s string) bool {
	u, err := url.Parse(s)

	return err == nil && u.Scheme == "wss" && u.User == nil && u.Hostname() != "" &&
		u.RawQuery == "" && u.Fragment == "" && !strings.Contains(s, "#")
}
