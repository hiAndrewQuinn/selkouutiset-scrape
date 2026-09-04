# Working in this repository

Notes for anyone — human or agent — changing code here. Read `README.md` first
for what the project is.

## The one rule

**Every calendar day from the first capture to the newest has exactly one file.
Never introduce a gap.**

Everything else is negotiable; this is not. Duplicate content on days Yle
published nothing is intended and is flagged downstream by hash. A missing day
is a defect, and `archive audit` exits non-zero for one.

## Layout

```
scripts/archive.go        the whole program: `capture` and `audit`
scripts/archive_test.go   15 tests
scripts/testdata/         one real capture, for the markup boundary
go.mod                    stdlib only — keep it that way
YYYY/MM/DD/*.html         the archive, ~1050 files and growing daily
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

## Never rewrite the data

The `YYYY/MM/DD/` files are the product. Do not relocate, deduplicate, or
"correct" them because a capture's directory date disagrees with the edition
inside it — that mismatch is normal and expected, and about 4% of the corpus has
it. Two exceptions were repaired by hand once (two committed CDN error pages);
if you find another, carry the previous day forward rather than leaving a hole.

## The fragile part

The edition date is parsed out of HTML with a regex anchored on the CSS class
`yle__article__heading--1`. This is genuinely brittle, and it is a deliberate
trade: parsing properly would mean a dependency, which the no-dependency rule
rules out. Mitigations, all of which matter:

- `scripts/testdata/real_capture.html` is a genuine capture, and
  `TestParsesRealCapture` runs the parser against it. If Yle changes the markup,
  that test goes red — which is the point. Do not replace it with a generated
  fixture.
- The styled-components classes *beside* that one (`aw__sc-xiym5b-0`, `aw-17z0l1m`)
  change between Yle builds. Only `yle__article__heading--1` has held for three
  years. Do not anchor on the others.
- Only the **first** heading in a page is the edition title; the rest are article
  headings. The page is one enormous line, so line-oriented tools do not help
  here — `grep -m1` fails for exactly this reason, which is a bug this project
  has already shipped once.

## Corpus oddities that are not bugs

Real inputs from the archive that any parser change must still survive. They are
all in `TestEditionDateFrom`:

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

## Dates

Resolve "today" in `Europe/Helsinki` (Yle's day, not the runner's — runners are
UTC and the edition lands at 16:45 local), then do all arithmetic on bare UTC
civil dates. Both halves matter. The shell version this replaced was bitten by
DST making a `+1 day` step 23 hours, which silently truncated day deltas to the
wrong integer.

## Workflows

- `scrape.yml` — 3×/day. Its critical path is fetch → file → commit → push. Do
  not add anything that can fail before the data is committed.
- `test.yml` — on push and PR, paths-filtered to code. Deliberately separate: a
  failing test must never block data collection, because a lost day cannot be
  recovered later. The front page only ever shows the newest edition.

## Things that were tried and rejected

Do not re-propose these without new information:

- **Naming files by edition date instead of capture date.** Collapses holiday
  stretches into one file and puts holes in the calendar. Violates the one rule.
- **A plausibility clamp on the parsed date.** Unnecessary: a bogus date can only
  select content for a day already found missing, so a date outside the archive
  or on a filled day matches nothing on its own.
- **Hard links for carried-forward days.** Today's slot is rewritten in place on
  every run, so a link would silently mutate the day it was carried from. Git
  dedupes identical blobs anyway.
- **Dropping the byte floor as redundant with `curl --fail`.** It still catches a
  truncated 200 and hand-runs against fixtures.
