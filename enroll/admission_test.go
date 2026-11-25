package enroll

import (
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// admissionAccount builds an admission account as an operator would: an
// account whose only user-signing key is the published issuer key, as a
// scoped key whose template pins each user to its own subject.
func admissionAccount(t *testing.T) (*jwt.AccountClaims, string) {
	t.Helper()

	acct, err := nkeys.FromRawSeed(nkeys.PrefixByteAccount, rfcKey(t, 2).Seed())
	if err != nil {
		t.Fatal(err)
	}

	pub, err := acct.PublicKey()
	if err != nil {
		t.Fatal(err)
	}

	scope := jwt.NewUserScope()
	scope.Key = AdmissionIssuerKey
	scope.Role = "admission"
	scope.Template.Pub.Allow.Add("example.*.svc.join.v1.*.{{subject()}}")
	scope.Template.Sub.Allow.Add("example.join.inbox.{{subject()}}.>")
	scope.Template.Subs = 2
	scope.Template.Payload = MaxMessage

	ac := jwt.NewAccountClaims(pub)
	ac.SigningKeys.AddScopedSigner(scope)

	return ac, pub
}

func TestAdmissionIssuerKeyIsTheSeeds(t *testing.T) {
	t.Parallel()

	kp, err := nkeys.FromSeed([]byte(AdmissionIssuerSeed))
	if err != nil {
		t.Fatal(err)
	}

	pub, err := kp.PublicKey()
	if err != nil || pub != AdmissionIssuerKey || !ValidAccountKey(pub) {
		t.Errorf("the seed's public key is %s, want %s", pub, AdmissionIssuerKey)
	}
}

// TestMintedUserIsScoped decodes a minted user and checks it against the
// account's scope the way the bus does: the scoped signer must accept
// it, so the user carries no permission of its own.
func TestMintedUserIsScoped(t *testing.T) {
	t.Parallel()

	ac, acctPub := admissionAccount(t)
	key := nkeyPublic(t, nkeyUser, 1)
	now := goldenCreatedAt

	token, err := MintAdmissionUser(acctPub, key, now)
	if err != nil {
		t.Fatal(err)
	}

	uc, err := jwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case uc.Subject != key:
		t.Errorf("subject = %s", uc.Subject)
	case uc.Issuer != AdmissionIssuerKey:
		t.Errorf("issuer = %s", uc.Issuer)
	case uc.IssuerAccount != acctPub:
		t.Errorf("issuer account = %s", uc.IssuerAccount)
	case uc.Expires != now.Add(AdmissionUserLifetime).Unix():
		t.Errorf("expires = %d", uc.Expires)
	case !uc.HasEmptyPermissions():
		t.Error("the user carries permissions of its own")
	}

	if !ac.DidSign(uc) {
		t.Fatal("the account does not take the user as its own")
	}

	scope, ok := ac.SigningKeys.GetScope(uc.Issuer)
	if !ok || scope == nil {
		t.Fatal("the issuer is not a scoped key of the account")
	}

	if err := scope.ValidateScopedSigner(uc); err != nil {
		t.Errorf("the scope refuses the minted user: %v", err)
	}
}

// TestUnscopedUserIsRefused pins why the minting must set the scope: a
// user that the published key signs with permissions of its own is
// refused by the scope, so the published seed grants nothing beyond the
// template.
func TestUnscopedUserIsRefused(t *testing.T) {
	t.Parallel()

	ac, acctPub := admissionAccount(t)

	issuer, err := nkeys.FromSeed([]byte(AdmissionIssuerSeed))
	if err != nil {
		t.Fatal(err)
	}

	uc := jwt.NewUserClaims(nkeyPublic(t, nkeyUser, 1))
	uc.IssuerAccount = acctPub
	uc.Pub.Allow.Add(">")
	uc.Sub.Allow.Add(">")

	token, err := uc.Encode(issuer)
	if err != nil {
		t.Fatal(err)
	}

	back, err := jwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatal(err)
	}

	scope, ok := ac.SigningKeys.GetScope(back.Issuer)
	if !ok || scope == nil {
		t.Fatal("the issuer is not a scoped key of the account")
	}

	if err := scope.ValidateScopedSigner(back); err == nil {
		t.Error("the scope took a user with permissions of its own")
	}
}

func TestMintAdmissionUserRefuses(t *testing.T) {
	t.Parallel()

	user, acct := nkeyPublic(t, nkeyUser, 1), nkeyPublic(t, nkeyAccount, 1)
	now := time.Now()

	for name, c := range map[string][2]string{
		"user as account": {user, user},
		"account as user": {acct, acct},
		"empty":           {"", ""},
	} {
		if tok, err := MintAdmissionUser(c[0], c[1], now); err == nil {
			t.Errorf("%s: minted %d bytes", name, len(tok))
		}
	}
}
