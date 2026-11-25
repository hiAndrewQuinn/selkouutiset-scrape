package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// --- helpers ---------------------------------------------------------------

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(isoDate, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// makePage builds a synthetic front page carrying the given headings, padded
// past the health floor so the gate accepts it. Real captures are ~130 KB;
// keeping the generated fixtures synthetic keeps them out of the repo. For the
// markup boundary itself see testdata/real_capture.html.
func makePage(headings ...string) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><body>")
	for _, h := range headings {
		b.WriteString(`<h1 class="aw__sc-xiym5b-0 ` + editionMarker + `">` + h + `</h1>`)
	}
	b.WriteString("<p>" + strings.Repeat("padding ", minCaptureBytes/8) + "</p></body></html>")
	return []byte(b.String())
}

// makeEdition builds a page carrying both anchors, as a real capture does: the
// schema.org datePublished the program prefers, and a matching Finnish
// headline. Extra headings become article headings after the edition title.
func makeEdition(edition time.Time, extraHeadings ...string) []byte {
	page := makePage(append([]string{"Selkouutiset | " + edition.Format(headingDate)}, extraHeadings...)...)
	return withPublished(page, edition)
}

// withPublished splices a datePublished timestamp into a page, in the Helsinki
// offset Yle uses. Placed before the body so it is found regardless of ordering.
func withPublished(page []byte, day time.Time) []byte {
	meta := []byte(`<script type="application/ld+json">{"datePublished":"` +
		day.Format("2006-01-02") + `T16:47:15+0300"}</script>`)
	return bytes.Replace(page, []byte("<body>"), append([]byte("<body>"), meta...), 1)
}

func writeFile(t *testing.T, path string, content []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixture writes a capture to its own temporary directory and returns its path.
func fixture(t *testing.T, content []byte) string {
	t.Helper()
	return writeFile(t, filepath.Join(t.TempDir(), "capture.html"), content)
}

// seed populates an archive root with a capture for each day, whose page
// advertises that same day's edition.
func seed(t *testing.T, root string, days ...string) {
	t.Helper()
	for _, d := range days {
		p := day(t, d)
		writeFile(t, filepath.Join(root, pathFor(p)), makeEdition(p))
	}
}

func mustCapture(t *testing.T, root, capturePath, today string) string {
	t.Helper()
	var out bytes.Buffer
	if err := runCapture(&out, root, capturePath, day(t, today)); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func mustList(t *testing.T, root string) []string {
	t.Helper()
	cs, err := listCaptures(root)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// archivedDays is the archive's day sequence; snapshot also pins file sizes, so
// it detects a rewrite that leaves the day sequence unchanged.
func archivedDays(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, rel := range mustList(t, root) {
		d, err := dateForPath(rel)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, d.Format(isoDate))
	}
	return out
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	for _, rel := range mustList(t, root) {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %d\n", rel, info.Size())
	}
	return b.String()
}

// --- parsing ---------------------------------------------------------------

func TestEditionDateFrom(t *testing.T) {
	// Every input below is a real shape from the corpus.
	tests := []struct {
		name    string
		heading string
		want    string // "" means it must not parse
	}{
		{"ordinary", `Selkouutiset | torstai 27.8.2026`, "2026-08-27"},
		{"no space after pipe", `Selkouutiset |tiistai 21.4.2026`, "2026-04-21"},
		{"double space", `Viikon uutinen selkosuomeksi | lauantai  23.5.2026`, "2026-05-23"},
		{"capitalised weekday", `Uutisviikko selkosuomeksi | Sunnuntai 15.2.2026`, "2026-02-15"},
		{"weekly round-up title", `Uutisviikko selkosuomeksi | sunnuntai 30.8.2026`, "2026-08-30"},
		{"zero padded is not octal", `Selkouutiset | perjantai 07.08.2026`, "2026-08-07"},
		{"leap day", `Selkouutiset | torstai 29.2.2024`, "2024-02-29"},
		{"five digit year typo", `Selkouutiset | torstai 28.5.20206`, ""},
		{"no date at all", `Selkouutiset`, ""},
		{"impossible calendar date", `Selkouutiset | tiistai 31.2.2026`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := editionDateFrom(tc.heading)
			if tc.want == "" {
				if ok {
					t.Fatalf("expected no parse, got %s", got.Format(isoDate))
				}
				return
			}
			if !ok {
				t.Fatalf("expected %s, got no parse", tc.want)
			}
			if got.Format(isoDate) != tc.want {
				t.Fatalf("got %s, want %s", got.Format(isoDate), tc.want)
			}
		})
	}
}

// The markup boundary, tested against a genuine capture rather than a fixture
// this file generated. If Yle renames the heading class or restructures the
// headline, this goes red here instead of silently failing three times a day
// in production.
func TestParsesRealCapture(t *testing.T) {
	page := readFile(t, filepath.Join("testdata", "real_capture.html"))

	if problem := healthProblem(page); problem != "" {
		t.Fatalf("real capture judged unhealthy: %s", problem)
	}
	heading := editionHeading(page)
	if !strings.Contains(heading, "Selkouutiset") {
		t.Fatalf("heading does not look like an edition title: %q", heading)
	}
	// A real page carries many headings; only the first is the edition title.
	if strings.Count(heading, editionMarker) != 1 {
		t.Fatalf("heading spans more than one element: %q", heading)
	}
	got, ok := editionDateFrom(heading)
	if !ok || got.Format(isoDate) != "2026-09-03" {
		t.Fatalf("got %v (ok=%v), want 2026-09-03", got, ok)
	}
}

// The page arrives as a single enormous line carrying many headings. Taking any
// but the first is a bug this project has already shipped once.
func TestEditionHeadingTakesFirstOnly(t *testing.T) {
	page := makePage(
		"Selkouutiset | torstai 3.9.2026",
		"Puoluekannatus",
		"Euroopan turvallisuus",
		"Perjantain sää",
	)
	h := editionHeading(page)
	if strings.Contains(h, "Puoluekannatus") {
		t.Fatalf("heading leaked a later article heading: %q", h)
	}
	got, ok := editionDateFrom(h)
	if !ok || got.Format(isoDate) != "2026-09-03" {
		t.Fatalf("got %v (ok=%v), want 2026-09-03", got, ok)
	}
}

func TestPathRoundTrip(t *testing.T) {
	for _, s := range []string{"2023-10-26", "2024-02-29", "2026-08-27", "2026-12-31"} {
		d := day(t, s)
		back, err := dateForPath(pathFor(d))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if !back.Equal(d) {
			t.Fatalf("%s round-tripped to %s", s, back.Format(isoDate))
		}
	}
	if got := pathFor(day(t, "2026-08-27")); got != "2026/08/27/selkouutiset_2026_08_27.html" {
		t.Fatalf("pathFor: %s", got)
	}
}

func TestMissingDays(t *testing.T) {
	have := map[string]bool{
		pathFor(day(t, "2026-09-01")): true,
		pathFor(day(t, "2026-09-04")): true,
	}
	got := missingDays(have, day(t, "2026-09-01"), day(t, "2026-09-04"))
	var iso []string
	for _, d := range got {
		iso = append(iso, d.Format(isoDate))
	}
	want := []string{"2026-09-02", "2026-09-03"}
	if !slices.Equal(iso, want) {
		t.Fatalf("got %v, want %v", iso, want)
	}
}

// --- filing rules ----------------------------------------------------------

// A run GitHub Actions delayed past midnight must file the edition under the
// day it belongs to as well as the day the runner woke up on. This is the
// original bug the whole project exists to fix.
func TestDelayedRunHealsSkippedDay(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-08-25", "2026-08-26")

	page := makeEdition(day(t, "2026-08-27"))
	out := mustCapture(t, root, fixture(t, page), "2026-08-28")

	want := []string{"2026-08-25", "2026-08-26", "2026-08-27", "2026-08-28"}
	if got := archivedDays(t, root); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if healed := readFile(t, filepath.Join(root, pathFor(day(t, "2026-08-27")))); !bytes.Equal(healed, page) {
		t.Fatal("healed day is not byte-identical to the capture")
	}
	if !strings.Contains(out, "filed    2026-08-27") {
		t.Fatalf("expected a filed line, got:\n%s", out)
	}
}

func TestMultiDayGapCarriedForward(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-08-28", "2026-08-29", "2026-08-30")
	source := readFile(t, filepath.Join(root, pathFor(day(t, "2026-08-30"))))

	mustCapture(t, root, fixture(t, makeEdition(day(t, "2026-09-03"))), "2026-09-03")

	for _, d := range []string{"2026-08-31", "2026-09-01", "2026-09-02"} {
		got := readFile(t, filepath.Join(root, pathFor(day(t, d))))
		if !bytes.Equal(got, source) {
			t.Fatalf("%s was not carried forward from 2026-08-30", d)
		}
	}
}

// A hole can open up behind an already-archived day. Considering only the tail
// would never repair it — a real defect caught during the shell version's
// testing.
func TestGapBehindNewestDayIsRepaired(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-08-25", "2026-08-26", "2026-08-27", "2026-08-28")
	if err := os.RemoveAll(filepath.Join(root, "2026/08/26")); err != nil {
		t.Fatal(err)
	}

	mustCapture(t, root, fixture(t, makeEdition(day(t, "2026-08-28"))), "2026-08-28")

	if _, err := os.Stat(filepath.Join(root, pathFor(day(t, "2026-08-26")))); err != nil {
		t.Fatal("gap behind the newest day was not repaired")
	}
}

func TestIdempotence(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02")
	f := fixture(t, makeEdition(day(t, "2026-09-03")))

	mustCapture(t, root, f, "2026-09-03")
	before := snapshot(t, root)
	mustCapture(t, root, f, "2026-09-03")

	if got := snapshot(t, root); got != before {
		t.Fatalf("second run changed the archive:\n%s\nvs\n%s", before, got)
	}
}

// Yle's own headlines are sometimes wrong. An implausible date needs no clamp:
// it simply matches no missing day.
func TestImplausibleEditionDateMatchesNothing(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02")

	mustCapture(t, root, fixture(t, makeEdition(day(t, "2020-05-28"))), "2026-09-03")

	if _, err := os.Stat(filepath.Join(root, "2020")); err == nil {
		t.Fatal("an out-of-archive date created a 2020/ directory")
	}
}

func TestWrongDateDoesNotOverwriteAnExistingDay(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02")
	target := filepath.Join(root, pathFor(day(t, "2026-09-01")))
	before := readFile(t, target)

	// Advertises a day that exists and is already correctly filled.
	mustCapture(t, root, fixture(t, makeEdition(day(t, "2026-09-01"))), "2026-09-03")

	if !bytes.Equal(before, readFile(t, target)) {
		t.Fatal("an existing day was overwritten")
	}
}

func TestGateRejectsBadCapturesWithoutWriting(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		// The 919-byte CDN block page, which was twice committed as news data.
		{"undersized", []byte(`<HTML><HEAD><TITLE>ERROR: The request could not be satisfied</TITLE></HEAD>` +
			`<BODY><H1>403 ERROR</H1></BODY></HTML>`), "below the"},
		{"no edition date at all", bytes.ReplaceAll(
			makePage("Selkouutiset | torstai 3.9.2026"),
			[]byte(editionMarker), []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxx")), "no edition date"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			seed(t, root, "2026-09-01", "2026-09-02")
			before := snapshot(t, root)

			var out bytes.Buffer
			err := runCapture(&out, root, fixture(t, tc.content), day(t, "2026-09-03"))
			if err == nil {
				t.Fatal("expected rejection, got nil error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if got := snapshot(t, root); got != before {
				t.Fatal("archive was modified despite rejection")
			}
		})
	}
}

// --- audit -----------------------------------------------------------------

func TestAuditReportsMissingDay(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02", "2026-09-03")

	var clean bytes.Buffer
	ok, err := runAudit(&clean, root)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !strings.Contains(clean.String(), "AUDIT PASSED") {
		t.Fatalf("expected a pass, got:\n%s", clean.String())
	}

	if err := os.RemoveAll(filepath.Join(root, "2026/09/02")); err != nil {
		t.Fatal(err)
	}
	var broken bytes.Buffer
	ok, err = runAudit(&broken, root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected a failure for a missing day")
	}
	if !strings.Contains(broken.String(), "MISSING DAY   2026-09-02") {
		t.Fatalf("missing day not reported:\n%s", broken.String())
	}
}

func TestAuditReportsUnhealthyCapture(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02")
	// Overwrite one day with a CDN error page, as happened twice for real.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-02"))), []byte("<H1>403 ERROR</H1>"))

	var out bytes.Buffer
	ok, err := runAudit(&out, root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected a failure for an unhealthy capture")
	}
	if !strings.Contains(out.String(), "UNHEALTHY") || !strings.Contains(out.String(), "below the") {
		t.Fatalf("unhealthy capture not reported:\n%s", out.String())
	}
}

// An off-by-one day is benign and must not be reported as a delta; anything
// else is reported but still passes, since the invariant is about presence.
func TestAuditClassifiesDeltas(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01")
	// 2026-09-02 holds the previous day's edition: the benign off-by-one.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-02"))), makeEdition(day(t, "2026-09-01")))
	// 2026-09-03 holds a much older edition: reported.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-03"))), makeEdition(day(t, "2026-08-12")))

	var out bytes.Buffer
	ok, err := runAudit(&out, root)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("date deltas must not fail the audit:\n%s", out.String())
	}
	s := out.String()
	if !strings.Contains(s, "1 exact, 1 off-by-one (benign), 1 other delta") {
		t.Fatalf("unexpected classification:\n%s", s)
	}
	if !strings.Contains(s, "(-22 days)") {
		t.Fatalf("expected a -22 day delta:\n%s", s)
	}
}

// --- anchors ---------------------------------------------------------------

// The primary anchor, checked against genuine markup rather than a fixture this
// file generated.
func TestRealCaptureCarriesBothAnchors(t *testing.T) {
	page := readFile(t, filepath.Join("testdata", "real_capture.html"))

	published, ok := editionFromPublished(page)
	if !ok {
		t.Fatal("real capture has no datePublished")
	}
	heading, ok := editionFromHeading(page)
	if !ok {
		t.Fatal("real capture has no parseable heading")
	}
	if !published.Equal(heading) {
		t.Fatalf("anchors disagree: datePublished=%s heading=%s",
			published.Format(isoDate), heading.Format(isoDate))
	}
	got, from := editionDate(page)
	if from != anchorPublished || got.Format(isoDate) != "2026-09-03" {
		t.Fatalf("got %s via %q, want 2026-09-03 via datePublished", got.Format(isoDate), from)
	}
}

// Yle's headlines carry real typos; the metadata does not. Where they differ,
// the metadata wins.
func TestPublishedBeatsHeadingWhenTheyDisagree(t *testing.T) {
	// Mirrors 2026/06/24, whose headline reads "keskiviikko 23.6.2026" although
	// 23 June 2026 was a Tuesday and the content is the 24th's edition.
	page := withPublished(makePage("Selkouutiset | keskiviikko 23.6.2026"), day(t, "2026-06-24"))

	got, from := editionDate(page)
	if from != anchorPublished {
		t.Fatalf("resolved via %q, want datePublished", from)
	}
	if got.Format(isoDate) != "2026-06-24" {
		t.Fatalf("got %s, want 2026-06-24", got.Format(isoDate))
	}
}

// If Yle ever stops emitting the metadata, captures must keep landing on the
// right day via the headline.
func TestFallsBackToHeadingWithoutMetadata(t *testing.T) {
	page := makePage("Selkouutiset | torstai 27.8.2026")

	got, from := editionDate(page)
	if from != anchorHeading {
		t.Fatalf("resolved via %q, want heading", from)
	}
	if got.Format(isoDate) != "2026-08-27" {
		t.Fatalf("got %s, want 2026-08-27", got.Format(isoDate))
	}
}

// The reason the gate no longer demands the heading class: a cosmetic rename by
// Yle must not stop collection while the date is still readable.
func TestGateAcceptsRenamedHeadingClass(t *testing.T) {
	page := bytes.ReplaceAll(makeEdition(day(t, "2026-09-03")),
		[]byte(editionMarker), []byte("yle__article__headline--v2"))

	if _, ok := editionFromHeading(page); ok {
		t.Fatal("fixture still has a recognisable heading; the test proves nothing")
	}
	if problem := healthProblem(page); problem != "" {
		t.Fatalf("gate rejected a page that still carries datePublished: %s", problem)
	}

	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02")
	out := mustCapture(t, root, fixture(t, page), "2026-09-03")
	if !strings.Contains(out, "edition 2026-09-03") {
		t.Fatalf("expected the metadata date to be used, got:\n%s", out)
	}
}

func TestAuditReportsAnchorProvenance(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01") // carries both anchors
	// Metadata only, no parseable heading.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-02"))),
		withPublished(makePage("Selkouutiset"), day(t, "2026-09-02")))
	// Heading only: the fallback path.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-03"))),
		makePage("Selkouutiset | torstai 3.9.2026"))
	// Both, disagreeing: metadata wins and the disagreement is counted.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-04"))),
		withPublished(makePage("Selkouutiset | perjantai 1.1.2020"), day(t, "2026-09-04")))

	var out bytes.Buffer
	if _, err := runAudit(&out, root); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "anchors:    3 datePublished, 1 heading fallback") {
		t.Fatalf("unexpected anchor counts:\n%s", s)
	}
	if !strings.Contains(s, "disagree:   1 capture(s)") {
		t.Fatalf("unexpected disagreement count:\n%s", s)
	}
}

// --- late runs and stale carry-forwards --------------------------------------

// editionIn reads the edition a day's slot holds.
func editionIn(t *testing.T, root, d string) string {
	t.Helper()
	held, exists, err := slotEdition(root, day(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("%s has no file", d)
	}
	return held.Format(isoDate)
}

// The production failure of November 2026, replayed run by run. After DST
// ended, GitHub started the afternoon cron after midnight Helsinki time, so
// every run woke on the day after the edition it saw. The old rules filed
// each edition only under the wake-up day, leaving its own day holding the
// edition before; and on 21 November a run before midnight overwrote the 21st
// with that day's edition, losing the 20th's entirely.
func TestLateRunsReplayOfLostNovemberEdition(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-11-18")

	runs := []struct{ today, edition string }{
		{"2026-11-19", "2026-11-18"}, // 00:31 and 05:54: 19th not yet published
		{"2026-11-20", "2026-11-19"}, // 00:38 on the 20th, seeing the 19th
		{"2026-11-21", "2026-11-20"}, // 00:12 on the 21st, seeing the 20th
		{"2026-11-21", "2026-11-21"}, // 22:51 on the 21st, seeing the 21st
	}
	for _, r := range runs {
		mustCapture(t, root, fixture(t, makeEdition(day(t, r.edition))), r.today)
	}

	for _, d := range []string{"2026-11-18", "2026-11-19", "2026-11-20", "2026-11-21"} {
		if got := editionIn(t, root, d); got != d {
			t.Errorf("%s holds edition %s, want its own", d, got)
		}
	}
}

// A run that wakes after midnight must upgrade the edition's own day, which an
// earlier run had already filled with the previous edition.
func TestLateRunUpgradesStaleOwnDay(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-11-17")
	// The 18th was filled before that day's edition was out.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-11-18"))), makeEdition(day(t, "2026-11-17")))

	page := makeEdition(day(t, "2026-11-18"))
	out := mustCapture(t, root, fixture(t, page), "2026-11-19")

	if got := readFile(t, filepath.Join(root, pathFor(day(t, "2026-11-18")))); !bytes.Equal(got, page) {
		t.Fatal("the edition's own day was not upgraded")
	}
	if !strings.Contains(out, "upgraded 2026-11-18  <- edition 2026-11-18 (slot held 2026-11-17)") {
		t.Fatalf("expected an upgrade line, got:\n%s", out)
	}
	if got := editionIn(t, root, "2026-11-19"); got != "2026-11-18" {
		t.Fatalf("today holds %s, want the capture's 2026-11-18", got)
	}
}

// Every stale carry-forward after the edition's day is upgraded, and the walk
// stops at the first day that already holds something at least as new.
func TestUpgradeWalksStaleDaysAndStopsAtNewer(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-12-20")
	for _, d := range []string{"2026-12-21", "2026-12-22", "2026-12-23"} {
		writeFile(t, filepath.Join(root, pathFor(day(t, d))), makeEdition(day(t, "2026-12-20")))
	}
	seed(t, root, "2026-12-24")
	before24 := readFile(t, filepath.Join(root, pathFor(day(t, "2026-12-24"))))

	// The 22nd's edition turns up late (the 22nd and 23rd were filled while
	// the front page still showed the 20th's). Today is the 25th.
	mustCapture(t, root, fixture(t, makeEdition(day(t, "2026-12-22"))), "2026-12-25")

	want := map[string]string{
		"2026-12-21": "2026-12-20", // before the edition: untouched
		"2026-12-22": "2026-12-22",
		"2026-12-23": "2026-12-22", // stale carry-forward: upgraded
	}
	for d, w := range want {
		if got := editionIn(t, root, d); got != w {
			t.Errorf("%s holds %s, want %s", d, got, w)
		}
	}
	if !bytes.Equal(before24, readFile(t, filepath.Join(root, pathFor(day(t, "2026-12-24"))))) {
		t.Fatal("a day holding a newer edition was overwritten")
	}
}

// Today's slot must not roll back to an older edition, as a stale CDN copy
// served on a later run would otherwise make it.
func TestCaptureNeverRollsTodayBack(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02", "2026-09-03")
	target := filepath.Join(root, pathFor(day(t, "2026-09-03")))
	before := readFile(t, target)

	out := mustCapture(t, root, fixture(t, makeEdition(day(t, "2026-09-02"))), "2026-09-03")

	if !bytes.Equal(before, readFile(t, target)) {
		t.Fatal("today's newer edition was replaced by an older one")
	}
	if !strings.Contains(out, "kept     2026-09-03") {
		t.Fatalf("expected a kept line, got:\n%s", out)
	}
}

// --- backfill ----------------------------------------------------------------

func mustBackfill(t *testing.T, root string, today string, pages ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	rejected, err := runBackfill(&out, root, pages, day(t, today))
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), rejected
}

// Backfill extends the archive backwards, carrying the preceding edition into
// days that have none, so the no-gaps invariant holds over the new span too.
func TestBackfillExtendsArchiveBackwards(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2023-10-26", "2023-10-27")
	dir := t.TempDir()
	a := writeFile(t, filepath.Join(dir, "a.html"), makeEdition(day(t, "2023-10-21")))
	b := writeFile(t, filepath.Join(dir, "b.html"), makeEdition(day(t, "2023-10-24")))

	if _, rejected := mustBackfill(t, root, "2023-10-28", a, b); rejected != 0 {
		t.Fatalf("%d rejected", rejected)
	}

	want := []string{"2023-10-21", "2023-10-22", "2023-10-23", "2023-10-24", "2023-10-25", "2023-10-26", "2023-10-27"}
	if got := archivedDays(t, root); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	held := map[string]string{
		"2023-10-21": "2023-10-21", "2023-10-22": "2023-10-21", "2023-10-23": "2023-10-21",
		"2023-10-24": "2023-10-24", "2023-10-25": "2023-10-24", "2023-10-26": "2023-10-26",
	}
	for d, w := range held {
		if got := editionIn(t, root, d); got != w {
			t.Errorf("%s holds %s, want %s", d, got, w)
		}
	}
}

// The result must not depend on the order pages are given in: the newest-first
// sort is an efficiency measure, not a semantic one.
func TestBackfillIsOrderIndependent(t *testing.T) {
	dir := t.TempDir()
	var pages []string
	for _, d := range []string{"2023-10-15", "2023-10-18", "2023-10-19", "2023-10-23"} {
		pages = append(pages, writeFile(t, filepath.Join(dir, d+".html"), makeEdition(day(t, d))))
	}
	reversed := slices.Clone(pages)
	slices.Reverse(reversed)

	var snaps []string
	for _, order := range [][]string{pages, reversed} {
		root := t.TempDir()
		seed(t, root, "2023-10-26")
		mustBackfill(t, root, "2023-10-27", order...)
		snaps = append(snaps, snapshot(t, root))
	}
	if snaps[0] != snaps[1] {
		t.Fatalf("archive depends on argument order:\n%s\nvs\n%s", snaps[0], snaps[1])
	}
}

// Where two pages carry the same edition date (a radio and a TV edition), the
// first one given wins, so callers state a preference through argument order.
func TestBackfillFirstPageWinsForADate(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2023-10-26")
	dir := t.TempDir()
	radio := makeEdition(day(t, "2023-10-25"), "radio")
	tv := makeEdition(day(t, "2023-10-25"), "tv")
	r := writeFile(t, filepath.Join(dir, "radio.html"), radio)
	v := writeFile(t, filepath.Join(dir, "tv.html"), tv)

	mustBackfill(t, root, "2023-10-27", r, v)

	if got := readFile(t, filepath.Join(root, pathFor(day(t, "2023-10-25")))); !bytes.Equal(got, radio) {
		t.Fatal("the first page given for a date did not win")
	}
}

// A lost day inside the archive is upgraded in place, along with its stale
// carry-forward, and nothing newer is touched.
func TestBackfillRecoversLostDayInsideArchive(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-11-19")
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-11-20"))), makeEdition(day(t, "2026-11-19")))
	seed(t, root, "2026-11-21")
	before21 := readFile(t, filepath.Join(root, pathFor(day(t, "2026-11-21"))))

	p := writeFile(t, filepath.Join(t.TempDir(), "p.html"), makeEdition(day(t, "2026-11-20")))
	mustBackfill(t, root, "2026-11-24", p)

	if got := editionIn(t, root, "2026-11-20"); got != "2026-11-20" {
		t.Fatalf("lost day holds %s after backfill", got)
	}
	if !bytes.Equal(before21, readFile(t, filepath.Join(root, pathFor(day(t, "2026-11-21"))))) {
		t.Fatal("a newer day was rewritten")
	}
}

// Bad pages are reported and skipped; good ones in the same batch still land.
func TestBackfillSkipsBadPagesAndFilesTheRest(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01")
	dir := t.TempDir()
	bad := writeFile(t, filepath.Join(dir, "bad.html"), []byte("<H1>404</H1>"))
	future := writeFile(t, filepath.Join(dir, "future.html"), makeEdition(day(t, "2026-09-09")))
	good := writeFile(t, filepath.Join(dir, "good.html"), makeEdition(day(t, "2026-08-31")))

	out, rejected := mustBackfill(t, root, "2026-09-02", bad, future, good)

	if rejected != 2 {
		t.Fatalf("rejected %d, want 2:\n%s", rejected, out)
	}
	if got := archivedDays(t, root); !slices.Equal(got, []string{"2026-08-31", "2026-09-01"}) {
		t.Fatalf("got %v", got)
	}
}

// --- feed --------------------------------------------------------------------

func TestFeedListsOnlyEditionsTheArchiveLacks(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-11-18", "2026-11-19")
	// The 20th holds the 19th's edition: the lost-day shape.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-11-20"))), makeEdition(day(t, "2026-11-19")))

	feed := writeFile(t, filepath.Join(t.TempDir(), "feed.xml"), []byte(`<?xml version="1.0"?>
<rss version="2.0"><channel><title>Selkouutiset</title>
<item><title>Selkouutiset | maanantai 23.11.2026</title><link>https://yle.fi/a/74-23?origin=rss</link><pubDate>Mon, 23 Nov 2026 16:47:56 +0200</pubDate></item>
<item><title>Selkouutiset | lauantai 21.11.2026</title><link>https://yle.fi/a/74-21?origin=rss</link><pubDate>Sat, 21 Nov 2026 14:18:58 +0200</pubDate></item>
<item><title>Selkouutiset | perjantai 20.11.2026</title><link>https://yle.fi/a/74-20?origin=rss</link><pubDate>Fri, 20 Nov 2026 16:35:35 +0200</pubDate></item>
<item><title>Selkouutiset | torstai 19.11.2026</title><link>https://yle.fi/a/74-19?origin=rss</link><pubDate>Thu, 19 Nov 2026 15:41:40 +0200</pubDate></item>
<item><title>Late-night edition</title><link>https://yle.fi/a/74-late</link><pubDate>Wed, 18 Nov 2026 22:30:00 +0000</pubDate></item>
</channel></rss>`))

	var out bytes.Buffer
	if err := runFeed(&out, root, feed, day(t, "2026-11-22")); err != nil {
		t.Fatal(err)
	}
	// 23rd: after today, skipped. 21st: missing. 20th: stale. 19th: correct.
	// The last item's 22:30 UTC is 00:30 on the 19th in Helsinki, which holds
	// the 19th's edition already.
	want := "https://yle.fi/a/74-21\nhttps://yle.fi/a/74-20\n"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestFeedRejectsGarbage(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-11-18")
	for name, body := range map[string]string{
		"not xml":  "<html><body>Bad gateway</body></html",
		"no items": `<rss><channel><title>x</title></channel></rss>`,
	} {
		t.Run(name, func(t *testing.T) {
			f := writeFile(t, filepath.Join(t.TempDir(), "feed.xml"), []byte(body))
			if err := runFeed(io.Discard, root, f, day(t, "2026-11-19")); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAuditCountsRepeats(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-12-23")
	for _, d := range []string{"2026-12-24", "2026-12-25"} {
		writeFile(t, filepath.Join(root, pathFor(day(t, d))), makeEdition(day(t, "2026-12-23")))
	}
	seed(t, root, "2026-12-26")

	var out bytes.Buffer
	if _, err := runAudit(&out, root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "repeats:    2 day(s)") {
		t.Fatalf("unexpected repeat count:\n%s", out.String())
	}
}
