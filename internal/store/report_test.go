package store

import "testing"

func TestNormalizeReportStatus(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "empty means every status", ok: true},
		{name: "whitespace", raw: "   ", ok: true},
		{name: "canonical", raw: "SUBMITTED", want: ReportStatusSubmitted, ok: true},
		{name: "lower case", raw: "approved", want: ReportStatusApproved, ok: true},
		{name: "mixed case and padding", raw: " ReJected ", want: ReportStatusRejected, ok: true},
		{name: "legacy draft", raw: "draft", want: ReportStatusDraft, ok: true},
		{name: "outside the vocabulary", raw: "PUBLISHED"},
		{name: "not a status at all", raw: "'; DROP TABLE reports--"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := NormalizeReportStatus(testCase.raw)
			if ok != testCase.ok {
				t.Fatalf("NormalizeReportStatus(%q) ok = %t, want %t", testCase.raw, ok, testCase.ok)
			}
			if ok && got != testCase.want {
				t.Fatalf("NormalizeReportStatus(%q) = %q, want %q", testCase.raw, got, testCase.want)
			}
		})
	}
}
