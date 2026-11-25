# selkouutiset-scrape

A daily archive of [Yle Uutiset selkosuomeksi](https://yle.fi/selkouutiset) — the
Finnish national broadcaster's news in simplified Finnish, aimed at learners and
at readers who benefit from plain language.

Yle publishes one edition each weekday at about 16:45 Europe/Helsinki, plus
weekend round-ups. The front page only ever shows the newest one, and there is
no public archive of the older editions. This repository keeps them.

## What's in here

One HTML snapshot per calendar day, going back to 2016-09-04 (nightly captures since 2023-10-26, the rest recovered from yle.fi):

```
2026/08/27/selkouutiset_2026_08_27.html
2026/08/28/selkouutiset_2026_08_28.html
...
```

### The invariant

> **Every calendar day from the first capture to the newest has exactly one file,
> holding the newest edition Yle published on or before that day.**

On days when Yle published nothing new — holidays, and some weekends — that
day's file therefore repeats the previous edition. Repeats are intended, and are
identified downstream by the edition's `datePublished` (not by content hash:
Yle's pages carry per-request bytes, so two captures of one edition never hash
the same). **There are never gaps.** An unbroken day sequence is what makes the
archive usable as a series.

So a file's directory date equals the edition date inside it whenever Yle
published that day; otherwise it holds the most recent earlier edition, and
holiday stretches can repeat one edition for a fortnight. `archive audit`
counts repeats without treating them as errors.

## How it works

`.github/workflows/scrape.yml` runs three times a day, fetches the front page
with `curl --fail`, and hands it to a small Go program:

```
go run ./scripts capture capture.html          # file a freshly fetched front page
go run ./scripts feed feed.xml                 # list feed editions the archive lacks
go run ./scripts backfill page.html...         # file edition pages by their own date
go run ./scripts audit                         # read-only invariant check
```

`capture` applies three rules:

1. **Today's slot is written**, overwriting, so a later run the same day
   converges on that day's real edition (unless it already holds a *newer*
   edition — a stale CDN copy must not roll it back).
2. **The edition is filed under its own day**, and every later day still holding
   an older edition is upgraded to it. Nothing is ever downgraded.
3. **Every missing day is filled** by carrying the preceding day forward.

Rule 2 is what makes an unreliable scheduler harmless. GitHub Actions' free-tier
crons are best-effort: in November 2026 the 15:00 UTC run was routinely starting
six to eight hours late, which in winter is after midnight in Helsinki. The run
then woke on the day *after* the edition it saw. The previous rules only filled
days that were *missing*, so the edition's own day kept the previous edition,
and whenever the next day had an edition of its own the late one was lost
outright — seven editions between October and November 2026, before that,
40 under the very first version of this scraper. Filing by the page's own date,
and upgrading stale days, removes the scheduler from the equation.

Then, after the capture is committed, the workflow reads Yle's RSS feed
(`https://yle.fi/rss/selkouutiset`, about three weeks of editions with their
article URLs). `archive feed` prints each edition the archive is missing or holds
stale, the workflow fetches those articles from yle.fi, and `archive backfill`
files them. So even a day no run saw at all heals itself on the next run.

The day comes from the article's own `datePublished` metadata, which every
capture carries exactly once and which describes the edition rather than when
the page was fetched. The Finnish headline (`Selkouutiset | torstai 27.8.2026`)
is parsed as a fallback. Where the two disagree the metadata is right: Yle's
headlines carry occasional typos, including wrong years and, on one day, a
weekday number a day behind the weekday name.

`audit` checks the same invariant read-only: missing days, captures that are
undersized or lack an edition heading (which is how CDN error pages twice ended
up committed as news data), path-vs-edition drift, and how many days repeat the
previous day's edition.

## Going further back

The archive's own captures start on 2023-10-26, but Yle keeps old editions online
at `https://yle.fi/a/<id>` in the same markup — radio and TV editions back to
2016. What it lacks is an index. `tools/history.py` builds one from the Wayback
Machine (old article URLs whose slugs spell the date, snapshots of the RSS feeds,
and snapshots of the front page), then fetches each edition fresh from yle.fi and
checks its `datePublished` before anything is filed:

```sh
uv run tools/history.py collect --archive . --since 2016-09-01 --out /tmp/history
uv run tools/history.py fetch --out /tmp/history
go run ./scripts backfill $(cat /tmp/history/backfill-order.txt)
go run ./scripts audit
```

Both steps cache what they download and are safe to re-run; `backfill` applies
the archive's own rules, so days with no recoverable edition are carried forward
like any other gap. See [`tools/history.py`](tools/history.py) for the details.

This was run on 2026-11-24 and filled 2016-09-04 to 2023-10-25:

| | days |
|---|---|
| needed (before the first capture, or holding the wrong edition) | 2,682 |
| a candidate edition found in the Wayback Machine | 2,288 |
| fetched from yle.fi and verified | 2,277 |
| carried forward (Yle published nothing, or no copy survives) | 335 |

Most of the carried-forward days are holidays, plus gaps in Wayback coverage:
much of 2017-2018 and April-October 2023. The same run also repaired 12 days inside
the existing archive that held the previous day's edition (2024-01-28, 2024-02-11,
2025-04-27, 2026-03-14, and eight November 2026 days from the live feed).
Editions that never reached the archive or the Wayback Machine (2026-10-05,
2026-10-12, 2026-11-02) cannot be recovered.

### `lost.txt`

[`lost.txt`](lost.txt) lists the 316 days whose edition is lost media: Yle
published it, but no copy survives anywhere. Their folders hold the previous
edition, carried forward like any gap; the cleaned pipeline marks them
`"lost": true` in `edition.json`, and the website lists them greyed out as
"(lost media)". Before 2023-10-26 a carried-forward day counts as lost unless it
falls on a day Yle normally skips (20 Dec-7 Jan, Easter, May Day, Ascension,
Midsummer, Independence Day); from 2023-10-26, when the scraper saw every day
itself, only editions known to have existed are listed. If a copy ever turns
up, file it with `archive backfill` and delete its line.

## Running it yourself

Go 1.24+, no dependencies — the program is standard library only.

```sh
go run ./scripts audit                       # check the archive
go run ./scripts -root /tmp/copy audit       # check somewhere else
go test ./...                                # 32 tests, ~30ms
```

## Licence and use

The captured HTML is Yle's copyrighted material, mirrored here for personal
study and research. The scraping code is yours to use freely. If you want the
news itself, read it at [yle.fi/selkouutiset](https://yle.fi/selkouutiset) —
Yle does the hard part.
