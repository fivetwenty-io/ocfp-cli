package netlayout

import (
	"reflect"
	"testing"
)

func TestBuiltinTablesMatchLegacyLayouts(t *testing.T) {
	for name, legacy := range map[string]Layout{"wide": wideLayout{}, "compact": compactLayout{}} {
		compiled := builtinLayouts()[name]
		got, _ := compiled.WorkloadTable("10.0.0.0/22")
		want, _ := legacy.WorkloadTable("10.0.0.0/22")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: compiled table diverges from legacy", name)
		}
		if compiled.SchemeVersion() != legacy.SchemeVersion() || compiled.MinPrefix() != legacy.MinPrefix() {
			t.Errorf("%s: identity mismatch", name)
		}
	}
}
