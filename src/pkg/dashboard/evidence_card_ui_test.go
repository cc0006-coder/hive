package dashboard

import (
	"regexp"
	"strings"
	"testing"
)

// TestEvidenceCardInThroughputSection pins the Evidence sub-panel (#12064): it
// sits inside the Throughput section below the throughput card, reuses the
// prt-card chrome, follows the Throughput window/repo selector, and reads
// /api/evidence/summary for the calibration, accuracy × model, boundary
// enforcement (#12368) and shadow-gate (#12068) panels.
func TestEvidenceCardInThroughputSection(t *testing.T) {
	html := indexHTML(t)
	section := strings.Index(html, `<div id="pr-throughput-section"`)
	if section < 0 {
		t.Fatal("pr-throughput-section missing")
	}
	end := strings.Index(html[section:], `<div id="advisory-section"`)
	if end < 0 {
		t.Fatal("cannot find end of pr-throughput-section markup")
	}
	markup := html[section : section+end]
	prt := strings.Index(markup, `id="pr-throughput-card"`)
	ev := strings.Index(markup, `<div id="evidence-card" class="prt-card advisory-subsection-card evidence-card"`)
	if prt < 0 || ev < 0 || ev < prt {
		t.Fatalf("evidence-card must sit inside pr-throughput-section below pr-throughput-card (prt=%d ev=%d)", prt, ev)
	}

	for _, snippet := range []string{
		"/api/evidence/summary?hours=${encodeURIComponent(hours)}",
		"if (repo) url += `&repo=${encodeURIComponent(repo)}`;",
		"if (hours !== prtHours || repo !== prtRepo) return;",
		"async function fetchPRThroughput() {\n      fetchEvidenceSummary();",
		"dto.reviewer_calibration",
		"dto.reviewer_accuracy_by_model",
		"verdicts_model_unknown",
		`<details class="ev-panel" data-ev-panel="${key}"`,
		`n=${Number(n) || 0}`,
		"What this does not show:",
		"'Reviewer calibration'",
		"'Reviewer accuracy × model family'",
		"row.model === 'unknown' ? 'ev-unknown' : ''",
		`<svg class="ev-svg"`,
		".ev-svg .ev-unknown .ev-bar",
		"dto.shadow_gate || {}",
		"evPanel('shadow-gates', 'Shadow gates', shadow.n, evShadowGateSvg(shadow),",
		"no shadow-gate observations in this window",
		"[['held', `would have ${verb}`], ['passed', 'would have passed']]",
		".ev-svg .ev-bar.sr",
		// Panel 4 (#12368): boundary enforcement.
		"const counters = dto.write_counters && typeof dto.write_counters === 'object' ? dto.write_counters : null;",
		"const sentinel = dto.sentinel_hits_per_week || {};",
		"evPanel('boundary-enforcement', 'Boundary enforcement', attemptedN, evWriteCountersSvg(counters) + evSentinelSparkline(sentinel),",
		"const EV_WRITE_TIERS = ['ADVISORY', 'ISSUES_ONLY', 'ISSUES_AND_PRS', 'ISSUES_PRS_MERGE'];",
		"Object.entries(c.blocked_by_reason || {})",
		"row.reasons.slice(0, EV_TOP_REASONS)",
		"no write counters yet (the proxy has not recorded an agent write, or the counter file is absent)",
		"no sentinel hits recorded in this window",
		"${bar('wa', row.attempted, 0, row.attempted)}${bar('wb', row.blocked, 1,",
		`aria-label="Sentinel hits per ISO week"`,
		"Write counters are proxy-side observations",
		"Would-block counts from intent verification are not included in these bars",
		"direct writes that bypass the proxy are invisible here",
		".ev-svg .ev-bar.wb",
		".ev-legend i.wa",
	} {
		if !strings.Contains(html, snippet) {
			t.Errorf("Evidence card missing snippet %q", snippet)
		}
	}
	// Panel order follows the RFC numbering: 4 (boundary) before 8 (shadow).
	p4 := strings.Index(html, "evPanel('boundary-enforcement'")
	p8 := strings.Index(html, "evPanel('shadow-gates'")
	if p4 < 0 || p8 < 0 || p4 > p8 {
		t.Errorf("boundary-enforcement panel must render before shadow-gates (p4=%d p8=%d)", p4, p8)
	}
}

// TestEvidenceCardUIHasNoUndefinedCallees is the ReferenceError guard for the
// Evidence card helpers.
func TestEvidenceCardUIHasNoUndefinedCallees(t *testing.T) {
	html := indexHTML(t)
	for _, fn := range []string{
		"fetchEvidenceSummary",
		"renderEvidenceSummary",
		"evPanel",
		"evRate",
		"evCalibrationSvg",
		"evAccuracySvg",
		"evWriteCountersSvg",
		"evSentinelSparkline",
		"evShadowGateSvg",
		"escapeHtml",
	} {
		decl := regexp.MustCompile(`(?m)^\s*(async\s+)?function\s+` + fn + `\s*\(`)
		if !jsDeclaredNear(html, fn, decl) {
			t.Errorf("Evidence card calls %q but it is never declared", fn)
		}
	}
}
