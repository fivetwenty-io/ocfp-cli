package enroll

import (
	"errors"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// The admission issuer key. Every installation's admission account
// carries this key as its one user-signing key, and only as a scoped
// key, so any caller can mint a user for its own nkey and the bus
// replaces that user's permissions with the scope's template. The seed
// is published on purpose: the scope, not the seed, is what limits an
// admission user, and the account's own identity key stays secret.
const (
	AdmissionIssuerSeed = "SAAEGUZRBQAHJJ46TIKTU36ADMGPIJ4O7SEVXVCQMPK37HS7NLH7HSYE5I" //nolint:gosec // published by design
	AdmissionIssuerKey  = "ABSA5NKTYCVPQ74ZYBOBA73Y3PGOZBAOJ2M6PCFA5JSDSZMKP527NDRN"
)

// AdmissionUserLifetime is how long an admission user lives. The caller
// mints a new one for each connection, and the bus closes an admission
// connection long before the user lapses.
const AdmissionUserLifetime = 10 * time.Minute

// MintAdmissionUser returns the user JWT with which the holder of
// userKey connects to the admission account, signed with the published
// issuer key and naming account as its issuer account. The user is
// scoped, so it carries no permission or limit of its own, and the bus
// grants it only what the scope's template grants. It expires
// AdmissionUserLifetime after now.
func MintAdmissionUser(account, userKey string, now time.Time) (string, error) {
	if !ValidAccountKey(account) {
		return "", errors.New("enroll: the admission account is not an account key")
	}

	if !ValidUserKey(userKey) {
		return "", errors.New("enroll: the key is not a user key")
	}

	issuer, err := nkeys.FromSeed([]byte(AdmissionIssuerSeed))
	if err != nil {
		return "", errors.New("enroll: the admission issuer seed does not read")
	}
	defer issuer.Wipe()

	uc := jwt.NewUserClaims(userKey)
	uc.SetScoped(true)
	uc.IssuerAccount = account
	uc.Expires = now.Add(AdmissionUserLifetime).Unix()

	token, err := uc.Encode(issuer)
	if err != nil {
		return "", errors.New("enroll: the admission user does not sign")
	}

	return token, nil
}
