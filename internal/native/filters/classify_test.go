package filters

import "testing"

func TestClassifyVerdicts(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		expected Verdict
	}{
		// SkipOghamLoop: anything matching the curated prefix list
		{"mcp tool", "mcp__ogham__store_memory", SkipOghamLoop},
		{"ogham underscore", "ogham_search", SkipOghamLoop},
		{"store_memory direct", "store_memory", SkipOghamLoop},
		{"hybrid_search direct", "hybrid_search", SkipOghamLoop},

		// SkipAlwaysSkip: from hooks_config.yaml always_skip_tools
		{"Read tool", "Read", SkipAlwaysSkip},
		{"Glob tool", "Glob", SkipAlwaysSkip},
		{"Grep tool", "Grep", SkipAlwaysSkip},
		{"WebFetch tool", "WebFetch", SkipAlwaysSkip},

		// CaptureRoutine: from routine_tools (Bash only at v0.1.0)
		{"Bash routine", "Bash", CaptureRoutine},

		// CaptureResponseGated: Edit and Write only fire when their
		// content/response clears the importance gate downstream.
		{"Edit gated", "Edit", CaptureResponseGated},
		{"Write gated", "Write", CaptureResponseGated},

		// SkipNotConfigured: anything not in any list
		{"unknown tool", "SomeRandomTool", SkipNotConfigured},
		{"LS not configured", "LS", SkipNotConfigured},
		{"empty tool", "", SkipNotConfigured},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.tool)
			if got != tc.expected {
				t.Errorf("Classify(%q) = %v, want %v", tc.tool, got, tc.expected)
			}
		})
	}
}

func TestVerdictShouldCapture(t *testing.T) {
	cases := []struct {
		verdict  Verdict
		expected bool
	}{
		{CaptureRoutine, true},
		{CaptureResponseGated, true},
		{SkipOghamLoop, false},
		{SkipAlwaysSkip, false},
		{SkipNotConfigured, false},
	}
	for _, tc := range cases {
		got := tc.verdict.ShouldCapture()
		if got != tc.expected {
			t.Errorf("%v.ShouldCapture() = %v, want %v", tc.verdict, got, tc.expected)
		}
	}
}

func TestVerdictStringLabels(t *testing.T) {
	cases := map[Verdict]string{
		SkipOghamLoop:        "skip:ogham_loop",
		SkipAlwaysSkip:       "skip:always_skip",
		SkipNotConfigured:    "skip:not_configured",
		CaptureRoutine:       "capture:routine",
		CaptureResponseGated: "capture:response_gated",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Errorf("Verdict(%d).String() = %q, want %q", v, got, want)
		}
	}
}
