// Command archive maintains the selkouutiset daily capture archive.
//
//	archive capture <fetched.html> [YYYY-MM-DD]   file a freshly fetched page
//	archive audit                                 read-only invariant check
//
// The archive holds exactly one file per calendar day under
// YYYY/MM/DD/selkouutiset_YYYY_MM_DD.html. On days when Yle published nothing
// new, that day's file repeats the previous edition; downstream tooling flags
// those as duplicates by content hash. There are never gaps.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	// Embeds the timezone database so resolving Europe/Helsinki does not
	// depend on the host having zoneinfo installed.
	_ "time/tzdata"
)

const (
	// Below this size a capture is an error page, not a front page: Yle's CDN
	// serves a 919-byte "403 ERROR" body when it blocks us, and two of those
	// were committed as news data before this floor existed.
	minCaptureBytes = 20000

	// The class wrapping the edition headline. Doubles as proof that the page
	// is really the selkouutiset front page.
	editionMarker = `yle__article__heading--1`

	// Layouts: isoDate for reporting, pathDate for the archive's directories,
	// headingDate for the D.M.YYYY that Yle prints in the headline.
	isoDate     = "2006-01-02"
	pathDate    = "2006/01/02"
	headingDate = "2.1.2006"

	// One capture per day, matched as a glob so the archive's shape is stated
	// once rather than spread across a depth count and a name check.
	archiveGlob = "[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/selkouutiset_*.html"
)

// headingRe matches an edition heading and its text. Only the first match in a
// page is the edition title; the rest are article headings.
//
// The page arrives as one enormous line, which is why the shell version this
// replaced could not use grep -m1: that stops at the first matching *line* and
// would still yield every heading on it.
var headingRe = regexp.MustCompile(editionMarker + `">[^<]*`)

// dateRe takes the last D.M.YYYY token in a heading. The weekday and title
// prefix are deliberately not parsed: the corpus contains
// "Selkouutiset |tiistai 21.4.2026" (no space), "lauantai  23.5.2026" (double
// space), a capitalised "Sunnuntai", and three different title forms. The
// styled-components classes sitting beside editionMarker change between Yle
// builds; that class name itself has held for three years.
//
// The trailing \D*$ anchor is load-bearing. It rejects the five-digit year in
// "torstai 28.5.20206" (a real typo in 2026/05/28) rather than silently
// truncating it to 2020, which is what an unanchored pattern did.
var dateRe = regexp.MustCompile(`(\d{1,2}\.\d{1,2}\.\d{4})\D*$`)

// pathFor returns the archive path for a day.
func pathFor(day time.Time) string {
	return day.Format(pathDate) + "/selkouutiset_" + day.Format("2006_01_02") + ".html"
}

// dateForPath is the inverse of pathFor: the day a relative archive path names.
func dateForPath(rel string) (time.Time, error) {
	if len(rel) < len(pathDate) {
		return time.Time{}, fmt.Errorf("path too short to carry a date: %q", rel)
	}
	return time.Parse(pathDate, rel[:len(pathDate)])
}

// listCaptures returns every archived capture as a path relative to root,
// oldest first. filepath.Glob sorts its results, and the path components are
// zero-padded fixed width, so that order is chronological.
func listCaptures(root string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, archiveGlob))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		rel, err := filepath.Rel(root, m)
		if err != nil {
			return nil, err
		}
		out[i] = filepath.ToSlash(rel)
	}
	return out, nil
}

// missingDays lists the days between from and to inclusive that have no
// capture, in order. Both subcommands are built on it: capture fills what it
// returns, audit reports it.
func missingDays(have map[string]bool, from, to time.Time) []time.Time {
	var out []time.Time
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		if !have[pathFor(day)] {
			out = append(out, day)
		}
	}
	return out
}

func haveSet(captures []string) map[string]bool {
	have := make(map[string]bool, len(captures))
	for _, c := range captures {
		have[c] = true
	}
	return have
}

// editionHeading returns the text of a page's first edition heading, or "" if
// it has none.
func editionHeading(page []byte) string {
	return string(headingRe.Find(page))
}

// editionDateFrom parses the edition date a heading advertises. time.Parse
// supplies the calendar validation, so an impossible date such as 31.2.2026 is
// rejected rather than silently normalised to 3 March.
func editionDateFrom(heading string) (time.Time, bool) {
	m := dateRe.FindStringSubmatch(heading)
	if m == nil {
		return time.Time{}, false
	}
	day, err := time.Parse(headingDate, m[1])
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// healthProblem reports why a page is not a usable capture, or "" if it is
// fine. One predicate, used both on an incoming capture and on archived ones,
// so the writer and the checker cannot drift apart on what "healthy" means.
func healthProblem(page []byte) string {
	if len(page) < minCaptureBytes {
		return fmt.Sprintf("%d bytes, below the %d-byte floor", len(page), minCaptureBytes)
	}
	if editionHeading(page) == "" {
		return fmt.Sprintf("no %q heading", editionMarker)
	}
	return ""
}

// place copies src over dest, creating parent directories. Copying a file onto
// itself is a no-op rather than an error, so re-filing an already archived
// capture is harmless.
//
// A copy, never a hard link: rule 1 rewrites today's slot in place on every
// run, and a link would make that silently mutate whichever earlier day it was
// carried forward from.
func place(dest, src string) error {
	if sameFile(dest, src) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func gitToplevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func main() {
	// -root exists mostly so tests can point at a temporary directory.
	root := flag.String("root", "", "archive root (default: the git toplevel)")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: archive [-root DIR] capture <fetched.html> [YYYY-MM-DD]\n"+
			"       archive [-root DIR] audit\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *root == "" {
		top, err := gitToplevel()
		if err != nil {
			fatal(2, "%v", err)
		}
		*root = top
	}

	switch args[0] {
	case "capture":
		if len(args) < 2 {
			fatal(2, "usage: archive capture <fetched.html> [YYYY-MM-DD]")
		}
		day, err := captureDay(args)
		if err != nil {
			fatal(2, "%v", err)
		}
		if err := runCapture(os.Stdout, *root, args[1], day); err != nil {
			fatal(1, "%v", err)
		}
	case "audit":
		ok, err := runAudit(os.Stdout, *root)
		if err != nil {
			fatal(1, "%v", err)
		}
		if !ok {
			os.Exit(1)
		}
	default:
		fatal(2, "unknown command: %s", args[0])
	}
}

// captureDay resolves which day a capture is being filed under. "Today" is
// Yle's day, not the runner's: GitHub runners are UTC and the edition lands at
// 16:45 Europe/Helsinki. The result is a bare UTC civil date, so no later
// arithmetic can be skewed by a DST changeover.
func captureDay(args []string) (time.Time, error) {
	if len(args) > 2 {
		return time.Parse(isoDate, args[2])
	}
	zone, err := time.LoadLocation("Europe/Helsinki")
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(isoDate, time.Now().In(zone).Format(isoDate))
}

func fatal(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(code)
}

// runCapture files a freshly fetched page into the archive.
//
// Two rules:
//
//  1. Today's slot is always written, overwriting, so a later run the same day
//     converges on that day's real edition.
//  2. Every other missing day is filled with the best content available: the
//     fresh capture if it advertises that day's edition, otherwise the
//     preceding day carried forward.
//
// Rule 2 is what makes a delayed run harmless. GitHub Actions routinely starts
// a cron hours late; when one slipped past midnight the old workflow filed that
// day's edition under the following day and the next run destroyed it. A run at
// 01:56 now fills both the day the edition belongs to and the day it woke up on.
//
// A bogus edition date needs no plausibility check: it can only ever select
// content for a day the walk already found missing, so a date outside the
// archive, or on a day already filled, matches nothing.
func runCapture(out io.Writer, root, capturePath string, today time.Time) error {
	page, err := os.ReadFile(capturePath)
	if err != nil {
		return fmt.Errorf("cannot read capture: %w", err)
	}

	// The workflow's `curl --fail` already rejects an HTTP error, but this is
	// also run by hand and against fixtures, so it checks what it was given.
	if problem := healthProblem(page); problem != "" {
		return fmt.Errorf("capture is %s\n       refusing to archive it", problem)
	}
	edition, hasEdition := editionDateFrom(editionHeading(page))

	// --- Rule 1: today's slot always exists ---
	if err := place(filepath.Join(root, pathFor(today)), capturePath); err != nil {
		return err
	}
	shown := "unparseable"
	if hasEdition {
		shown = edition.Format(isoDate)
	}
	fmt.Fprintf(out, "today    %s  <- capture (%d bytes, edition %s)\n",
		today.Format(isoDate), len(page), shown)

	// --- Rule 2: no gaps anywhere in the archive ---
	//
	// Considers the whole calendar rather than just the tail, so a hole that
	// opened up behind an already-archived day is repaired too.
	captures, err := listCaptures(root)
	if err != nil {
		return err
	}
	oldest, err := dateForPath(captures[0]) // rule 1 just wrote one, so never empty
	if err != nil {
		return err
	}

	for _, day := range missingDays(haveSet(captures), oldest, today) {
		slot := filepath.Join(root, pathFor(day))
		if hasEdition && day.Equal(edition) {
			if err := place(slot, capturePath); err != nil {
				return err
			}
			fmt.Fprintf(out, "healed   %s  <- capture (a run the scheduler delayed past midnight)\n",
				day.Format(isoDate))
			continue
		}
		// The preceding day is always present: either it was never missing, or
		// it was filled by an earlier iteration, since missingDays is ordered.
		prev := pathFor(day.AddDate(0, 0, -1))
		if err := place(slot, filepath.Join(root, prev)); err != nil {
			return err
		}
		fmt.Fprintf(out, "gap      %s  <- %s (carried forward)\n",
			day.Format(isoDate), filepath.Dir(prev))
	}
	return nil
}

// runAudit checks the archive and reports. It changes nothing.
//
// Checks three things:
//
//  1. Unbroken calendar — every day from the first capture to the newest has a
//     file. This is the archive's governing invariant.
//  2. Capture health — no file is undersized or missing its edition heading,
//     which is how CDN error pages used to slip in as news data.
//  3. Path vs. edition date, reported for information only. An off-by-one day
//     is normal and benign: the front page was still showing yesterday's
//     edition when that day was captured. Larger deltas are typos in Yle's own
//     headline, or a holiday stretch with no new edition.
//
// Returns false only for a missing day or an unhealthy capture. Date deltas
// never fail the audit: the invariant is about presence, not attribution.
func runAudit(out io.Writer, root string) (bool, error) {
	captures, err := listCaptures(root)
	if err != nil {
		return false, err
	}
	if len(captures) == 0 {
		return false, errors.New("archive is empty")
	}

	first, err := dateForPath(captures[0])
	if err != nil {
		return false, err
	}
	last, err := dateForPath(captures[len(captures)-1])
	if err != nil {
		return false, err
	}

	fmt.Fprintf(out, "archive: %d captures spanning %s .. %s\n\n",
		len(captures), first.Format(isoDate), last.Format(isoDate))

	// --- 1. Unbroken calendar ---
	absent := missingDays(haveSet(captures), first, last)
	for _, day := range absent {
		fmt.Fprintf(out, "MISSING DAY   %s\n", day.Format(isoDate))
	}

	// --- 2. Capture health, and 3. path vs. edition date ---
	//
	// One pass. Both checks read the same edition heading: whether it exists is
	// the health check, and the date inside it is the attribution check.
	var unhealthy, exact, offbyone, other, unparseable int
	for _, rel := range captures {
		page, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return false, err
		}
		if problem := healthProblem(page); problem != "" {
			fmt.Fprintf(out, "UNHEALTHY     %s (%s)\n", rel, problem)
			unhealthy++
			continue
		}
		edition, ok := editionDateFrom(editionHeading(page))
		if !ok {
			unparseable++
			continue
		}
		pathdate, err := dateForPath(rel)
		if err != nil {
			return false, err
		}
		if edition.Equal(pathdate) {
			exact++
			continue
		}
		// Printed delta is edition minus path, in days.
		delta := int(edition.Sub(pathdate).Hours() / 24)
		if delta == -1 {
			offbyone++
			continue
		}
		other++
		fmt.Fprintf(out, "DATE DELTA    %s  path=%s edition=%s (%+d days)\n",
			rel, pathdate.Format(isoDate), edition.Format(isoDate), delta)
	}

	fmt.Fprintf(out, "\ncalendar:   %d missing day(s)\n", len(absent))
	fmt.Fprintf(out, "health:     %d unhealthy capture(s)\n", unhealthy)
	fmt.Fprintf(out, "edition:    %d exact, %d off-by-one (benign), %d other delta, %d unparseable\n",
		exact, offbyone, other, unparseable)

	if len(absent) > 0 || unhealthy > 0 {
		fmt.Fprint(out, "\nAUDIT FAILED\n")
		return false, nil
	}
	fmt.Fprint(out, "\nAUDIT PASSED\n")
	return true, nil
}
