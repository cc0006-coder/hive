package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hivecommons/hive/pkg/evidence"
)

func evSummaryBundle(repo string, n int, kind string, created time.Time, merged bool, verdicts []evidence.Verdict, sentinel int, ci bool) *evidence.Bundle {
	b := &evidence.Bundle{
		SchemaVersion: evidence.SchemaVersion, Repo: repo, Number: n, HeadSHA: "abc1234",
		Author: evidence.Author{Login: "x", Kind: kind}, CreatedAt: created, UpdatedAt: created,
		Verdicts: verdicts,
	}
	if merged {
		b.Merge = &evidence.MergeEvent{Actor: "a", Method: "squash", SHA: "m"}
	}
	for i := 0; i < sentinel; i++ {
		b.Sentinel = append(b.Sentinel, evidence.SentinelFinding{Rule: "r"})
	}
	if ci {
		b.CI.Checks = []evidence.Check{{Name: "build", Conclusion: "success"}}
	}
	return b
}

func TestBuildEvidenceSummary(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Hour)
	old := now.Add(-10 * 24 * time.Hour)
	bundles := []*evidence.Bundle{
		evSummaryBundle("a/b", 1, evidence.AuthorAgent, recent.Add(-time.Hour), false, nil, 0, true),
		// newer bundle for the same PR supersedes the one above
		evSummaryBundle("a/b", 1, evidence.AuthorAgent, recent, true, []evidence.Verdict{
			{Perspective: "security", Model: "m1", Verdict: "approve", Confidence: 0.95},
			{Perspective: "security", Model: "", Verdict: "changes_requested", Confidence: 0.3},
		}, 2, true),
		evSummaryBundle("a/b", 2, evidence.AuthorHuman, recent, false, []evidence.Verdict{
			{Perspective: "security", Model: "m1", Verdict: "approve", Confidence: 0.6},
		}, 0, false),
		evSummaryBundle("c/d", 3, evidence.AuthorBot, old, true, nil, 1, true),
	}

	got := buildEvidenceSummary(bundles, now, 24, "", "")
	if got.N != 2 {
		t.Fatalf("n = %d, want 2", got.N)
	}
	if o := got.Outcomes["agent"]; o.N != 1 || o.Merged != 1 || o.Open != 0 {
		t.Errorf("agent outcomes = %+v", o)
	}
	if o := got.Outcomes["human"]; o.N != 1 || o.Open != 1 {
		t.Errorf("human outcomes = %+v", o)
	}
	if got.Missing.VerdictsModelUnknown != 1 || got.Missing.RecordsEmptyCI != 1 {
		t.Errorf("missing = %+v", got.Missing)
	}
	if got.SentinelPerWeek.N != 2 || len(got.SentinelPerWeek.Weeks) != 1 || got.SentinelPerWeek.Weeks[0].Week != "2026-W42" {
		t.Errorf("sentinel = %+v", got.SentinelPerWeek)
	}
	if len(got.Calibration) != 1 || got.Calibration[0].N != 3 {
		t.Fatalf("calibration = %+v", got.Calibration)
	}
	bk := got.Calibration[0].Buckets
	if bk[0].N != 1 || bk[0].Blocks != 1 || bk[1].N != 1 || bk[3].N != 1 || bk[3].Merged != 1 {
		t.Errorf("buckets = %+v", bk)
	}
	if len(got.ReviewerModels) != 2 {
		t.Fatalf("models = %+v", got.ReviewerModels)
	}
	unk := got.ReviewerModels[1]
	if unk.Model != "unknown" || unk.ByAuthor["agent"].MergedAfterBlock != 1 {
		t.Errorf("unknown model = %+v", unk)
	}
	if got.ShadowGate.WouldHaveHeld != 0 {
		t.Errorf("shadow gate = %+v", got.ShadowGate)
	}

	if all := buildEvidenceSummary(bundles, now, 0, "c/d", ""); all.N != 1 || !all.AllTime {
		t.Errorf("all-time repo filter: %+v", all)
	}
	if k := buildEvidenceSummary(bundles, now, 0, "", "human"); k.N != 1 || k.Outcomes["agent"].N != 0 {
		t.Errorf("author_kind filter: %+v", k)
	}
}

func TestHandleEvidenceSummaryValidation(t *testing.T) {
	s := &Server{}
	for _, q := range []string{"hours=-1", "hours=x", "repo=nope", "author_kind=robot",
		"to=2026-10-15", "from=yesterday", "from=2026-10-15&to=2026-10-01", "from=2025-01-01&to=2026-10-15", "from=2026-10-01&to=bogus"} {
		w := httptest.NewRecorder()
		s.handleEvidenceSummary(w, httptest.NewRequest(http.MethodGet, "/api/evidence/summary?"+q, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, w.Code)
		}
	}
}

func TestHandleEvidenceSummaryEmpty(t *testing.T) {
	reviewEvidenceTestEnv(t)
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleEvidenceSummary(w, httptest.NewRequest(http.MethodGet, "/api/evidence/summary", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var resp EvidenceSummary
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.N != 0 || resp.Hours != 24 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestLoadEvidenceWriteCounters(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "wc.json")
	body := `{"totals":{"observe":{"attempted":5,"blocked":2,"blocked_by_reason":{"x":2}}},"weeks":{}}`
	if err := os.WriteFile(good, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadEvidenceWriteCounters(good)
	if got["observe"] != (EvidenceWriteCount{Attempted: 5, Blocked: 2}) {
		t.Errorf("got %+v", got)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if loadEvidenceWriteCounters(bad) != nil || loadEvidenceWriteCounters(filepath.Join(dir, "missing.json")) != nil {
		t.Error("unreadable or absent file must yield nil")
	}
}

func TestBuildEvidenceSummaryShadowGate(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Hour)
	yes, no := true, false
	withShadow := func(b *evidence.Bundle, held, blocked *bool) *evidence.Bundle {
		b.Shadow = &evidence.Shadow{}
		if held != nil {
			b.Shadow.ReviewGate = &evidence.ShadowGate{WouldHaveHeld: held, Reason: "r", EvaluatedAt: recent}
		}
		if blocked != nil {
			b.Shadow.Intent = &evidence.ShadowGate{WouldHaveBlocked: blocked, Tier: "observe", EvaluatedAt: recent}
		}
		return b
	}
	reverted := withShadow(evSummaryBundle("a/b", 1, evidence.AuthorAgent, recent, true, nil, 0, true), &yes, &yes)
	reverted.RevertedBy = []string{"a/b#9@def"}
	bundles := []*evidence.Bundle{
		reverted,
		withShadow(evSummaryBundle("a/b", 2, evidence.AuthorAgent, recent, false, nil, 0, true), &yes, nil),
		withShadow(evSummaryBundle("a/b", 3, evidence.AuthorHuman, recent, true, nil, 0, true), &no, &no),
		// no shadow observation at all
		evSummaryBundle("a/b", 4, evidence.AuthorHuman, recent, true, nil, 0, true),
		// outside the 24h window
		withShadow(evSummaryBundle("a/b", 5, evidence.AuthorAgent, now.Add(-48*time.Hour), true, nil, 0, true), &yes, &yes),
		// another repo
		withShadow(evSummaryBundle("c/d", 6, evidence.AuthorAgent, recent, true, nil, 0, true), &yes, nil),
	}

	sg := buildEvidenceSummary(bundles, now, 24, "a/b", "").ShadowGate
	if sg.N != 3 || sg.WouldHaveHeld != 2 || sg.WouldHaveBlocked != 1 {
		t.Fatalf("shadow gate = %+v", sg)
	}
	if want := (EvidenceShadowOutcome{N: 2, Merged: 1, Open: 1, Reverted: 1}); sg.ReviewGate.Held != want || sg.ReviewGate.N != 3 {
		t.Errorf("review gate = %+v", sg.ReviewGate)
	}
	if want := (EvidenceShadowOutcome{N: 1, Merged: 1}); sg.ReviewGate.Passed != want {
		t.Errorf("review gate passed = %+v", sg.ReviewGate.Passed)
	}
	if want := (EvidenceShadowOutcome{N: 1, Merged: 1, Reverted: 1}); sg.Intent.Held != want || sg.Intent.N != 2 || sg.Intent.Passed.N != 1 {
		t.Errorf("intent = %+v", sg.Intent)
	}

	if k := buildEvidenceSummary(bundles, now, 24, "a/b", "human").ShadowGate; k.N != 1 || k.WouldHaveHeld != 0 || k.ReviewGate.Passed.N != 1 {
		t.Errorf("author_kind filter: %+v", k)
	}
	if z := buildEvidenceSummary(bundles[3:4], now, 24, "", "").ShadowGate; z != (EvidenceShadowGate{}) {
		t.Errorf("no shadow bundles must yield zero shadow gate, got %+v", z)
	}
}

// TestParseEvidenceWindow pins the 24h/7d/30d/custom window contract
// (#12062): hours up to the throughput cap, and from/to as RFC3339 or
// YYYY-MM-DD with an inclusive date-only `to`.
func TestParseEvidenceWindow(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	q := func(s string) map[string][]string {
		u, err := url.ParseQuery(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	win, err := parseEvidenceWindow(q("hours=720"), now)
	if err != nil || win.Hours != 720 || win.Custom || !win.From.Equal(now.Add(-720*time.Hour)) {
		t.Fatalf("hours=720: %+v %v", win, err)
	}
	win, err = parseEvidenceWindow(q("from=2026-10-01&to=2026-10-15"), now)
	if err != nil || !win.Custom || win.Hours != 15*24 ||
		!win.From.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) ||
		!win.To.Equal(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("date range: %+v %v", win, err)
	}
	win, err = parseEvidenceWindow(q("from=2026-10-15T06:00:00Z"), now)
	if err != nil || !win.Custom || !win.To.Equal(now) || win.Hours != 30 {
		t.Fatalf("rfc3339 open-ended: %+v %v", win, err)
	}
	if _, err := parseEvidenceWindow(q("from=2025-10-01&to=2026-10-15"), now); err == nil {
		t.Error("range over 366 days must be rejected")
	}
	if _, err := parseEvidenceWindow(q("from=2026-10-15&to=2026-10-01"), now); err == nil {
		t.Error("to before from must be rejected")
	}
}

func TestBuildEvidenceSummaryCustomRange(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	bundles := []*evidence.Bundle{
		evSummaryBundle("a/b", 1, evidence.AuthorAgent, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), true, nil, 0, true),
		evSummaryBundle("a/b", 2, evidence.AuthorHuman, time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC), false, nil, 0, true),
		evSummaryBundle("a/b", 3, evidence.AuthorHuman, time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC), false, nil, 0, true),
	}
	win := evidenceWindow{Custom: true, Hours: 7 * 24,
		From: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	got := buildEvidenceSummaryWindow(bundles, win, "", "")
	if got.N != 1 || got.Outcomes["human"].N != 1 || got.AllTime {
		t.Fatalf("custom range: %+v", got)
	}
	if got.From != "2026-10-05T00:00:00Z" || got.To != "2026-10-12T00:00:00Z" {
		t.Errorf("from/to echo = %q %q", got.From, got.To)
	}
	// All-time still has no bounds.
	if all := buildEvidenceSummary(bundles, now, 0, "", ""); all.N != 3 || !all.AllTime || all.From != "" {
		t.Errorf("all time: %+v", all)
	}
}
