package bastion

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		desired string
		want    string
	}{
		{
			name:    "identical content produces no diff",
			current: "PATH=/usr/bin\n",
			desired: "PATH=/usr/bin\n",
			want:    "",
		},
		{
			name:    "changed line counts only the real lines",
			current: "export A=1\nexport B=2\nexport C=3\n",
			desired: "export A=1\nexport B=9\nexport C=3\n",
			want: "--- f (current)\n" +
				"+++ f (proposed)\n" +
				"@@ -1,3 +1,3 @@\n" +
				" export A=1\n" +
				"-export B=2\n" +
				"+export B=9\n" +
				" export C=3\n",
		},
		{
			name:    "creating from empty uses a zero-length from range",
			current: "",
			desired: "PATH=/usr/bin\nLANG=C\n",
			want: "--- f (current)\n" +
				"+++ f (proposed)\n" +
				"@@ -0,0 +1,2 @@\n" +
				"+PATH=/usr/bin\n" +
				"+LANG=C\n",
		},
		{
			name:    "appended line",
			current: "PATH=/usr/bin\n",
			desired: "PATH=/usr/bin\nLANG=C\n",
			want: "--- f (current)\n" +
				"+++ f (proposed)\n" +
				"@@ -1 +1,2 @@\n" +
				" PATH=/usr/bin\n" +
				"+LANG=C\n",
		},
		{
			name:    "missing trailing newline is reported",
			current: "a\nb",
			desired: "a\nc",
			want: "--- f (current)\n" +
				"+++ f (proposed)\n" +
				"@@ -1,2 +1,2 @@\n" +
				" a\n" +
				"-b\n" +
				"\\ No newline at end of file\n" +
				"+c\n" +
				"\\ No newline at end of file\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := unifiedDiff("f (current)", "f (proposed)", tt.current, tt.desired)
			if got != tt.want {
				t.Errorf("unifiedDiff() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tt.want)
			}
		})
	}
}

// TestUnifiedDiffContextWindow pins the context window to diffContextLines so a
// large file reports only the lines surrounding each change.
func TestUnifiedDiffContextWindow(t *testing.T) {
	t.Parallel()

	lines := make([]string, 0, 20)
	for i := range 20 {
		lines = append(lines, "line"+string(rune('a'+i)))
	}

	current := strings.Join(lines, "\n") + "\n"
	changed := append([]string(nil), lines...)
	changed[10] = "CHANGED"
	desired := strings.Join(changed, "\n") + "\n"

	got := unifiedDiff("f (current)", "f (proposed)", current, desired)

	if !strings.Contains(got, "-line"+string(rune('a'+10))) {
		t.Errorf("expected the removed line in the diff, got:\n%s", got)
	}

	if !strings.Contains(got, "+CHANGED") {
		t.Errorf("expected the added line in the diff, got:\n%s", got)
	}

	// 3 lines of context either side of a single changed line means the hunk
	// spans 7 lines and starts at line 8.
	if !strings.Contains(got, "@@ -8,7 +8,7 @@") {
		t.Errorf("expected a %d-line context window, got:\n%s", diffContextLines, got)
	}

	// The first and last lines fall outside the context window.
	if strings.Contains(got, "linea\n") {
		t.Errorf("first line should be outside the context window, got:\n%s", got)
	}
}
