package vault

import (
	"regexp"
	"strings"
	"testing"
)

// policyPathLine reads the path of each rule in a rendered policy.
var policyPathLine = regexp.MustCompile(`(?m)^path "([^"]*)" \{$`)

// vaultGlob compiles a policy path into the match Vault applies: a
// segment that is exactly + matches any one segment, and a trailing *
// matches any suffix. Anything else matches itself.
func vaultGlob(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()

	prefix, glob := strings.CutSuffix(pattern, "*")

	segments := strings.Split(prefix, "/")
	for i, s := range segments {
		if s == "+" {
			segments[i] = `[^/]+`
		} else {
			segments[i] = regexp.QuoteMeta(s)
		}
	}

	expr := "^" + strings.Join(segments, "/")
	if glob {
		expr += ".*"
	}

	return regexp.MustCompile(expr + "$")
}

// TestVaultGlobFollowsVaultRules pins the matcher itself, so the policy
// test below can't pass by matching nothing.
func TestVaultGlobFollowsVaultRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"secret/lab-operator/*", "secret/lab-operator/first-run", true},
		{"secret/lab-operator/*", "secret/lab-operator", false},
		{"secret/lab-op*", "secret/lab-operator/first-run", true},
		{"secret/+/first-run", "secret/lab-operator/first-run", true},
		{"secret/+/first-run", "secret/a/b/first-run", false},
		{"secret/config/lab/*", "secret/lab-operator/first-run", false},
		{"secret/lab-operator/first-run", "secret/lab-operator/first-run", true},
		{"secret/lab-operator/first-run", "secret/lab-operator/first-runs", false},
	}

	for _, c := range cases {
		if got := vaultGlob(t, c.pattern).MatchString(c.path); got != c.want {
			t.Errorf("%s against %s = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// operatorSecretPaths are the paths of the bloc's operator secrets, the
// first-run code and the DNS challenge credential, in their logical form
// and in the forms a version 2 KV mount checks policies against.
func operatorSecretPaths(bloc string) []string {
	leaves := []string{FirstRunCodePath(bloc), "secret/" + bloc + operatorTreeSuffix + "/dns-challenge"}

	paths := make([]string, 0, 3*len(leaves))
	for _, leaf := range leaves {
		rest := strings.TrimPrefix(leaf, SecretPrefix+"/")
		paths = append(paths, leaf, SecretPrefix+"/data/"+rest, SecretPrefix+"/metadata/"+rest)
	}

	return paths
}

// TestOnlyTheAdminPolicyReachesTheOperatorSecrets renders every policy the
// manager creates and matches each rule against the operator secrets'
// paths. Only the admin policy may match, and it must match both leaves
// with full capabilities. The readonly policy is for monitoring, so it
// must not read the first-run code.
func TestOnlyTheAdminPolicyReachesTheOperatorSecrets(t *testing.T) {
	t.Parallel()

	const bloc = "lab"

	pm := &PolicyManager{blocName: bloc}
	admin := pm.getPolicyName("admin")

	templates := pm.getOCFPPolicyTemplates()
	if len(templates) != PolicyTemplateCapacity {
		t.Fatalf("the manager creates %d policies, want %d", len(templates), PolicyTemplateCapacity)
	}

	adminLeaves := map[string]bool{}

	for _, tmpl := range templates {
		hcl := pm.generatePolicyHCL(tmpl)

		rules := policyPathLine.FindAllStringSubmatch(hcl, -1)
		if len(rules) != len(tmpl.Rules) {
			t.Fatalf("%s renders %d rules, its template holds %d", tmpl.Name, len(rules), len(tmpl.Rules))
		}

		for i, rule := range rules {
			glob := vaultGlob(t, rule[1])

			for _, path := range operatorSecretPaths(bloc) {
				if !glob.MatchString(path) {
					continue
				}

				if tmpl.Name != admin {
					t.Errorf("%s's rule %q matches %s", tmpl.Name, rule[1], path)

					continue
				}

				caps := strings.Join(tmpl.Rules[i].Capabilities, ",")
				if caps != "create,read,update,delete,list" {
					t.Errorf("the admin rule %q grants %s, want full capabilities", rule[1], caps)
				}

				adminLeaves[path] = true
			}
		}
	}

	for _, leaf := range []string{FirstRunCodePath(bloc), "secret/" + bloc + operatorTreeSuffix + "/dns-challenge"} {
		if !adminLeaves[leaf] {
			t.Errorf("the admin policy doesn't reach %s", leaf)
		}
	}
}

// TestThePublicHalfStaysInTheConfigTree checks that the public half sits
// where every policy that reads the bloc's config tree reads it, and
// where the deployment policies' narrower rules don't reach.
func TestThePublicHalfStaysInTheConfigTree(t *testing.T) {
	t.Parallel()

	const bloc = "lab"

	pm := &PolicyManager{blocName: bloc}
	public := FirstRunPublicPath(bloc)

	readers := map[string]bool{}

	for _, tmpl := range pm.getOCFPPolicyTemplates() {
		for _, rule := range tmpl.Rules {
			if vaultGlob(t, rule.Path).MatchString(public) {
				readers[tmpl.Name] = true
			}
		}
	}

	for _, want := range []string{"admin", "migration", "readonly"} {
		if !readers[pm.getPolicyName(want)] {
			t.Errorf("%s doesn't read the public half", want)
		}
	}

	for _, not := range []string{"bosh", "cf"} {
		if readers[pm.getPolicyName(not)] {
			t.Errorf("%s reads the public half", not)
		}
	}
}
