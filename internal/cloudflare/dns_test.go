package cloudflare

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertCNAME_CreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records":  `{"success":true,"result":[]}`,
		"POST /client/v4/zones/z/dns_records": `{"success":true,"result":{"id":"rec-1"}}`,
	}}
	c := NewClient("t", f)
	err := c.UpsertCNAME(context.Background(), "z", "*.apps.ocf.wayne.lab.fivetwenty.io", "tun-1.cfargotunnel.com")
	require.NoError(t, err)
}

func TestDeleteCNAME_NotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/zones/z/dns_records": `{"success":true,"result":[]}`,
	}}
	c := NewClient("t", f)
	// no record to delete -> nil error
	assert.NoError(t, c.DeleteCNAME(context.Background(), "z", "*.apps.ocf.wayne.lab.fivetwenty.io"))
}
