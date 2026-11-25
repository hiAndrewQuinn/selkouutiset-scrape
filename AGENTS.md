# Working in this repository

Notes for anyone — human or agent — changing code here. Read `README.md` first
for what the project is.

## The one rule

**Every calendar day from the first capture to the newest has exactly one file.
Never introduce a gap.**

Everything else is negotiable; this is not. Each day's file holds the newest
edition published on or before that day, so on days Yle published nothing the
previous edition repeats. That is intended, and downstream identifies repeats by
`datePublished` (not by hash: two captures of one edition never hash the same).
A missing day is a defect, and `archive audit` exits non-zero for one.

## Layout

```
scripts/archive.go        the whole program: `capture`, `feed`, `backfill`, `audit`
scripts/archive_test.go   32 tests
scripts/testdata/         one real capture, for the markup boundary
tools/history.py          one-off: finds pre-archive editions via the Wayback Machine
go.mod                    stdlib only — keep it that way
YYYY/MM/DD/*.html         the archive, one file per day, growing daily
```

`go.mod` has no dependencies and should stay that way: the workflow runs
`go build` on a cold runner with no module cache, and a dependency would mean a
network fetch in the data-collection path. `golang.org/x/net/html` is the
tempting one — resist it; see "the fragile part" below.

## Before you change anything

```sh
go test ./...          # fast, hermetic, uses t.TempDir()
go vet ./... && gofmt -l .
go run ./scripts audit # ~250ms against the real 1044-file archive
```

The archive is 128 MB. Tests never touch it — they build a temporary archive via
the `-root` flag. Keep it that way; if a change makes you want to test against
the real tree, that is a sign the seam is wrong.

## Never rewrite the data by hand

The `YYYY/MM/DD/` files are the product. Do not relocate, deduplicate, or
hand-edit them. A day holding an *earlier* edition than its own date is normal:
it is a repeat, on a day Yle published nothing.

The program does rewrite one kind of file, and only one: a day still holding an
older edition when a newer one published on or before that day turns up (a
carry-forward written before the real edition was seen). `fileEdition` upgrades
those, never downgrades anything, and that is how late runs and the RSS feed
repair lost days. If you find a damaged file (two committed CDN error pages were
once repaired by hand), the same rule applies: carry the previous day forward
rather than leaving a hole, and let the next capture or backfill upgrade it.

## How the edition date is found

Two anchors, in order:

1. **`"datePublished"`** — the article's own schema.org timestamp. Present in all
   1044 captures, exactly once each, always Helsinki local time (only `+0300`
   and `+0200` appear, never UTC), never later than 23:00. Only the ten-character
   date prefix is used, so there is no offset arithmetic and no midnight-boundary
   case. This is the primary anchor and it should stay that way.
2. **The Finnish headline** — the fallback, anchored on the CSS class
   `yle__article__heading--1`. Kept because it is *independent* of the metadata:
   if Yle stops emitting `datePublished`, captures keep landing on the right day.

`audit` reports which anchor each capture used. The `heading fallback` count is
the canary — it reads 0 today, and goes non-zero the moment the metadata
disappears. If you see it climbing, Yle changed something.

Where the two disagree, **the metadata is right and the headline is wrong** —
15 captures, and the Finnish weekday name backs the metadata on 13 of them.
`2026/06/24` reads "keskiviikko 23.6.2026" although 23 June 2026 was a Tuesday.
Do not "fix" this by preferring the headline.

The health gate deliberately accepts a page carrying *either* anchor. Requiring
one specific one would turn a cosmetic change by Yle into a data-loss outage,
opening exactly the gaps this archive exists to prevent.

## Corpus oddities that are not bugs

These affect the **fallback** parser only — the metadata anchor sidesteps all of
them — but the fallback has to keep working, so any change to it must still
survive these. They are all in `TestEditionDateFrom`:

| Input | Note |
|---|---|
| `Selkouutiset \|tiistai 21.4.2026` | no space after the pipe |
| `... \| lauantai  23.5.2026` | double space |
| `... \| Sunnuntai 15.2.2026` | capitalised weekday |
| `... \| torstai 28.5.20206` | five-digit year; must **not** parse |
| `07.08.2026` | zero-padded |

Three title forms exist: `Selkouutiset`, `Viikon uutinen selkosuomeksi`, and
`Uutisviikko selkosuomeksi`. The weekday name is never parsed — only the trailing
`D.M.YYYY`. Do not add weekday handling; it will break.

`scripts/testdata/real_capture.html` is a genuine capture, and the tests run both
anchors against it. If Yle changes the markup, that goes red — which is the
point. Do not replace it with a generated fixture. The styled-components classes
sitting beside `yle__article__heading--1` (`aw__sc-xiym5b-0`, `aw-17z0l1m`) change
between Yle builds; only that one has held for three years. Note also that only
the **first** heading in a page is the edition title, and the page is a single
enormous line, so line-oriented tools do not help — `grep -m1` fails for exactly
this reason, a bug this project has already shipped once.

## Dates

Resolve "today" in `Europe/Helsinki` (Yle's day, not the runner's — runners are
UTC and the edition lands at 16:45 local), then do all arithmetic on bare UTC
civil dates. Both halves matter. The shell version this replaced was bitten by
DST making a `+1 day` step 23 hours, which silently truncated day deltas to the
wrong integer.

## Workflows

- `scrape.yml` — 3×/day. Its critical path is fetch → file → commit → push. Do
  not add anything that can fail before the data is committed. After that push,
  a `continue-on-error` step reads Yle's RSS feed and backfills any edition of
  the last ~3 weeks the archive is missing or holds stale, then commits again.
- `test.yml` — on push and PR, paths-filtered to code. Deliberately separate: a
  failing test must never block data collection, because a lost day cannot be
  recovered later. The front page only ever shows the newest edition.

## Things that were tried and rejected

Do not re-propose these without new information:

- **Naming files by edition date instead of calendar day.** Collapses holiday
  stretches into one file and puts holes in the calendar. Violates the one rule.
  (Files are named by calendar day and *hold* the newest edition on or before
  it; that is different, and is what `fileEdition` maintains.)
- **Only filling days that are missing.** The rule before November 2026. A run
  delayed past midnight then left the edition's own day holding the previous
  edition, and lost the edition outright whenever the next day had one too.
  Seven editions went that way in October–November 2026.
- **Searching yle.fi for old editions.** Yle's search lives under `/haku/`,
  which robots.txt disallows. `tools/history.py` gets its index from the Wayback
  Machine instead, and fetches only the article pages (`/a/`), which are allowed.
- **A plausibility clamp on the parsed date.** Unnecessary: a bogus date can only
  select content for a day already found missing, so a date outside the archive
  or on a filled day matches nothing on its own.
- **Hard links for carried-forward days.** Today's slot is rewritten in place on
  every run, so a link would silently mutate the day it was carried from. Git
  dedupes identical blobs anyway.
- **Dropping the byte floor as redundant with `curl --fail`.** It still catches a
  truncated 200 and hand-runs against fixtures.
- **Adding `golang.org/x/net/html` to parse the page properly.** The cost is
  trivial (3.2 s on a cold module cache), but it would make things *worse*: a DOM
  query would anchor on the same CSS class, and scoping the metadata search to
  `<script type="application/ld+json">` blocks — which is what a parser buys —
  misses 15 captures whose `datePublished` sits in the Next.js payload instead.
  A targeted match on the key finds all 1044. The anchor was the lever, not the
  parser.
