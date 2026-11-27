package cloudflare

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	groupsKey  = "GET /client/v4/user/tokens/permission_groups"
	createKey  = "POST /client/v4/user/tokens"
	testSecret = "tok-SECRET-value-0123456789"
)

const groupsBody = `{"success":true,"result":[
 {"id":"g-read","name":"Zone Read","scopes":["com.cloudflare.api.account.zone"]},
 {"id":"g-acct-dns","name":"DNS Write","scopes":["com.cloudflare.api.account"]},
 {"id":"g-dns","name":"DNS Write","scopes":["com.cloudflare.api.account.zone"]}
]}`

func createdBody() string {
	return `{"success":true,"result":{"id":"tok-id","value":"` + testSecret + `"}}`
}

type createRequest struct {
	Name     string `json:"name"`
	Policies []struct {
		Effect           string            `json:"effect"`
		Resources        map[string]string `json:"resources"`
		PermissionGroups []struct {
			ID string `json:"id"`
		} `json:"permission_groups"`
	} `json:"policies"`
	Condition struct {
		RequestIP struct {
			In    []string `json:"in"`
			NotIn []string `json:"not_in"`
		} `json:"request_ip"`
	} `json:"condition"`
	ExpiresOn string `json:"expires_on"`
}

// TestCreateDNSChallengeTokenScopesToOneZoneAndPinsTheFilter checks what
// is actually sent: the zone's DNS edit group only, the one zone resource,
// the client IP filter, and the returned secret.
func TestCreateDNSChallengeTokenScopesToOneZoneAndPinsTheFilter(t *testing.T) {
	t.Parallel()

	f := &fakeDoer{responses: map[string]string{groupsKey: groupsBody, createKey: createdBody()}}
	c := NewClient("bloc-admin-token", f)

	got, err := c.CreateDNSChallengeToken(context.Background(), "ocfp-dns-challenge-lab", "zone-abc",
		[]string{"203.0.113.0/24", "198.51.100.7"})
	require.NoError(t, err)
	assert.Equal(t, testSecret, got)

	require.Len(t, f.requests, 2)
	assert.Equal(t, groupsKey, f.requests[0].Method+" "+f.requests[0].Path)
	assert.Equal(t, createKey, f.requests[1].Method+" "+f.requests[1].Path)

	var sent createRequest
	require.NoError(t, json.Unmarshal(f.requests[1].Body, &sent))

	assert.Equal(t, "ocfp-dns-challenge-lab", sent.Name)
	require.Len(t, sent.Policies, 1, "one policy and no more")
	assert.Equal(t, "allow", sent.Policies[0].Effect)
	assert.Equal(t, map[string]string{"com.cloudflare.api.account.zone.zone-abc": "*"}, sent.Policies[0].Resources)
	require.Len(t, sent.Policies[0].PermissionGroups, 1)
	assert.Equal(t, "g-dns", sent.Policies[0].PermissionGroups[0].ID, "the zone-scoped DNS Write group")
	assert.Equal(t, []string{"203.0.113.0/24", "198.51.100.7"}, sent.Condition.RequestIP.In)
	assert.Empty(t, sent.Condition.RequestIP.NotIn)
	assert.Empty(t, sent.ExpiresOn, "the token renews with the core and carries no expiry")
	assert.Equal(t, "Bearer bloc-admin-token", f.seen[1].Header.Get("Authorization"))
}

// TestCreateDNSChallengeTokenNeverWithoutAFilter checks that an empty or
// unusable filter is refused before any call goes out.
func TestCreateDNSChallengeTokenNeverWithoutAFilter(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		egress []string
	}{
		{"nil", nil},
		{"empty", []string{}},
		{"blank entry", []string{"203.0.113.0/24", " "}},
		{"not an address", []string{"nope"}},
		{"every IPv4 address", []string{"0.0.0.0/0"}},
		{"every IPv6 address", []string{"::/0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &fakeDoer{responses: map[string]string{groupsKey: groupsBody, createKey: createdBody()}}

			got, err := NewClient("tok", f).CreateDNSChallengeToken(context.Background(), "n", "zone-abc", tc.egress)
			require.Error(t, err)
			assert.Equal(t, CodeEgressUnknown, CodeOf(err))
			assert.Empty(t, got)
			assert.Empty(t, f.requests, "nothing is sent without a usable filter")
		})
	}
}

// TestCreateDNSChallengeTokenRefusesBlankNames checks the other inputs.
func TestCreateDNSChallengeTokenRefusesBlankNames(t *testing.T) {
	t.Parallel()

	f := &fakeDoer{}
	c := NewClient("tok", f)

	_, err := c.CreateDNSChallengeToken(context.Background(), "", "zone-abc", []string{"203.0.113.0/24"})
	assert.Equal(t, CodeTokenInvalid, CodeOf(err))

	_, err = c.CreateDNSChallengeToken(context.Background(), "n", " ", []string{"203.0.113.0/24"})
	assert.Equal(t, CodeTokenInvalid, CodeOf(err))
	assert.Empty(t, f.requests)
}

// TestCreateDNSChallengeTokenRefusals checks each failure's code, and that
// no refusal carries a token, whether the bloc's or a new one.
func TestCreateDNSChallengeTokenRefusals(t *testing.T) {
	t.Parallel()

	noGroup := `{"success":true,"result":[{"id":"g-read","name":"Zone Read","scopes":["com.cloudflare.api.account.zone"]}]}`

	for _, tc := range []struct {
		name      string
		responses map[string]string
		status    map[string]int
		wantCode  string
	}{
		{"groups listing refused", map[string]string{groupsKey: `{"success":false,"errors":[{"code":9109}]}`},
			map[string]int{groupsKey: 403}, CodeTokenRefused},
		{"no zone DNS edit group", map[string]string{groupsKey: noGroup}, nil, CodeTokenRefused},
		{"create refused", map[string]string{groupsKey: groupsBody, createKey: `{"success":false,"errors":[{"code":9109}]}`},
			map[string]int{createKey: 403}, CodeTokenRefused},
		{"created without a value", map[string]string{groupsKey: groupsBody, createKey: `{"success":true,"result":{"id":"tok-id"}}`},
			nil, CodeTokenRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &fakeDoer{responses: tc.responses, status: tc.status}

			got, err := NewClient("bloc-admin-token", f).CreateDNSChallengeToken(
				context.Background(), "n", "zone-abc", []string{"203.0.113.0/24"})
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, CodeOf(err))
			assert.Empty(t, got)
			assert.NotContains(t, err.Error(), "bloc-admin-token")
			assert.NotContains(t, err.Error(), testSecret)
		})
	}
}

// TestCreateDNSChallengeTokenHidesTheSecretOnBadResults checks that a
// result that the decoder rejects doesn't echo the token the API sent.
func TestCreateDNSChallengeTokenHidesTheSecretOnBadResults(t *testing.T) {
	t.Parallel()

	f := &fakeDoer{responses: map[string]string{
		groupsKey: groupsBody,
		createKey: `{"success":true,"result":{"id":["` + testSecret + `"],"value":"` + testSecret + `"}}`,
	}}

	_, err := NewClient("tok", f).CreateDNSChallengeToken(context.Background(), "n", "zone-abc", []string{"203.0.113.0/24"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testSecret)
}
