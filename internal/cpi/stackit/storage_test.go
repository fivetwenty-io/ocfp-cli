package stackit_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi/stackit"
)

func TestParseBlocFromBucketName(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{"prod-bosh-blobstore", "prod"},
		{"dev-cf-buildpacks", "dev"},
		{"namewithoutdash", ""},
		{"-leading", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := stackit.ParseBlocFromBucketName(c.in)
		if got != c.want {
			t.Fatalf("stackit.ParseBlocFromBucketName(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
