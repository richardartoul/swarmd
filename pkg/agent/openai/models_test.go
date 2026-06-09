// See LICENSE for licensing information

package openai

import "testing"

func TestSplitModelReasoningEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model      string
		wantBase   string
		wantEffort string
	}{
		{model: "gpt-5", wantBase: "gpt-5", wantEffort: ""},
		{model: "gpt-5-xhigh", wantBase: "gpt-5", wantEffort: "high"},
		{model: "gpt-5-xxhigh", wantBase: "gpt-5", wantEffort: "xhigh"},
		{model: "gpt-5-xminimal", wantBase: "gpt-5", wantEffort: "minimal"},
		{model: "gpt-5-xnone", wantBase: "gpt-5", wantEffort: "none"},
		// "-high" without the "-x" marker is part of the model name, not an effort.
		{model: "gpt-5-high", wantBase: "gpt-5-high", wantEffort: ""},
		// A bare suffix with no base model is not an effort marker.
		{model: "-xhigh", wantBase: "-xhigh", wantEffort: ""},
		{model: "  gpt-5-xlow  ", wantBase: "gpt-5", wantEffort: "low"},
	}
	for _, test := range tests {
		base, effort := SplitModelReasoningEffort(test.model)
		if base != test.wantBase || effort != test.wantEffort {
			t.Errorf(
				"SplitModelReasoningEffort(%q) = (%q, %q), want (%q, %q)",
				test.model, base, effort, test.wantBase, test.wantEffort,
			)
		}
	}
}
