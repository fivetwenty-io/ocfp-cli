package artifacts

import (
	"regexp"
	"testing"
)

var hexAlphabet = regexp.MustCompile(`^[0-9a-f]+$`)

func TestGenerateCredentials_ProducesHexOfExpectedLength(t *testing.T) {
	t.Parallel()

	c, err := GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials returned error: %v", err)
	}

	if len(c.AccessKey) != accessKeyBytes*2 {
		t.Errorf("access key length = %d, want %d", len(c.AccessKey), accessKeyBytes*2)
	}

	if len(c.SecretKey) != secretKeyBytes*2 {
		t.Errorf("secret key length = %d, want %d", len(c.SecretKey), secretKeyBytes*2)
	}

	if !hexAlphabet.MatchString(c.AccessKey) {
		t.Errorf("access key %q is not lowercase hex", c.AccessKey)
	}

	if !hexAlphabet.MatchString(c.SecretKey) {
		t.Errorf("secret key %q is not lowercase hex", c.SecretKey)
	}
}

func TestGenerateCredentials_ReturnsDistinctValues(t *testing.T) {
	t.Parallel()

	a, err := GenerateCredentials()
	if err != nil {
		t.Fatalf("first GenerateCredentials: %v", err)
	}

	b, err := GenerateCredentials()
	if err != nil {
		t.Fatalf("second GenerateCredentials: %v", err)
	}

	if a.AccessKey == b.AccessKey {
		t.Error("two consecutive access keys collided; rand source suspect")
	}

	if a.SecretKey == b.SecretKey {
		t.Error("two consecutive secret keys collided; rand source suspect")
	}
}

func TestResolveCredentials_PassThroughWhenBothProvided(t *testing.T) {
	t.Parallel()

	in := Credentials{AccessKey: "AK_FIXED", SecretKey: "SK_FIXED"}

	out, err := ResolveCredentials(in)
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}

	if out != in {
		t.Errorf("expected pass-through; got %+v", out)
	}
}

func TestResolveCredentials_GeneratesWhenEitherEmpty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   Credentials
	}{
		{"both empty", Credentials{}},
		{"only access", Credentials{AccessKey: "ak"}},
		{"only secret", Credentials{SecretKey: "sk"}},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := ResolveCredentials(tc.in)
			if err != nil {
				t.Fatalf("ResolveCredentials: %v", err)
			}

			if out.AccessKey == tc.in.AccessKey && out.SecretKey == tc.in.SecretKey {
				t.Errorf("expected regeneration; got input back %+v", out)
			}

			if len(out.AccessKey) != accessKeyBytes*2 || len(out.SecretKey) != secretKeyBytes*2 {
				t.Errorf("regenerated creds wrong length: %+v", out)
			}
		})
	}
}
