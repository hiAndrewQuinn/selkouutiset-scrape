# selkouutiset-scrape

A daily archive of [Yle Uutiset selkosuomeksi](https://yle.fi/selkouutiset) — the
Finnish national broadcaster's news in simplified Finnish, aimed at learners and
at readers who benefit from plain language.

Yle publishes one edition each weekday at about 16:45 Europe/Helsinki, plus
weekend round-ups. The front page only ever shows the newest one, and there is
no public archive of the older editions. This repository keeps them.

## What's in here

One HTML snapshot per calendar day, going back to 2014-09-20 (nightly captures since 2023-10-26, the rest recovered from yle.fi):

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
September 2014. What it lacks is an index. `tools/history.py` builds one from the Wayback
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
Editions that never reached the archive or the Wayback Machine were listed as
lost; the second sweep below found most of them, 2026-10-05, 2026-10-12 and
2026-11-02 included.

A second run on 2026-11-27 went back to 2014. Before autumn 2016 the editions
sat directly under `/uutiset/` (`yle.fi/uutiset/maanantai_10112014/7613741`),
so `collect` now reads those slugs too, and `fetch` also checks each page's
JSON-LD names `"publisher":{"name":"Yle Selkouutiset"}`. It filed 2014-09-20
(the earliest edition yle.fi still serves) to 2016-09-03:

| | days |
|---|---|
| a candidate edition found in the Wayback Machine | 683 |
| fetched from yle.fi and verified | 678 |
| carried forward | 37 |

Of the carried-forward days, 17 are days Yle usually skips; the other 20 are
listed in `lost.txt`, twelve of them in December 2015.

### Finding editions without an index

Yle assigns article IDs in increasing order, and the edition titles name their
day ("Tiistai 6.6.2017 (radio)", "Radio Selkouutiset keskiviikko 13.9.2023").
So an edition nobody indexed can still be found: estimate its ID from the
editions on either side and read the titles of the IDs around the estimate.
`tools/probe_ids.py` does this, reading only the head of each page, and walks
runs of lost days one at a time, each find anchoring the next.
`tools/cc_index.py` adds a second, independent index: Common Crawl's record of
the old site's dated URLs. `tools/fetch_ids.py` then fetches each candidate
from yle.fi and keeps it only if its `datePublished` is the day:

```sh
grep -v '^#' lost.txt | cut -f1 > /tmp/probe/days.txt
uv run tools/cc_index.py /tmp/probe/cc /tmp/probe/days.txt > /tmp/probe/cc.tsv
uv run tools/probe_ids.py . /tmp/probe/cache /tmp/probe/days.txt /tmp/probe/found.tsv
uv run tools/fetch_ids.py /tmp/probe/fetched /tmp/probe/cc.tsv /tmp/probe/found.tsv
go run ./scripts backfill $(cat /tmp/probe/fetched/order.txt)
go run ./scripts audit
```

This was run on 2026-11-25/26 against the 316 days then in `lost.txt`:

| | days |
|---|---|
| lost before the sweep | 316 |
| found by probing article IDs | 288 |
| named by Common Crawl's index (60, nearly all also found by probing) | 60 |
| fetched from yle.fi and verified, now filed | 292 |
| still lost | 24 |

Probing made about 164,000 requests (most are 404s), roughly 570 per day found,
at about one request per second per worker.

A third pass the same day took the remaining 24 days from 24 to 3. The lesson
was that Yle's titles are hand-typed: "Perjantai 19.1. 2018", "Tiistai
21.2.2020" (for 21.1.), "Keskiviikko 12.9.2019" (for 12.6.), "Sunnuntai
21.4.2109". The old site's daily quiz pages, found in Common Crawl and the
Wayback Machine, linked their day's edition under these misdated slugs, which
gave it away. `probe_ids.py` now lists every edition-shaped title between a
lost day's neighbours as a candidate, whatever date it names, and lets
`datePublished` decide. That found 16 days; fully scanning the ID ranges of
the rest found 3 more whose editions sat just outside the probe window.

Two editions were posted after midnight, so their `datePublished` names the
next day, which has an edition of its own: the TV edition titled 7.3.2019
(posted 08:37 on the 8th) and the radio edition titled 14.4.2019 (00:01 on
the 15th). `archive backfill` files by `datePublished` and cannot place them,
so they were copied by hand to `2019/03/07/` and `2019/04/14/`. The audit
counts them as benign off-by-one days, and since their edition is newer than
their day, the capture rules never touch them.

| | days |
|---|---|
| still lost after the second sweep | 24 |
| recovered from misdated titles | 16 |
| recovered by scanning ID ranges completely | 3 |
| filed by hand (posted after midnight) | 2 |
| still lost | 3 |

A fourth pass resolved the last three by looking sideways rather than harder:

- **2022-07-06**: the Wayback Machine's capture of the old site's archive
  page, taken at 18:29 that evening, links `keskiviikko_572022/12526285`. The
  edition is titled "Keskiviikko 5.7.2022", with the previous day's date and,
  like all mid-2022 editions, no "(radio)" suffix, so the misdated-title
  filter (which required the suffix) passed over it although the prober had
  cached its title.
- **2022-05-01**: listing every non-404 title between the neighbouring
  editions turned up "Sunnunta 1.5.2022 (radio)" (3-12425039): the weekday is
  misspelt, so no weekday pattern matched it.
- **2023-03-18**: Yle's own front page, captured by the Wayback Machine on the
  20th, lists every edition from 10.3. to 19.3. except the 18th. Every ID
  between the 17th's and 19th's editions is 404 and was never archived, and no
  crawl knows a `lauantai_1832023` URL. No web edition seems to have been
  published that day, so it is now an ordinary carried-forward gap.

`probe_ids.py`'s filter now accepts the first five letters of a weekday
followed by a date, with or without the suffix; re-run on the archive as it
was, it names both pages and `fetch_ids.py` verifies them.

| | days |
|---|---|
| still lost after the third pass | 3 |
| recovered (title typos the filter missed) | 2 |
| apparently never published | 1 |
| still lost | 0 |

### `lost.txt`

[`lost.txt`](lost.txt) lists the days whose edition is lost media: Yle
published it, but no copy survives anywhere. It is empty since 2026-11-26. Their folders hold the previous
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

## The 2006–2009 podcast feed

From 2006 to early 2009 Yle also published Selkouutiset as a podcast, and the
feed's `<description>` carried the full text of each broadcast. Those pages are
long gone from yle.fi, but the Wayback Machine kept snapshots of the feed.
`tools/podcast_feed.py` reads every snapshot and files each broadcast's
`<item>` verbatim, one file per day, under its own directory:

```
podcast-feed/2008-04-30.xml   <!-- the snapshot it came from --> + the <item>
```

That came to 632 broadcasts, 27.4.2006 – 5.2.2009 (the programme went out
Sunday to Thursday). They live outside the `YYYY/MM/DD/` tree because they are
not captures of Yle's page and carry no `datePublished`; the invariant above
covers only the daily captures. selkouutiset-scrape-cleaned turns them into
day pages.

```sh
uv run tools/podcast_feed.py --out podcast-feed --cache /tmp/feed-cache
```

## Licence and use

The captured HTML is Yle's copyrighted material, mirrored here for personal
study and research. The scraping code is yours to use freely. If you want the
news itself, read it at [yle.fi/selkouutiset](https://yle.fi/selkouutiset) —
Yle does the hard part.
