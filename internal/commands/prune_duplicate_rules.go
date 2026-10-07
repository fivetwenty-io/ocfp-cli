package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Errors reported while pruning duplicate security group rules.
var (
	ErrPruneFlagConflict   = errors.New("--prune-duplicate-rules runs on its own and cannot be combined with other configure options")
	ErrApplyWithoutPrune   = errors.New("--apply only applies to --prune-duplicate-rules or --check-cpi-role")
	ErrPruneUnexpectedRule = errors.New("security group changed during pruning")
	ErrPruneBadPosition    = errors.New("rule has a non-numeric position")

	ErrPruneProviderUnsupported = errors.New("--prune-duplicate-rules is supported only on PVE")
)

// pruneResult totals one pruning run.
type pruneResult struct {
	Duplicates int
	Deleted    int
}

// duplicateRule is one rule slated for removal and the surviving twin it
// duplicates.
type duplicateRule struct {
	rule *cpi.SecurityRule
	pos  int
	key  string
	keep *cpi.SecurityRule
}

// ownedSecurityGroup reports whether ocfp owns a security group. A group is
// ours when it is named "<bloc>-<short name>" and ocfp has a rule definition
// for that short name. This is the rule configure uses to decide which groups
// to reconcile, so pruning never reaches a group configure would not touch.
func ownedSecurityGroup(blocName, groupName string, ruleDefs map[string][]*cpi.SecurityRule) bool {
	shortName := strings.TrimPrefix(groupName, blocName+"-")
	if shortName == groupName {
		return false
	}

	_, ok := ruleDefs[shortName]

	return ok
}

// ruleKey is the full normalized identity of a rule. Rules with the same key
// are exact duplicates. The rule ID and position are left out on purpose. The
// source keeps an empty value, 0.0.0.0/0, and ::/0 apart, so only exact twins
// are ever paired.
func ruleKey(rule *cpi.SecurityRule) string {
	attrs := make([]string, 0, len(rule.Attributes))
	for k, v := range rule.Attributes {
		attrs = append(attrs, strings.ToLower(strings.TrimSpace(k))+"="+strings.TrimSpace(v))
	}

	sort.Strings(attrs)

	return strings.Join([]string{
		cpi.NormalizeDirection(rule.Direction),
		cpi.NormalizeProtocol(rule.Protocol),
		strconv.Itoa(rule.PortRangeMin) + "-" + strconv.Itoa(rule.PortRangeMax),
		cpi.NormalizeRemoteCIDRExact(rule.RemoteIPCIDR),
		strings.TrimSpace(rule.RemoteGroup),
		strings.TrimSpace(rule.Description),
		strings.Join(attrs, ","),
	}, "|")
}

// describeRule renders a rule as a readable line.
func describeRule(rule *cpi.SecurityRule) string {
	ports := "any port"

	switch {
	case rule.PortRangeMin == 0 && rule.PortRangeMax == 0:
	case rule.PortRangeMax > rule.PortRangeMin:
		ports = fmt.Sprintf("ports %d-%d", rule.PortRangeMin, rule.PortRangeMax)
	default:
		ports = fmt.Sprintf("port %d", rule.PortRangeMin)
	}

	remote := cpi.NormalizeRemoteCIDRExact(rule.RemoteIPCIDR)
	if remote == "" {
		remote = "anywhere"
	}

	if rule.RemoteGroup != "" {
		remote = "group " + rule.RemoteGroup
	}

	text := fmt.Sprintf("%s %s %s from %s", cpi.NormalizeDirection(rule.Direction), cpi.NormalizeProtocol(rule.Protocol), ports, remote)

	if rule.Description != "" {
		text += fmt.Sprintf(" (%q)", rule.Description)
	}

	if len(rule.Attributes) > 0 {
		keys := make([]string, 0, len(rule.Attributes))
		for k := range rule.Attributes {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + rule.Attributes[k]
		}

		text += " [" + strings.Join(parts, " ") + "]"
	}

	return text
}

// findDuplicates buckets a group's rules by key and returns every rule that
// has a lower-position twin, highest position first. A bucket's lowest
// position is always kept, so the last copy of a rule is never listed.
func findDuplicates(rules []*cpi.SecurityRule) ([]duplicateRule, error) {
	type positioned struct {
		rule *cpi.SecurityRule
		pos  int
	}

	buckets := make(map[string][]positioned)

	for _, rule := range rules {
		pos, err := strconv.Atoi(rule.ID)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrPruneBadPosition, rule.ID)
		}

		key := ruleKey(rule)
		buckets[key] = append(buckets[key], positioned{rule, pos})
	}

	var dups []duplicateRule

	for key, members := range buckets {
		sort.Slice(members, func(i, j int) bool { return members[i].pos < members[j].pos })

		for _, dup := range members[1:] {
			dups = append(dups, duplicateRule{rule: dup.rule, pos: dup.pos, key: key, keep: members[0].rule})
		}
	}

	sort.Slice(dups, func(i, j int) bool { return dups[i].pos > dups[j].pos })

	return dups, nil
}

// verifyNextTarget checks that the group still holds, at the target
// position, the rule we planned to delete, and that its surviving twin is
// still in place. It returns the number of rules in the group.
func verifyNextTarget(rules []*cpi.SecurityRule, target duplicateRule) (int, error) {
	targetID := strconv.Itoa(target.pos)

	var found, twin bool

	for _, rule := range rules {
		if ruleKey(rule) != target.key {
			continue
		}

		switch rule.ID {
		case targetID:
			found = true
		case target.keep.ID:
			twin = true
		}
	}

	if !found || !twin {
		return 0, fmt.Errorf("%w: rule at position %d is not the expected duplicate (%s)", ErrPruneUnexpectedRule, target.pos, describeRule(target.rule))
	}

	return len(rules), nil
}

// pruneGroup deletes a group's planned duplicates, highest position first.
// PVE renumbers the rules behind a deleted one, so after each delete the
// group is listed again and the next target must still be the rule we
// expected before it is removed.
func pruneGroup(ctx context.Context, security cpi.SecurityManager, group *cpi.SecurityGroup, rules []*cpi.SecurityRule, dups []duplicateRule, out io.Writer) (int, error) {
	deleted := 0
	current := rules

	for _, target := range dups {
		count, err := verifyNextTarget(current, target)
		if err != nil {
			return deleted, fmt.Errorf("group %s: %w", group.Name, err)
		}

		err = security.RemoveSecurityRule(ctx, group.ID, strconv.Itoa(target.pos))
		if err != nil {
			return deleted, fmt.Errorf("group %s: removing rule %d: %w", group.Name, target.pos, err)
		}

		deleted++

		_, _ = fmt.Fprintf(out, "deleted %s/%d\n", group.Name, target.pos)

		current, err = security.ListSecurityRules(ctx, group.ID)
		if err != nil {
			return deleted, fmt.Errorf("group %s: re-listing after delete: %w", group.Name, err)
		}

		if len(current) != count-1 {
			return deleted, fmt.Errorf("group %s: %w: %d rules before the delete and %d after", group.Name, ErrPruneUnexpectedRule, count, len(current))
		}
	}

	return deleted, nil
}

// pruneDuplicateRules lists, and with apply set deletes, exact duplicate
// rules in the security groups for which owned returns true. It never adds,
// reorders, or edits a rule, and it never removes the last copy of one.
func pruneDuplicateRules(ctx context.Context, security cpi.SecurityManager, owned func(groupName string) bool, apply bool, out io.Writer) (pruneResult, error) {
	var result pruneResult

	groups, err := security.ListSecurityGroups(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("failed to list security groups: %w", err)
	}

	for _, group := range groups {
		if !owned(group.Name) {
			continue
		}

		rules, err := security.ListSecurityRules(ctx, group.ID)
		if err != nil {
			return result, fmt.Errorf("failed to list rules for group %s: %w", group.Name, err)
		}

		dups, err := findDuplicates(rules)
		if err != nil {
			return result, fmt.Errorf("group %s: %w", group.Name, err)
		}

		for _, dup := range dups {
			_, _ = fmt.Fprintf(out, "%s/%d  %s  (duplicate of %s/%s)\n", group.Name, dup.pos, describeRule(dup.rule), group.Name, dup.keep.ID)
		}

		result.Duplicates += len(dups)

		verb := "would delete"

		if apply && len(dups) > 0 {
			deleted, err := pruneGroup(ctx, security, group, rules, dups, out)
			result.Deleted += deleted

			if err != nil {
				return result, err
			}

			verb = "deleted"
		}

		_, _ = fmt.Fprintf(out, "%s: %d duplicate rule(s) of %d, %s %d\n", group.Name, len(dups), len(rules), verb, len(dups))
	}

	if apply {
		_, _ = fmt.Fprintf(out, "Total: %d duplicate rule(s) found, %d deleted\n", result.Duplicates, result.Deleted)
	} else {
		_, _ = fmt.Fprintf(out, "Total: %d duplicate rule(s) found, none deleted (dry run; use --apply to delete)\n", result.Duplicates)
	}

	return result, nil
}

// runPruneDuplicateRules prunes the provider's owned groups and does nothing
// else.
func runPruneDuplicateRules(ctx context.Context, cfg *config.Config, provider cpi.Provider, blocName string, apply bool, out io.Writer) error {
	err := validatePruneProvider(cfg)
	if err != nil {
		return err
	}

	security := provider.SecurityManager()
	if security == nil {
		return ErrProviderDoesNotSupportSecurityMgmt
	}

	ruleDefs := bootstrap.DefaultSecurityGroupRules(cfg)

	_, err = pruneDuplicateRules(ctx, security, func(name string) bool {
		return ownedSecurityGroup(blocName, name, ruleDefs)
	}, apply, out)

	return err
}

// validatePruneProvider refuses providers other than PVE. Pruning deletes a
// rule by its numeric position in the group, which only PVE reports. Other
// providers identify rules by UUIDs or composite names, so their rules could
// not be listed or deleted this way.
func validatePruneProvider(cfg *config.Config) error {
	if !strings.EqualFold(strings.TrimSpace(cfg.Provider), "pve") {
		return fmt.Errorf("%w (this bloc uses %q)", ErrPruneProviderUnsupported, cfg.Provider)
	}

	return nil
}
