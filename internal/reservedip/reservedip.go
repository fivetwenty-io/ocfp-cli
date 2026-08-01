// Package reservedip computes per-tier reserved IP addresses for a subnet
// from its CIDR plus a static per-envType assignment table. It is the shared
// engine behind the STACKIT and PVE vault providers: both compute reserved
// IPs the same way (offset-from-subnet-base singles, plus available/reserved
// range pairs), differing only in the assignment table each provider passes
// in. Keeping the engine in one place means a fix or behavior change applies
// identically everywhere it is used.
package reservedip

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// CIDRPartsCount is the expected number of parts when splitting a CIDR
// notation string ("10.0.0.0/24" -> 2 parts).
const CIDRPartsCount = 2

// IPv4OctetCount is the number of dotted-decimal octets in an IPv4 address.
const IPv4OctetCount = 4

// Sentinel errors for malformed CIDR/IP input.
var (
	ErrInvalidIPAddressFormat = errors.New("invalid IP address format")
)

// ErrOffsetBeyondSubnet is the sentinel a caller can errors.Is-check for
// when an assignment table entry resolves to an offset (or range endpoint)
// past a subnet's last usable host offset (see CalculateLastHostOffset), or
// to a range whose end precedes its start. Calculate wraps this sentinel
// with the offending offset/range and the subnet's limit; use
// errors.Is(err, ErrOffsetBeyondSubnet) rather than matching the message.
var ErrOffsetBeyondSubnet = errors.New("offset beyond subnet bounds")

// ErrInvalidCIDRFormat returns an error for a malformed CIDR notation string.
func ErrInvalidCIDRFormat(cidr string) error {
	return fmt.Errorf("invalid CIDR format: %s", cidr) //nolint:err113 // dynamic error with context
}

// Logger is the minimal structured-logging surface Calculate needs.
// *zap.SugaredLogger satisfies this interface without this package
// importing zap directly. A nil Logger is valid: all calls become no-ops.
type Logger interface {
	Debugw(msg string, keysAndValues ...any)
	Warnw(msg string, keysAndValues ...any)
}

// noopLogger is used internally whenever Calculate is called with a nil
// Logger, so call sites never need a nil check of their own.
type noopLogger struct{}

func (noopLogger) Debugw(string, ...any) {}
func (noopLogger) Warnw(string, ...any)  {}

// Assignment describes how a single named role (or the "available"/
// "reserved" pseudo-roles) is placed within a subnet. Exactly one of the
// four forms should be populated:
//
//   - Offset: a single IP at this offset from the subnet base, regardless of
//     subnet index.
//   - SubnetMapping: a single IP at the given offset, but only on the
//     listed subnet indices (offset -> []subnetNum).
//   - RangeSpec: one or more IP ranges (e.g. "11-29" or "0-10,30->"),
//     applied regardless of subnet index.
//   - SubnetRanges: one or more IP ranges, but only on the listed subnet
//     indices (range-spec -> []subnetNum).
//
// Offset (and SubnetMapping) default to writing their single computed IP
// under the key "{assignmentType}_ip" (plus "{assignmentType}_a"/"_b" bound
// keys). IPKey overrides that key for the Offset case only, for the rare
// role whose consumer reads an exact literal key that isn't
// "{assignmentType}_ip" — e.g. a smoke-test errand's dedicated static IP,
// which a kit reads as "{role}_ip_smoke" alongside (not instead of) the
// role's own "{role}_ip" (see internal/vault/pve_reserved_ips.go's rustfs/
// garage smoke assignments). Leave empty for the default behavior.
type Assignment struct {
	Offset        int
	SubnetMapping map[int][]int
	RangeSpec     string
	SubnetRanges  map[string][]int
	IPKey         string
}

// IPRange is an inclusive [Start, End] pair of dotted-decimal IPv4 addresses.
type IPRange struct {
	Start string
	End   string
}

// AssignmentTable maps an assignment/role name (e.g. "bosh", "available") to
// a per-envType Assignment. An "other" envType key is the fallback used when
// the requested envType has no explicit entry.
type AssignmentTable map[string]map[string]*Assignment

// Calculate computes every reserved IP for one subnet from its CIDR, the
// assignment table, and the requesting envType/subnetNum. assignmentTypes
// are processed in the order given by priority (ties broken alphabetically,
// unlisted types processed last, also alphabetically) so output is
// deterministic across runs. A nil log is accepted and treated as a no-op
// sink.
//
// Returns an error when cidr itself fails to parse, or when a resolved
// offset or range endpoint exceeds the subnet's last usable host offset, or
// a range's end precedes its start (wrapping ErrOffsetBeyondSubnet in the
// latter two cases). A malformed individual RangeSpec (non-numeric, not an
// out-of-bounds question) is instead logged as a warning and skipped rather
// than aborting the whole subnet, matching the reference (Perl)
// implementation's best-effort behavior for that specific failure mode.
func Calculate(
	cidr string,
	assignments AssignmentTable,
	envType string,
	subnetNum int,
	priority map[string]int,
	log Logger,
) (map[string]any, error) {
	if log == nil {
		log = noopLogger{}
	}

	baseIP, networkBits, err := ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %s: %w", cidr, err)
	}

	vaultIPs := make(map[string]any)
	usedIPs := make(map[string]bool)

	assignmentTypes := make([]string, 0, len(assignments))
	for assignmentType := range assignments {
		assignmentTypes = append(assignmentTypes, assignmentType)
	}

	SortAssignmentTypes(assignmentTypes, priority)

	for _, assignmentType := range assignmentTypes {
		envMap := assignments[assignmentType]

		assignment := envMap[envType]
		if assignment == nil {
			assignment = envMap["other"]
		}

		if assignment == nil {
			continue
		}

		if err := processAssignment(assignmentType, assignment, baseIP, networkBits, subnetNum, vaultIPs, usedIPs, log); err != nil {
			return nil, err
		}
	}

	return vaultIPs, nil
}

// processAssignment dispatches a single reserved IP assignment to the
// appropriate processor based on which field of Assignment is populated.
// Returns a non-nil error (wrapping ErrOffsetBeyondSubnet) only when the
// assignment resolves to an offset or range endpoint past the subnet's
// bounds, or an inverted range; a nil return means the assignment (or a
// malformed RangeSpec within it, which is logged and skipped instead) was
// handled.
func processAssignment(
	assignmentType string,
	assignment *Assignment,
	baseIP string,
	networkBits, subnetNum int,
	vaultIPs map[string]any,
	usedIPs map[string]bool,
	log Logger,
) error {
	switch {
	// Offset == 0 is deliberately excluded (not just "unset"): offset 0 is
	// always the subnet's network address, which is never a valid host
	// assignment, so an Assignment can never mean "put this role at the
	// network address" via Offset. A role that genuinely needs the network
	// address (there is none today) would have to use RangeSpec "0" instead.
	case assignment.Offset > 0:
		return processOffsetAssignment(assignmentType, assignment.Offset, assignment.IPKey, baseIP, networkBits, vaultIPs, usedIPs, log)
	case len(assignment.SubnetMapping) > 0:
		return processSubnetMappingAssignment(assignmentType, assignment.SubnetMapping, baseIP, networkBits, subnetNum, vaultIPs, usedIPs, log)
	case assignment.RangeSpec != "":
		return processRangeSpecAssignment(assignmentType, assignment.RangeSpec, baseIP, networkBits, vaultIPs, log)
	case len(assignment.SubnetRanges) > 0:
		return processSubnetRangesAssignment(assignmentType, assignment.SubnetRanges, baseIP, networkBits, subnetNum, vaultIPs, log)
	}

	return nil
}

// processOffsetAssignment handles a simple offset-based single IP
// reservation. ipKey, when non-empty, overrides the default
// "{assignmentType}_ip" output key (see Assignment.IPKey); the "_a"/"_b"
// bound keys always key off ipKey too when it is set, so a custom-keyed
// role gets the same bound-key convention as the default case. Returns
// ErrOffsetBeyondSubnet (wrapped with the offending offset and the subnet's
// last usable host offset) if offset exceeds the subnet's bounds.
func processOffsetAssignment(
	assignmentType string, offset int, ipKey string, baseIP string, networkBits int,
	vaultIPs map[string]any, usedIPs map[string]bool, log Logger,
) error {
	if limit := CalculateLastHostOffset(networkBits); offset > limit {
		return fmt.Errorf("%s: offset %d exceeds last usable host offset %d: %w",
			assignmentType, offset, limit, ErrOffsetBeyondSubnet)
	}

	ip := AddOffsetToIP(baseIP, offset) //nolint:varnamelen // ip is clear in context
	if usedIPs[ip] {
		return nil
	}

	key := assignmentType + "_ip"
	if ipKey != "" {
		key = ipKey
	}

	vaultIPs[key] = ip
	usedIPs[ip] = true

	// Add IP bounds (_a and _b) for Genesis compatibility.
	vaultIPs[key+"_a"] = AddOffsetToIP(baseIP, offset-1)
	vaultIPs[key+"_b"] = AddOffsetToIP(baseIP, offset+1)

	log.Debugw("Reserved IP", "type", assignmentType, "key", key, "ip", ip, "offset", offset)

	return nil
}

// processSubnetMappingAssignment handles offset-to-subnet-number mapping
// reservations. Returns ErrOffsetBeyondSubnet (wrapped with the offending
// offset and the subnet's last usable host offset) if the offset matching
// subnetNum exceeds the subnet's bounds.
func processSubnetMappingAssignment(
	assignmentType string, subnetMapping map[int][]int, baseIP string, networkBits int,
	subnetNum int, vaultIPs map[string]any, usedIPs map[string]bool, log Logger,
) error {
	limit := CalculateLastHostOffset(networkBits)

	for offset, subnets := range subnetMapping {
		if !ContainsInt(subnets, subnetNum) {
			continue
		}

		if offset > limit {
			return fmt.Errorf("%s: offset %d exceeds last usable host offset %d: %w",
				assignmentType, offset, limit, ErrOffsetBeyondSubnet)
		}

		ip := AddOffsetToIP(baseIP, offset) //nolint:varnamelen // ip is clear in context
		if usedIPs[ip] {
			break
		}

		vaultIPs[assignmentType+"_ip"] = ip
		usedIPs[ip] = true
		vaultIPs[assignmentType+"_a"] = AddOffsetToIP(baseIP, offset-1)
		vaultIPs[assignmentType+"_b"] = AddOffsetToIP(baseIP, offset+1)

		log.Debugw("Reserved IP from subnet mapping",
			"type", assignmentType, "ip", ip, "offset", offset, "subnet_num", subnetNum)

		break
	}

	return nil
}

// processRangeSpecAssignment handles range-specification-based IP
// reservations. A range endpoint past the subnet's bounds, or an inverted
// range, is fatal: it propagates as ErrOffsetBeyondSubnet rather than being
// skipped, since (unlike a malformed spec string) it is well-formed but
// semantically invalid for this subnet. A malformed (non-numeric) spec is
// still logged and skipped, matching the pre-existing best-effort behavior.
func processRangeSpecAssignment(
	assignmentType, rangeSpec, baseIP string, networkBits int,
	vaultIPs map[string]any, log Logger,
) error {
	ranges, err := ParseIPRangeSpec(rangeSpec, baseIP, networkBits)
	if err != nil {
		if errors.Is(err, ErrOffsetBeyondSubnet) {
			return fmt.Errorf("%s: %w", assignmentType, err)
		}

		log.Warnw("Failed to parse range spec",
			"type", assignmentType, "spec", rangeSpec, "error", err)

		return nil
	}

	storeIPRanges(assignmentType, ranges, vaultIPs, "Reserved IP range", 0, log)

	return nil
}

// processSubnetRangesAssignment handles subnet-specific range-based IP
// reservations. Bounds/inversion errors on the subnetNum-matching spec are
// fatal (see processRangeSpecAssignment); a malformed spec is logged and
// skipped, and the loop continues to the next spec in the map.
func processSubnetRangesAssignment(
	assignmentType string, subnetRanges map[string][]int, baseIP string,
	networkBits, subnetNum int, vaultIPs map[string]any, log Logger,
) error {
	for rangeSpec, subnets := range subnetRanges {
		if !ContainsInt(subnets, subnetNum) {
			continue
		}

		ranges, err := ParseIPRangeSpec(rangeSpec, baseIP, networkBits)
		if err != nil {
			if errors.Is(err, ErrOffsetBeyondSubnet) {
				return fmt.Errorf("%s: %w", assignmentType, err)
			}

			log.Warnw("Failed to parse subnet range spec",
				"type", assignmentType, "spec", rangeSpec, "error", err)

			continue
		}

		storeIPRanges(assignmentType, ranges, vaultIPs, "Reserved IP range from subnet mapping", subnetNum, log)

		break
	}

	return nil
}

// storeIPRanges writes IP range boundaries into the vault data map and logs them.
func storeIPRanges(
	assignmentType string, ranges []IPRange,
	vaultIPs map[string]any, logMessage string, subnetNum int, log Logger,
) {
	idx := 0
	for _, rng := range ranges {
		vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = rng.Start
		idx++
		vaultIPs[fmt.Sprintf("%s_%d", assignmentType, idx)] = rng.End
		idx++

		logFields := []any{"type", assignmentType, "start", rng.Start, "end", rng.End}
		if subnetNum > 0 {
			logFields = append(logFields, "subnet_num", subnetNum)
		}

		log.Debugw(logMessage, logFields...)
	}
}

// ParseCIDR parses a CIDR notation and returns base IP and network bits.
func ParseCIDR(cidr string) (string, int, error) {
	parts := strings.Split(cidr, "/")
	if len(parts) != CIDRPartsCount {
		return "", 0, ErrInvalidCIDRFormat(cidr)
	}

	baseIP := parts[0]
	networkBits := 0

	_, err := fmt.Sscanf(parts[1], "%d", &networkBits)
	if err != nil {
		return "", 0, fmt.Errorf("invalid network bits: %w", err)
	}

	// Validate IP address format
	octets := strings.Split(baseIP, ".")
	if len(octets) != IPv4OctetCount {
		return "", 0, ErrInvalidIPAddressFormat
	}

	return baseIP, networkBits, nil
}

// AddOffsetToIP adds an offset (which may be negative) to an IP address,
// using full 32-bit arithmetic so a carry/borrow that crosses more than one
// octet boundary (e.g. base=10.64.255.250, offset=20 => 10.65.0.14; a /16's
// open-ended "N->" range can need this for offsets up into the tens of
// thousands) resolves correctly rather than only carrying into the
// immediately adjacent octet. Returns baseIP unchanged if it is not a
// well-formed dotted-decimal IPv4 address, or if applying offset would fall
// outside the representable unsigned 32-bit IPv4 address space (result < 0
// or > 4294967295) — the offset tables this package drives never require
// wrapping past the address space, so returning the unmodified input is
// safer than producing a silently wrong IP.
func AddOffsetToIP(baseIP string, offset int) string {
	parsed := net.ParseIP(baseIP)
	if parsed == nil {
		return baseIP
	}

	v4 := parsed.To4()
	if v4 == nil {
		return baseIP
	}

	const (
		octetShift24 = 24
		octetShift16 = 16
		octetShift8  = 8
		octetMask    = 0xFF
	)

	base := uint32(v4[0])<<octetShift24 | uint32(v4[1])<<octetShift16 | uint32(v4[2])<<octetShift8 | uint32(v4[3])

	result := int64(base) + int64(offset)
	if result < 0 || result > int64(^uint32(0)) {
		return baseIP
	}

	r := uint32(result) //nolint:varnamelen // r is clear in context

	return net.IPv4(
		byte(r>>octetShift24&octetMask), byte(r>>octetShift16&octetMask), byte(r>>octetShift8&octetMask), byte(r&octetMask),
	).String()
}

// ParseIPRangeSpec parses a range specification like "11-29" or "0-10,30->"
// into concrete IP ranges relative to baseIP. networkBits is used to resolve
// the open-ended "N->" form to the subnet's last usable host offset, and to
// bounds-check every resolved offset against that same limit.
//
// Returns an error for a malformed (non-numeric, wrong shape) subrange
// string. Returns an error wrapping ErrOffsetBeyondSubnet, distinguishable
// via errors.Is, when a resolved offset exceeds the subnet's last usable
// host offset, or when a resolved range's end precedes its start — both are
// well-formed but semantically invalid for the given subnet.
func ParseIPRangeSpec(rangeSpec string, baseIP string, networkBits int) ([]IPRange, error) {
	subranges := strings.Split(rangeSpec, ",")
	ranges := make([]IPRange, 0, len(subranges))
	limit := CalculateLastHostOffset(networkBits)

	for _, subrange := range subranges {
		subrange = strings.TrimSpace(subrange)
		if subrange == "" {
			continue
		}

		var lower, upper int

		switch {
		case strings.Contains(subrange, "->"):
			parts := strings.Split(subrange, "->")

			lowerVal, err := strconv.Atoi(strings.TrimSpace(parts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid range spec %q: %w", subrange, err)
			}

			lower = lowerVal
			upper = limit
		case strings.Contains(subrange, "-"):
			parts := strings.Split(subrange, "-")
			if len(parts) != CIDRPartsCount {
				return nil, fmt.Errorf("invalid range spec %q", subrange) //nolint:err113 // dynamic error with context
			}

			lowerVal, err := strconv.Atoi(strings.TrimSpace(parts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid range spec %q: %w", subrange, err)
			}

			upperVal, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid range spec %q: %w", subrange, err)
			}

			lower, upper = lowerVal, upperVal
		default:
			val, err := strconv.Atoi(subrange)
			if err != nil {
				return nil, fmt.Errorf("invalid range spec %q: %w", subrange, err)
			}

			lower, upper = val, val
		}

		if lower > limit {
			return nil, fmt.Errorf("range spec %q: start offset %d exceeds last usable host offset %d: %w",
				subrange, lower, limit, ErrOffsetBeyondSubnet)
		}

		if upper > limit {
			return nil, fmt.Errorf("range spec %q: end offset %d exceeds last usable host offset %d: %w",
				subrange, upper, limit, ErrOffsetBeyondSubnet)
		}

		if upper < lower {
			return nil, fmt.Errorf("range spec %q: end offset %d is before start offset %d: %w",
				subrange, upper, lower, ErrOffsetBeyondSubnet)
		}

		startIP := AddOffsetToIP(baseIP, lower)
		endIP := AddOffsetToIP(baseIP, upper)

		ranges = append(ranges, IPRange{Start: startIP, End: endIP})
	}

	return ranges, nil
}

// CalculateLastHostOffset returns the last usable host offset (relative to
// the subnet base) for a network of the given prefix length, i.e. the
// broadcast address offset minus one (2^(32-networkBits) - 2).
func CalculateLastHostOffset(networkBits int) int {
	const ipv4Bits = 32

	hostBits := ipv4Bits - networkBits
	if hostBits <= 0 {
		return 0
	}

	const nonHostAddrs = 2 // network address + broadcast address

	maxHosts := (1 << uint(hostBits)) - nonHostAddrs

	return maxHosts
}

// ContainsInt reports whether slice contains value.
func ContainsInt(slice []int, value int) bool {
	for _, v := range slice {
		if v == value {
			return true
		}
	}

	return false
}

// SortAssignmentTypes sorts types in place by priority (lower first), with
// ties and unlisted types (priority 0, or absent from the map) sorted
// alphabetically after every listed type. This makes Calculate's output
// order deterministic regardless of Go's randomized map iteration.
func SortAssignmentTypes(types []string, priority map[string]int) {
	const unlisted = 1 << 30

	rank := func(t string) int {
		if p, ok := priority[t]; ok && p != 0 {
			return p
		}

		return unlisted
	}

	for i := range len(types) - 1 { //nolint:varnamelen // i is clear in context
		for j := i + 1; j < len(types); j++ { //nolint:varnamelen // j is clear in context
			pri1 := rank(types[i])
			pri2 := rank(types[j])

			if pri1 > pri2 || (pri1 == pri2 && types[i] > types[j]) {
				types[i], types[j] = types[j], types[i]
			}
		}
	}
}
