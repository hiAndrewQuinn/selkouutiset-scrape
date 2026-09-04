# selkouutiset-scrape

A daily archive of [Yle Uutiset selkosuomeksi](https://yle.fi/selkouutiset) — the
Finnish national broadcaster's news in simplified Finnish, aimed at learners and
at readers who benefit from plain language.

Yle publishes one edition each weekday at about 16:45 Europe/Helsinki, plus
weekend round-ups. The front page only ever shows the newest one, and there is
no public archive of the older editions. This repository keeps them.

## What's in here

One HTML snapshot per calendar day, going back to 2023-10-26:

```
2026/08/27/selkouutiset_2026_08_27.html
2026/08/28/selkouutiset_2026_08_28.html
...
```

### The invariant

> **Every calendar day from the first capture to the newest has exactly one file.**

On days when Yle published nothing new — weekends, holidays, or a run that never
happened — that day's file repeats the previous edition. Duplicates are intended
and are detectable downstream by content hash. **There are never gaps.** An
unbroken day sequence is what makes the archive usable as a series.

Because of this, a file's directory date is *when it was captured*, not
necessarily the date of the edition inside it. Most days the two agree; about 4%
of the corpus is off by a day, and holiday stretches can repeat one edition for a
fortnight. `archive audit` reports the difference without treating it as an
error.

## How it works

`.github/workflows/scrape.yml` runs three times a day, fetches the front page
with `curl --fail`, and hands it to a small Go program:

```
go run ./scripts capture capture.html   # file a freshly fetched page
go run ./scripts audit                  # read-only invariant check
```

`capture` applies two rules:

1. **Today's slot is always written**, overwriting, so a later run the same day
   converges on that day's real edition.
2. **Every other missing day is filled** with the best content available: the
   fresh capture if the page advertises that day's edition, otherwise the
   preceding day carried forward.

Rule 2 is what makes an unreliable scheduler harmless. GitHub Actions' free-tier
crons are best-effort and are routinely delayed by hours or dropped outright;
the original version of this scraper named its output file from the clock at run
time, so a run that slipped past midnight filed the day's edition under the
*next* day, and the following day's run then overwrote it. That destroyed 40
editions and misattributed dozens more before it was found. Deriving the day from
the page itself, and filling gaps from the archive, removes the scheduler from
the equation.

The day comes from the article's own `datePublished` metadata, which every
capture carries exactly once and which describes the edition rather than when
the page was fetched. The Finnish headline (`Selkouutiset | torstai 27.8.2026`)
is parsed as a fallback. Where the two disagree the metadata is right: Yle's
headlines carry occasional typos, including wrong years and, on one day, a
weekday number a day behind the weekday name.

`audit` checks the same invariant read-only: missing days, captures that are
undersized or lack an edition heading (which is how CDN error pages twice ended
up committed as news data), and path-vs-edition drift.

## Running it yourself

Go 1.24+, no dependencies — the program is standard library only.

```sh
go run ./scripts audit                       # check the archive
go run ./scripts -root /tmp/copy audit       # check somewhere else
go test ./...                                # 15 tests, ~20ms
```

## Licence and use

The captured HTML is Yle's copyrighted material, mirrored here for personal
study and research. The scraping code is yours to use freely. If you want the
news itself, read it at [yle.fi/selkouutiset](https://yle.fi/selkouutiset) —
Yle does the hard part.
