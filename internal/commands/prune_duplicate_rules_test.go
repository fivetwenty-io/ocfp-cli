package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// pruneFakeSecurity is a cpi.SecurityManager that keeps rules per group the
// way PVE does: the rule ID is its position, and every delete renumbers the
// rules behind it.
type pruneFakeSecurity struct {
	groups  []*cpi.SecurityGroup
	rules   map[string][]*cpi.SecurityRule
	removed []string
	added   int
	// afterRemove runs after each delete, so a test can simulate another
	// writer changing the group.
	afterRemove func(groupID string)
}

func newPruneFake(rules map[string][]*cpi.SecurityRule, groups ...*cpi.SecurityGroup) *pruneFakeSecurity {
	fake := &pruneFakeSecurity{groups: groups, rules: rules}
	for id := range rules {
		fake.renumber(id)
	}

	return fake
}

func (f *pruneFakeSecurity) renumber(groupID string) {
	for i, r := range f.rules[groupID] {
		r.ID = strconv.Itoa(i)
	}
}

func (f *pruneFakeSecurity) CreateSecurityGroup(_ context.Context, _ *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return nil, errors.New("unexpected CreateSecurityGroup") //nolint:err113 // test fake
}

func (f *pruneFakeSecurity) GetSecurityGroup(_ context.Context, _ string) (*cpi.SecurityGroup, error) {
	return nil, errors.New("unexpected GetSecurityGroup") //nolint:err113 // test fake
}

func (f *pruneFakeSecurity) ListSecurityGroups(_ context.Context, _ map[string]string) ([]*cpi.SecurityGroup, error) {
	return f.groups, nil
}

func (f *pruneFakeSecurity) DeleteSecurityGroup(_ context.Context, _ string) error {
	return errors.New("unexpected DeleteSecurityGroup") //nolint:err113 // test fake
}

func (f *pruneFakeSecurity) AddSecurityRule(_ context.Context, _ string, _ *cpi.SecurityRule) error {
	f.added++

	return errors.New("unexpected AddSecurityRule") //nolint:err113 // test fake
}

func (f *pruneFakeSecurity) RemoveSecurityRule(_ context.Context, groupID, ruleID string) error {
	pos, err := strconv.Atoi(ruleID)
	if err != nil || pos < 0 || pos >= len(f.rules[groupID]) {
		return fmt.Errorf("no rule %q in %s", ruleID, groupID) //nolint:err113 // test fake
	}

	f.rules[groupID] = append(f.rules[groupID][:pos], f.rules[groupID][pos+1:]...)
	f.renumber(groupID)
	f.removed = append(f.removed, groupID+"/"+ruleID)

	if f.afterRemove != nil {
		f.afterRemove(groupID)
	}

	return nil
}

func (f *pruneFakeSecurity) ListSecurityRules(_ context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	out := make([]*cpi.SecurityRule, len(f.rules[groupID]))
	for i, r := range f.rules[groupID] {
		cp := *r
		out[i] = &cp
	}

	return out, nil
}

func pruneRule(direction, remote, comment string, port int) *cpi.SecurityRule {
	return &cpi.SecurityRule{
		Direction: direction, Protocol: "tcp", PortRangeMin: port, PortRangeMax: port,
		RemoteIPCIDR: remote, Description: comment,
		Attributes: map[string]string{"action": "ACCEPT", "enable": "1"},
	}
}

const pruneBloc = "lab"

func pruneOwned(name string) bool { return name == pruneBloc+"-bastion" }

func TestPruneDryRunListsAndDeletesNothing(t *testing.T) {
	t.Parallel()

	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": {
			pruneRule("ingress", "", "ssh", 22),
			pruneRule("ingress", "", "web", 80),
			pruneRule("ingress", "", "ssh", 22),
			pruneRule("ingress", "", "ssh", 22),
		}},
		&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
	)

	var out bytes.Buffer

	res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, false, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.removed) != 0 {
		t.Errorf("dry run deleted %v", fake.removed)
	}

	if res.Duplicates != 2 || res.Deleted != 0 {
		t.Errorf("result = %+v, want 2 duplicates and 0 deleted", res)
	}

	text := out.String()
	for _, want := range []string{"lab-bastion/2", "lab-bastion/3", "duplicate of lab-bastion/0", "lab-bastion: 2 duplicate", "Total: 2 duplicate"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, "lab-bastion/1 ") || strings.Contains(text, "lab-bastion/0 ") {
		t.Errorf("output lists a kept rule as a duplicate:\n%s", text)
	}
}

func TestPruneApplyDeletesOnlyDuplicatesHighestFirst(t *testing.T) {
	t.Parallel()

	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": {
			pruneRule("ingress", "", "ssh", 22),
			pruneRule("ingress", "", "web", 80),
			pruneRule("ingress", "", "ssh", 22),
			pruneRule("ingress", "10.0.0.0/8", "ssh", 22), // same but source, not a twin
			pruneRule("ingress", "", "ssh", 22),
		}},
		&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
	)

	var out bytes.Buffer

	res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out.String())
	}

	if got := strings.Join(fake.removed, ","); got != "g1/4,g1/2" {
		t.Errorf("removed = %s, want g1/4,g1/2", got)
	}

	if res.Deleted != 2 || len(fake.rules["g1"]) != 3 || fake.added != 0 {
		t.Errorf("result = %+v, remaining = %d, added = %d", res, len(fake.rules["g1"]), fake.added)
	}

	wantComments := []string{"ssh", "web", "ssh"}
	for i, r := range fake.rules["g1"] {
		if r.Description != wantComments[i] {
			t.Errorf("rule %d comment = %q, want %q", i, r.Description, wantComments[i])
		}
	}

	if fake.rules["g1"][2].RemoteIPCIDR != "10.0.0.0/8" {
		t.Errorf("the scoped rule was disturbed: %+v", fake.rules["g1"][2])
	}
}

func TestPruneStopsWhenRenumberingSurprises(t *testing.T) {
	t.Parallel()

	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": {
			pruneRule("ingress", "", "ssh", 22),
			pruneRule("ingress", "", "ssh", 22),
			pruneRule("ingress", "", "web", 80),
			pruneRule("ingress", "", "ssh", 22),
		}},
		&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
	)

	// After the first delete, someone swaps in a different rule at the
	// next target position.
	fake.afterRemove = func(groupID string) {
		fake.rules[groupID][1] = pruneRule("ingress", "", "other", 9999)
		fake.renumber(groupID)
	}

	var out bytes.Buffer

	_, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, true, &out)
	if err == nil {
		t.Fatalf("expected an error, got nil\n%s", out.String())
	}

	if got := strings.Join(fake.removed, ","); got != "g1/3" {
		t.Errorf("removed = %s, want only g1/3", got)
	}
}

func TestPruneLeavesUnownedGroupsAlone(t *testing.T) {
	t.Parallel()

	dupes := func() []*cpi.SecurityRule {
		return []*cpi.SecurityRule{pruneRule("ingress", "", "ssh", 22), pruneRule("ingress", "", "ssh", 22)}
	}
	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": dupes(), "g2": dupes()},
		&cpi.SecurityGroup{ID: "g1", Name: "handmade"},
		&cpi.SecurityGroup{ID: "g2", Name: "lab-bastion"},
	)

	var out bytes.Buffer

	res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := strings.Join(fake.removed, ","); got != "g2/1" {
		t.Errorf("removed = %s, want g2/1 only", got)
	}

	if len(fake.rules["g1"]) != 2 || res.Deleted != 1 {
		t.Errorf("unowned group changed or wrong total: %+v", res)
	}

	if strings.Contains(out.String(), "handmade") {
		t.Errorf("output mentions an unowned group:\n%s", out.String())
	}
}

func TestPruneNoDuplicatesIsNoOp(t *testing.T) {
	t.Parallel()

	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": {
			pruneRule("ingress", "", "ssh", 22),
			pruneRule("egress", "", "ssh", 22),
			pruneRule("ingress", "", "ssh", 23),
			pruneRule("ingress", "", "other comment", 22),
		}},
		&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
	)

	var out bytes.Buffer

	res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.removed) != 0 || res.Duplicates != 0 || len(fake.rules["g1"]) != 4 {
		t.Errorf("expected no-op, got %+v removed=%v", res, fake.removed)
	}
}

func TestPruneTreatsEquivalentSpellingsAsDuplicates(t *testing.T) {
	t.Parallel()

	a := pruneRule("in", "", "ssh", 22)
	b := pruneRule("ingress", "", "ssh", 22)
	c := pruneRule("INGRESS", "any", "ssh", 22)
	c.Protocol = "TCP"

	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": {a, b, c}},
		&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
	)

	var out bytes.Buffer

	res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := strings.Join(fake.removed, ","); got != "g1/2,g1/1" || res.Deleted != 2 || len(fake.rules["g1"]) != 1 {
		t.Errorf("removed = %s, result = %+v", got, res)
	}
}

func TestPruneKeepsRulesThatDifferInAttributes(t *testing.T) {
	t.Parallel()

	accept := pruneRule("ingress", "", "ssh", 22)
	drop := pruneRule("ingress", "", "ssh", 22)
	drop.Attributes = map[string]string{"action": "DROP", "enable": "1"}
	off := pruneRule("ingress", "", "ssh", 22)
	off.Attributes = map[string]string{"action": "ACCEPT", "enable": "0"}

	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": {accept, drop, off}},
		&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
	)

	var out bytes.Buffer

	res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Duplicates != 0 || len(fake.removed) != 0 {
		t.Errorf("rules with different action or enable were treated as twins: %+v", res)
	}
}

// An empty source (both address families), 0.0.0.0/0 (IPv4 only), and ::/0
// (IPv6 only) are three different rules, so none of them is a duplicate of
// another.
func TestPruneKeepsAnywhereSourcesApart(t *testing.T) {
	t.Parallel()

	fake := newPruneFake(
		map[string][]*cpi.SecurityRule{"g1": {
			pruneRule("ingress", "0.0.0.0/0", "ssh", 22),
			pruneRule("ingress", "::/0", "ssh", 22),
			pruneRule("ingress", "", "ssh", 22),
		}},
		&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
	)

	var out bytes.Buffer

	res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Duplicates != 0 || len(fake.removed) != 0 {
		t.Errorf("rules with different anywhere sources were treated as twins: %+v\n%s", res, out.String())
	}
}

func TestPruneStillPairsExactAnywhereTwins(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"", "0.0.0.0/0", "::/0"} {
		fake := newPruneFake(
			map[string][]*cpi.SecurityRule{"g1": {
				pruneRule("ingress", source, "ssh", 22),
				pruneRule("ingress", source, "ssh", 22),
			}},
			&cpi.SecurityGroup{ID: "g1", Name: "lab-bastion"},
		)

		var out bytes.Buffer

		res, err := pruneDuplicateRules(context.Background(), fake, pruneOwned, false, &out)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if res.Duplicates != 1 {
			t.Errorf("source %q: duplicates = %d, want 1", source, res.Duplicates)
		}
	}
}

func TestValidatePruneOptions(t *testing.T) {
	t.Parallel()

	if err := validatePruneOptions(&configureOptions{pruneDuplicates: true}); err != nil {
		t.Errorf("plain prune rejected: %v", err)
	}

	if err := validatePruneOptions(&configureOptions{pruneDuplicates: true, apply: true}); err != nil {
		t.Errorf("prune with apply rejected: %v", err)
	}

	if err := validatePruneOptions(&configureOptions{apply: true}); !errors.Is(err, ErrApplyWithoutPrune) {
		t.Errorf("apply alone = %v, want ErrApplyWithoutPrune", err)
	}

	for _, opts := range []*configureOptions{
		{pruneDuplicates: true, dryRun: true},
		{pruneDuplicates: true, skipRoutes: true},
		{pruneDuplicates: true, skipBastion: true},
	} {
		if err := validatePruneOptions(opts); !errors.Is(err, ErrPruneFlagConflict) {
			t.Errorf("%+v = %v, want ErrPruneFlagConflict", opts, err)
		}
	}
}
