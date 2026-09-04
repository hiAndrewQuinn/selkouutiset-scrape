package main

import (
	"bytes"
	"fmt"
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
		writeFile(t, filepath.Join(root, pathFor(p)),
			makePage("Selkouutiset | "+p.Format(headingDate)))
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

	page := makePage("Selkouutiset | torstai 27.8.2026")
	out := mustCapture(t, root, fixture(t, page), "2026-08-28")

	want := []string{"2026-08-25", "2026-08-26", "2026-08-27", "2026-08-28"}
	if got := archivedDays(t, root); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if healed := readFile(t, filepath.Join(root, pathFor(day(t, "2026-08-27")))); !bytes.Equal(healed, page) {
		t.Fatal("healed day is not byte-identical to the capture")
	}
	if !strings.Contains(out, "healed   2026-08-27") {
		t.Fatalf("expected a healed line, got:\n%s", out)
	}
}

func TestMultiDayGapCarriedForward(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-08-28", "2026-08-29", "2026-08-30")
	source := readFile(t, filepath.Join(root, pathFor(day(t, "2026-08-30"))))

	mustCapture(t, root, fixture(t, makePage("Selkouutiset | torstai 3.9.2026")), "2026-09-03")

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

	mustCapture(t, root, fixture(t, makePage("Selkouutiset | perjantai 28.8.2026")), "2026-08-28")

	if _, err := os.Stat(filepath.Join(root, pathFor(day(t, "2026-08-26")))); err != nil {
		t.Fatal("gap behind the newest day was not repaired")
	}
}

func TestIdempotence(t *testing.T) {
	root := t.TempDir()
	seed(t, root, "2026-09-01", "2026-09-02")
	f := fixture(t, makePage("Selkouutiset | torstai 3.9.2026"))

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

	mustCapture(t, root, fixture(t, makePage("Selkouutiset | torstai 28.5.2020")), "2026-09-03")

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
	mustCapture(t, root, fixture(t, makePage("Selkouutiset | tiistai 1.9.2026")), "2026-09-03")

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
		{"no edition heading", bytes.ReplaceAll(
			makePage("Selkouutiset | torstai 3.9.2026"),
			[]byte(editionMarker), []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxx")), "heading"},
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
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-02"))),
		makePage("Selkouutiset | tiistai 1.9.2026"))
	// 2026-09-03 holds a much older edition: reported.
	writeFile(t, filepath.Join(root, pathFor(day(t, "2026-09-03"))),
		makePage("Selkouutiset | keskiviikko 12.8.2026"))

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
