// Command archive maintains the selkouutiset daily capture archive.
//
//	archive capture <fetched.html> [YYYY-MM-DD]   file a freshly fetched front page
//	archive backfill <page.html>...               file edition pages by their own date
//	archive feed <rss.xml> [YYYY-MM-DD]           list feed editions the archive lacks
//	archive audit                                 read-only invariant check
//
// The archive holds exactly one file per calendar day under
// YYYY/MM/DD/selkouutiset_YYYY_MM_DD.html, and that file holds the newest
// edition Yle published on or before that day. On days when Yle published
// nothing new, the file therefore repeats the previous edition; downstream
// tooling identifies those repeats by the edition's datePublished. There are
// never gaps.
package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

// publishedRe finds the article's own publication timestamp, which Yle emits as
// schema.org metadata. This is the primary anchor: it is present in all 1044
// archived captures, exactly once each, and it describes the *edition* rather
// than when the page was rendered -- a capture taken at 01:56 showing the
// previous day's edition still reports that edition's date.
//
// Only the ten-character date prefix is taken. The timestamp is always Helsinki
// local time (only +0300 and +0200 appear across the corpus, never UTC) and the
// latest publication hour on record is 23:00, so no capture sits near a
// midnight boundary and no offset arithmetic is needed.
var publishedRe = regexp.MustCompile(`"datePublished":"(\d{4}-\d{2}-\d{2})`)

// anchor names where a capture's edition date came from.
type anchor string

const (
	anchorPublished anchor = "datePublished"
	anchorHeading   anchor = "heading"
	anchorNone      anchor = ""
)

// editionFromPublished reads the schema.org publication date.
func editionFromPublished(page []byte) (time.Time, bool) {
	m := publishedRe.FindSubmatch(page)
	if m == nil {
		return time.Time{}, false
	}
	day, err := time.Parse(isoDate, string(m[1]))
	return day, err == nil
}

// editionFromHeading reads the date out of the Finnish headline. This is the
// fallback, kept because it is independent of the metadata: if Yle ever stops
// emitting datePublished, captures keep landing on the right day.
func editionFromHeading(page []byte) (time.Time, bool) {
	return editionDateFrom(editionHeading(page))
}

// editionDate resolves which edition a page carries, preferring the metadata.
//
// Where the two anchors disagree the headline is the one that is wrong: across
// the corpus they differed on 15 captures, and the Finnish weekday name -- an
// independent witness -- backed datePublished on 13 of them. Yle's headlines
// carry real typos, including a wrong weekday number and several wrong years
// ("Sunnuntai 21.4.2109"); recovering misdated editions in 2026-11 raised the
// count to 24, every one confirmed by datePublished.
func editionDate(page []byte) (time.Time, anchor) {
	if day, ok := editionFromPublished(page); ok {
		return day, anchorPublished
	}
	if day, ok := editionFromHeading(page); ok {
		return day, anchorHeading
	}
	return time.Time{}, anchorNone
}

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
//
// The gate asks for what the program actually needs: enough bytes to be a real
// page, and an edition date from either anchor. Requiring one specific anchor
// would turn a cosmetic change by Yle -- renaming a CSS class, or dropping the
// schema.org block -- into a data-loss outage, opening exactly the gaps this
// archive exists to prevent. Losing both still fails, loudly.
func healthProblem(page []byte) string {
	if len(page) < minCaptureBytes {
		return fmt.Sprintf("%d bytes, below the %d-byte floor", len(page), minCaptureBytes)
	}
	if _, from := editionDate(page); from == anchorNone {
		return "carrying no edition date (no datePublished, no parseable heading)"
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
			"       archive [-root DIR] backfill <page.html>...\n"+
			"       archive [-root DIR] feed <rss.xml> [YYYY-MM-DD]\n"+
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
	case "backfill":
		if len(args) < 2 {
			fatal(2, "usage: archive backfill <page.html>...")
		}
		today, err := captureDay(nil)
		if err != nil {
			fatal(2, "%v", err)
		}
		rejected, err := runBackfill(os.Stdout, *root, args[1:], today)
		if err != nil {
			fatal(1, "%v", err)
		}
		if rejected > 0 {
			fatal(1, "%d page(s) rejected; the rest were filed", rejected)
		}
	case "feed":
		if len(args) < 2 {
			fatal(2, "usage: archive feed <rss.xml> [YYYY-MM-DD]")
		}
		today, err := captureDay(args)
		if err != nil {
			fatal(2, "%v", err)
		}
		if err := runFeed(os.Stdout, *root, args[1], today); err != nil {
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

// slotEdition reports which edition a day's slot holds. exists is false when
// the day has no file. A file whose edition cannot be read reports the zero
// time, which sorts before every real edition, so any real page upgrades it.
func slotEdition(root string, day time.Time) (edition time.Time, exists bool, err error) {
	page, err := os.ReadFile(filepath.Join(root, pathFor(day)))
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if healthProblem(page) != "" {
		return time.Time{}, true, nil
	}
	edition, _ = editionDate(page)
	return edition, true, nil
}

// fileEdition writes a page carrying the given edition into the archive under
// the edition's own day, and replaces any stale carry-forward after it.
//
// The archive's invariant is that a day's file holds the newest edition
// published on or before that day. So, walking forward from the edition's day:
//
//   - the edition's own slot is written if it is missing or holds an older
//     edition (a carry-forward written before this edition was seen);
//   - each later slot still holding an older edition is upgraded too, since
//     this edition is the newest one known for that day as well;
//   - the walk stops at the first slot holding this edition or a newer one.
//     Everything after it is at least as new. Nothing is ever downgraded.
//
// Missing slots after the edition's day are left for fillGaps, which carries
// the preceding day forward into them.
//
// This is what makes a late scheduler harmless. GitHub Actions starts the
// afternoon cron six to eight hours late, which in winter is past midnight in
// Helsinki: the run then wakes on day D+1, and the old rules filed edition D
// only under D+1, leaving D holding D-1. When D+1 had an edition of its own,
// D was lost outright. Here D is upgraded no matter when the run happens.
func fileEdition(out io.Writer, root, src string, edition, until time.Time) error {
	for d := edition; !d.After(until); d = d.AddDate(0, 0, 1) {
		held, exists, err := slotEdition(root, d)
		if err != nil {
			return err
		}
		switch {
		case !exists && d.Equal(edition):
			if err := place(filepath.Join(root, pathFor(d)), src); err != nil {
				return err
			}
			fmt.Fprintf(out, "filed    %s  <- edition %s\n", d.Format(isoDate), edition.Format(isoDate))
		case !exists:
			continue
		case !held.Before(edition):
			return nil
		default:
			if err := place(filepath.Join(root, pathFor(d)), src); err != nil {
				return err
			}
			fmt.Fprintf(out, "upgraded %s  <- edition %s (slot held %s)\n",
				d.Format(isoDate), edition.Format(isoDate), describeEdition(held))
		}
	}
	return nil
}

// describeEdition formats a held edition for the log; the zero time means the
// slot held a page with no readable edition.
func describeEdition(edition time.Time) string {
	if edition.IsZero() {
		return "an unreadable page"
	}
	return edition.Format(isoDate)
}

// archiveSpan returns the oldest and newest archived days. ok is false for an
// empty archive.
func archiveSpan(root string) (oldest, newest time.Time, ok bool, err error) {
	captures, err := listCaptures(root)
	if err != nil || len(captures) == 0 {
		return time.Time{}, time.Time{}, false, err
	}
	if oldest, err = dateForPath(captures[0]); err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if newest, err = dateForPath(captures[len(captures)-1]); err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	return oldest, newest, true, nil
}

// fillGaps fills every missing day between from and to inclusive by carrying
// the preceding day forward. The preceding day is always present: either it
// was never missing, or an earlier iteration filled it, since missingDays is
// ordered. from must itself be present.
//
// It considers the whole span rather than just the tail, so a hole that opened
// up behind an already-archived day is repaired too.
func fillGaps(out io.Writer, root string, from, to time.Time) error {
	captures, err := listCaptures(root)
	if err != nil {
		return err
	}
	for _, day := range missingDays(haveSet(captures), from, to) {
		prev := pathFor(day.AddDate(0, 0, -1))
		if err := place(filepath.Join(root, pathFor(day)), filepath.Join(root, prev)); err != nil {
			return err
		}
		fmt.Fprintf(out, "gap      %s  <- %s (carried forward)\n",
			day.Format(isoDate), filepath.Dir(prev))
	}
	return nil
}

// readEdition loads a page and resolves its edition, applying the health gate.
func readEdition(path string) (page []byte, edition time.Time, err error) {
	page, err = os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	// The workflow's `curl --fail` already rejects an HTTP error, but this is
	// also run by hand and against fixtures, so it checks what it was given.
	if problem := healthProblem(page); problem != "" {
		return nil, time.Time{}, fmt.Errorf("%s is %s\n       refusing to archive it", path, problem)
	}
	edition, _ = editionDate(page) // the health gate guarantees an anchor
	return page, edition, nil
}

// runCapture files a freshly fetched front page into the archive.
//
// Three rules:
//
//  1. Today's slot is written, overwriting, so a later run the same day
//     converges on that day's real edition. The one exception is a slot that
//     already holds a newer edition than the capture: a stale CDN copy must not
//     roll today back.
//  2. The edition is filed under its own day, upgrading stale carry-forwards
//     (see fileEdition). This is what makes a run delayed past midnight
//     harmless.
//  3. Every missing day is filled by carrying the preceding day forward.
//
// An edition dated before the archive's first day, or after today, is not
// filed by rule 2: a front-page capture never extends the archive backwards
// (that is what backfill is for), and a date in the future can only be a typo.
// A wrong date inside the archive is harmless too, since rule 2 never
// downgrades a slot: it only selects content for days holding something older.
func runCapture(out io.Writer, root, capturePath string, today time.Time) error {
	page, edition, err := readEdition(capturePath)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}

	// --- Rule 1: today's slot always exists ---
	held, exists, err := slotEdition(root, today)
	if err != nil {
		return err
	}
	if exists && held.After(edition) {
		fmt.Fprintf(out, "kept     %s  (holds edition %s, newer than the capture's %s)\n",
			today.Format(isoDate), held.Format(isoDate), edition.Format(isoDate))
	} else {
		if err := place(filepath.Join(root, pathFor(today)), capturePath); err != nil {
			return err
		}
		fmt.Fprintf(out, "today    %s  <- capture (%d bytes, edition %s)\n",
			today.Format(isoDate), len(page), edition.Format(isoDate))
	}

	oldest, _, _, err := archiveSpan(root) // rule 1 guarantees a non-empty archive
	if err != nil {
		return err
	}

	// --- Rule 2: the edition's own day, and stale days after it ---
	if !edition.Before(oldest) && !edition.After(today) {
		if err := fileEdition(out, root, capturePath, edition, today); err != nil {
			return err
		}
	}

	// --- Rule 3: no gaps anywhere in the archive ---
	return fillGaps(out, root, oldest, today)
}

// runBackfill files edition pages fetched from anywhere -- an article URL from
// the RSS feed, or a historical edition found through the Wayback Machine --
// each under its own edition date. Unlike capture it may extend the archive
// backwards, and it never writes a day it has no edition for except by
// carrying the preceding day forward into a gap.
//
// Pages are filed newest edition first. That keeps a large historical backfill
// linear: each older page lands just before the archive's current first day,
// so its forward walk crosses only the gap between them. Filed oldest first,
// every page would upgrade the carry-forwards of every page before it.
// Among pages carrying the same edition date the first one given wins, so the
// caller expresses a preference (radio over TV, say) through argument order.
//
// An unhealthy page is reported and skipped; the rest are still filed. The
// number skipped is returned so the caller can exit non-zero.
func runBackfill(out io.Writer, root string, pages []string, today time.Time) (rejected int, err error) {
	type candidate struct {
		path    string
		edition time.Time
	}
	var cands []candidate
	for _, p := range pages {
		_, edition, err := readEdition(p)
		if err != nil {
			fmt.Fprintf(out, "rejected %v\n", err)
			rejected++
			continue
		}
		if edition.After(today) {
			fmt.Fprintf(out, "rejected %s: edition %s is in the future\n", p, edition.Format(isoDate))
			rejected++
			continue
		}
		cands = append(cands, candidate{p, edition})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].edition.After(cands[j].edition) })

	for _, c := range cands {
		_, newest, ok, err := archiveSpan(root)
		if err != nil {
			return rejected, err
		}
		until := c.edition
		if ok && newest.After(until) {
			until = newest
		}
		if err := fileEdition(out, root, c.path, c.edition, until); err != nil {
			return rejected, err
		}
	}

	oldest, newest, ok, err := archiveSpan(root)
	if err != nil || !ok {
		return rejected, err
	}
	return rejected, fillGaps(out, root, oldest, newest)
}

// feedItem is one <item> of Yle's selkouutiset RSS feed, which lists the last
// few weeks of editions with their article URLs.
type feedItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
}

// feedDay resolves an RSS pubDate to Yle's calendar day. The feed gives
// Helsinki offsets today, but the conversion makes that an observation rather
// than an assumption.
func feedDay(pubDate string, zone *time.Location) (time.Time, bool) {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(layout, strings.TrimSpace(pubDate)); err == nil {
			day, err := time.Parse(isoDate, t.In(zone).Format(isoDate))
			return day, err == nil
		}
	}
	return time.Time{}, false
}

// runFeed prints the article URL of every feed edition the archive is missing
// or holds only a stale carry-forward for, one per line, for the workflow to
// fetch and hand to backfill. It reads only; it never writes the archive.
//
// This is the safety net under capture. The front page only ever shows the
// newest edition, so an edition that was never on the front page while a run
// happened used to be lost for good. The feed reaches back about three weeks,
// so any day missed in that window is recovered on the next run.
func runFeed(out io.Writer, root, feedPath string, today time.Time) error {
	raw, err := os.ReadFile(feedPath)
	if err != nil {
		return fmt.Errorf("cannot read feed: %w", err)
	}
	var doc struct {
		Items []feedItem `xml:"channel>item"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("feed is not valid RSS: %w", err)
	}
	if len(doc.Items) == 0 {
		return errors.New("feed carries no items")
	}
	zone, err := time.LoadLocation("Europe/Helsinki")
	if err != nil {
		return err
	}
	for _, it := range doc.Items {
		day, ok := feedDay(it.PubDate, zone)
		link := strings.TrimSpace(it.Link)
		if i := strings.IndexByte(link, '?'); i >= 0 {
			link = link[:i] // drop ?origin=rss
		}
		if !ok || link == "" || day.After(today) {
			continue
		}
		held, exists, err := slotEdition(root, day)
		if err != nil {
			return err
		}
		if !exists || held.Before(day) {
			fmt.Fprintln(out, link)
		}
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
	var fromPublished, fromHeading, disagree int
	// repeats counts days holding the same edition as the day before: Yle
	// published nothing that day, or the edition was lost and not yet recovered.
	var repeats int
	var prevEdition time.Time
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
		// Both anchors are resolved, not just the winner: the fallback count is
		// the canary. It reads zero today, and goes non-zero the moment Yle
		// stops emitting datePublished -- without it the fallback would absorb
		// a markup change silently.
		published, hasPublished := editionFromPublished(page)
		heading, hasHeading := editionFromHeading(page)
		var edition time.Time
		switch {
		case hasPublished:
			edition = published
			fromPublished++
			if hasHeading && !heading.Equal(published) {
				disagree++
			}
		case hasHeading:
			edition = heading
			fromHeading++
		default:
			unparseable++
			continue
		}
		if edition.Equal(prevEdition) {
			repeats++
		}
		prevEdition = edition
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
	fmt.Fprintf(out, "repeats:    %d day(s) hold the previous day's edition\n", repeats)
	fmt.Fprintf(out, "anchors:    %d datePublished, %d heading fallback\n", fromPublished, fromHeading)
	fmt.Fprintf(out, "disagree:   %d capture(s) where the anchors differ\n", disagree)

	if len(absent) > 0 || unhealthy > 0 {
		fmt.Fprint(out, "\nAUDIT FAILED\n")
		return false, nil
	}
	fmt.Fprint(out, "\nAUDIT PASSED\n")
	return true, nil
}
