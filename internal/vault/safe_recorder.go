package vault

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// PlannedWrite records one vault path a dry-run populate would write, with
// the key names only — values are deliberately not retained for display so a
// plan can never leak a secret.
type PlannedWrite struct {
	Path string
	Keys []string
}

// recordingSafe is the dry-run seam: a SafeInterface decorator that records
// writes instead of forwarding them, while keeping reads working. Reads see a
// read-your-writes overlay — providers read back records they wrote earlier
// in the same populate run (subnet records feeding reserved-ips, for one), so
// recorded writes must be visible to subsequent reads, merged over the
// underlying data.
type recordingSafe struct {
	under SafeInterface

	// order preserves first-write path order for stable plan output.
	order   []string
	pending map[string]map[string]interface{}
}

func newRecordingSafe(under SafeInterface) *recordingSafe {
	return &recordingSafe{
		under:   under,
		order:   nil,
		pending: map[string]map[string]interface{}{},
	}
}

// Plan returns the recorded writes in first-write path order, key names
// sorted within each path.
func (r *recordingSafe) Plan() []PlannedWrite {
	plan := make([]PlannedWrite, 0, len(r.order))

	for _, path := range r.order {
		keys := make([]string, 0, len(r.pending[path]))
		for k := range r.pending[path] {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		plan = append(plan, PlannedWrite{Path: path, Keys: keys})
	}

	return plan
}

// record captures key/value pairs for a path without touching the underlying
// safe. Values are kept only for the read overlay, never for display.
func (r *recordingSafe) record(path string, data map[string]interface{}) {
	if _, ok := r.pending[path]; !ok {
		r.pending[path] = map[string]interface{}{}
		r.order = append(r.order, path)
	}

	for k, v := range data {
		r.pending[path][k] = v
	}
}

func (r *recordingSafe) Set(path, key string, value interface{}) error {
	r.record(path, map[string]interface{}{key: value})

	return nil
}

func (r *recordingSafe) SetMultiple(path string, data map[string]interface{}) error {
	r.record(path, data)

	return nil
}

func (r *recordingSafe) Import(path string, data map[string]interface{}) error {
	r.record(path, data)

	return nil
}

// Delete is recorded as a no-op: populate flows do not delete, and a dry-run
// must not remove anything either way.
func (r *recordingSafe) Delete(_, _ string) error {
	return nil
}

func (r *recordingSafe) Get(path, key string) (interface{}, error) {
	if d, ok := r.pending[path]; ok {
		if v, ok := d[key]; ok {
			return v, nil
		}
	}

	return r.under.Get(path, key)
}

func (r *recordingSafe) GetAll(path string) (map[string]interface{}, error) {
	overlay, hasOverlay := r.pending[path]

	underData, err := r.under.GetAll(path)
	if err != nil {
		if !hasOverlay {
			return nil, err
		}

		underData = map[string]interface{}{}
	}

	merged := make(map[string]interface{}, len(underData)+len(overlay))
	for k, v := range underData {
		merged[k] = v
	}

	for k, v := range overlay {
		merged[k] = v
	}

	return merged, nil
}

func (r *recordingSafe) Exists(path string) (bool, error) {
	if _, ok := r.pending[path]; ok {
		return true, nil
	}

	return r.under.Exists(path)
}

// List merges the underlying listing with the immediate children of path
// among recorded writes, so a provider enumerating what it has written so
// far sees a consistent view.
func (r *recordingSafe) List(path string) ([]string, error) {
	entries, err := r.under.List(path)
	if err != nil {
		entries = nil
	}

	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		seen[e] = true
	}

	prefix := strings.TrimSuffix(path, "/") + "/"

	for _, p := range r.order {
		if !strings.HasPrefix(p, prefix) {
			continue
		}

		child := strings.SplitN(strings.TrimPrefix(p, prefix), "/", 2)[0]
		if !seen[child] {
			seen[child] = true

			entries = append(entries, child)
		}
	}

	if len(entries) == 0 && err != nil {
		return nil, err
	}

	return entries, nil
}

func (r *recordingSafe) Export(path string) (map[string]interface{}, error) {
	return r.under.Export(path)
}

func (r *recordingSafe) GetEngineInfo(path string) (*EngineInfo, error) {
	return r.under.GetEngineInfo(path)
}

func (r *recordingSafe) MustGet(path, key string) interface{} {
	value, err := r.Get(path, key)
	if err != nil {
		panic(fmt.Sprintf("failed to get %s:%s - %v", path, key, err))
	}

	return value
}

func (r *recordingSafe) GetString(path, key string) (string, error) {
	value, err := r.Get(path, key)
	if err != nil {
		return "", err
	}

	if str, ok := value.(string); ok {
		return str, nil
	}

	return "", ErrValueNotStringAtPath(path, key)
}

func (r *recordingSafe) GetJSON(path, key string) ([]byte, error) {
	value, err := r.Get(path, key)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal value: %w", err)
	}

	return data, nil
}

// Ensure recordingSafe implements SafeInterface.
var _ SafeInterface = (*recordingSafe)(nil)

// writePopulatePlan renders a dry-run populate plan: the resolved target
// vault and every path with the key names that would be written. Key names
// only — never values. Write errors are ignored: the plan goes to a console
// writer and a failed print must not fail the dry-run.
func writePopulatePlan(w io.Writer, target string, writes []PlannedWrite) {
	_, _ = fmt.Fprintf(w, "[DRY RUN] vault populate plan — target: %s\n", target)

	if len(writes) == 0 {
		_, _ = fmt.Fprintln(w, "[DRY RUN] no writes would be performed")

		return
	}

	keyCount := 0

	for _, pw := range writes {
		keyCount += len(pw.Keys)

		_, _ = fmt.Fprintf(w, "[DRY RUN] would write %s: %s\n", pw.Path, strings.Join(pw.Keys, ", "))
	}

	_, _ = fmt.Fprintf(w, "[DRY RUN] %d paths, %d keys; no changes made\n", len(writes), keyCount)
}
