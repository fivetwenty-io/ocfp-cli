package stackit

import "testing"

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
		got := parseBlocFromBucketName(c.in)
		if got != c.want {
			t.Fatalf("parseBlocFromBucketName(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
