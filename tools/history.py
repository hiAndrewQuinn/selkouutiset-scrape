# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Find and fetch Selkouutiset editions the archive does not have.

Yle's front page only ever shows the newest edition, but the editions
themselves stay online for years at https://yle.fi/a/<id>, in the same markup
the daily capture sees. What is missing is an index: Yle publishes no archive
listing, and its own search lives under /haku/, which robots.txt disallows.
The Wayback Machine supplies one. It has recorded, over the years:

  slug   article URLs of the 2016-2023 site, whose slugs spell out the date:
         /uutiset/osasto/selkouutiset/keskiviikko_10102018_radio/10448913
  feed   snapshots of the RSS feeds, each listing a few weeks of editions
  front  snapshots of the front page, each naming that day's edition

Only the *index* comes from the Wayback Machine. Every page that lands in the
archive is fetched fresh from yle.fi and checked: its datePublished must match
the day it was found for.

  uv run tools/history.py collect --archive . --since 2016-09-01 --out work/
  uv run tools/history.py fetch --out work/
  go run ./scripts backfill $(cat work/backfill-order.txt)

Both steps cache what they download under --out and are safe to re-run: an
interrupted fetch resumes where it stopped. The tool never touches the archive
itself; `archive backfill` does the filing, under the archive's own rules.
"""

from __future__ import annotations

import argparse
import datetime as dt
import html
import json
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

USER_AGENT = "selkouutiset-scrape history backfill (+https://github.com/hiAndrewQuinn/selkouutiset-scrape)"

CDX = "https://web.archive.org/cdx/search/cdx"
WAYBACK_RAW = "https://web.archive.org/web/{ts}id_/{url}"
YLE_ARTICLE = "https://yle.fi/a/{id}"

# Seconds between requests. Wayback asks for restraint; yle.fi gets the same.
POLITE_DELAY = 1.0

# Matches archive.go: below this a page is an error page, not an edition.
MIN_PAGE_BYTES = 20000

# Preference when one day has several editions. Radio ran seven days a week
# for the whole 2016-2023 period and is what the archive's first captures
# (October 2023) hold, so it gives the most uniform series. "plain" is the
# old text-only web edition, which yle.fi mostly no longer serves.
KIND_PREFERENCE = {"radio": 0, "tv": 1, "web": 2, "plain": 3}

WEEKDAYS = {
    "maanantai": 0, "tiistai": 1, "keskiviikko": 2, "torstai": 3,
    "perjantai": 4, "lauantai": 5, "sunnuntai": 6,
}

SLUG_RE = re.compile(
    r"/selkouutiset/(?P<wd>" + "|".join(WEEKDAYS) + r")_(?P<digits>[0-9.]{6,10})(?:_(?P<kind>radio|tv))?/(?P<id>\d{6,9})(?:[/?#]|$)"
)
PUBLISHED_RE = re.compile(rb'"datePublished":\s*"(\d{4}-\d{2}-\d{2})')
JSONLD_ID_RE = re.compile(rb'"@id":\s*"(?:https://yle\.fi/a/)?((?:74|3)-\d{6,9})"')
TITLE_DATE_RE = re.compile(r"(\d{1,2})\.(\d{1,2})\.(\d{4})(?!\d)")
LINK_ID_RE = re.compile(r"(?:/a/|/uutiset/)((?:74|3)-\d{6,9})|/selkouutiset/[^/]+/(\d{6,9})(?:[/?#]|$)")


# --- network ------------------------------------------------------------------

_last_request = 0.0


def get(url: str, *, attempts: int = 4, timeout: int = 120) -> tuple[int, bytes]:
    """GET with a polite delay and retries on transient failures.

    Returns (status, body). A 404 or 410 is an answer, not an error, and is
    returned without retrying.
    """
    global _last_request
    for attempt in range(1, attempts + 1):
        wait = _last_request + POLITE_DELAY - time.monotonic()
        if wait > 0:
            time.sleep(wait)
        _last_request = time.monotonic()
        req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as e:
            if e.code in (404, 410):
                return e.code, b""
            err = f"HTTP {e.code}"
        except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
            err = str(e)
        if attempt < attempts:
            backoff = 15 * attempt
            print(f"  retry {attempt}/{attempts - 1} in {backoff}s: {err} ({url})", file=sys.stderr)
            time.sleep(backoff)
    raise RuntimeError(f"giving up on {url}: {err}")


def cached(cache: Path, name: str, url: str) -> bytes | None:
    """Fetch url once and keep it under cache/name. None for a 404."""
    path = cache / name
    if path.exists():
        return path.read_bytes()
    status, body = get(url)
    if status != 200:
        return None
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(body)
    return body


def cdx(cache: Path, name: str, **params: str) -> list[list[str]]:
    params.setdefault("output", "txt")
    body = cached(cache, f"cdx/{name}.txt", CDX + "?" + urllib.parse.urlencode(params))
    return [line.split() for line in (body or b"").decode().splitlines() if line.strip()]


# --- parsing ------------------------------------------------------------------


def slug_dates(weekday: str, digits: str) -> list[dt.date]:
    """Every date an old-site slug can spell that falls on its weekday.

    Slugs drop the dots: keskiviikko_1112017 could be 1.11. or 11.1. The
    weekday in the slug settles most of these, but not all -- both 1 Nov and
    11 Jan 2017 were Wednesdays. resolve_by_id settles the rest.
    """
    if "." in digits:
        parts = digits.split(".")
        readings = [(parts[0], parts[1], parts[2])] if len(parts) == 3 else []
    else:
        year, rest = digits[-4:], digits[:-4]
        readings = [(rest[:i], rest[i:], year) for i in range(1, len(rest))]
    found = set()
    for d, m, y in readings:
        if not (1 <= len(d) <= 2 and 1 <= len(m) <= 2):
            continue
        try:
            day = dt.date(int(y), int(m), int(d))
        except ValueError:
            continue
        if day.weekday() == WEEKDAYS[weekday]:
            found.add(day)
    return sorted(found)


def resolve_by_id(article_id: int, readings: list[dt.date], known: list[tuple[int, dt.date]]) -> dt.date | None:
    """Pick the reading that fits between the dates of neighbouring article IDs.

    Yle assigns article IDs in increasing order, so an edition's date must lie
    between those of the nearest unambiguous IDs below and above it (give or
    take a couple of days: an ID is assigned when the draft is created).
    known is sorted by ID.
    """
    import bisect

    i = bisect.bisect_left(known, (article_id, dt.date.min))
    lo = known[i - 1][1] if i > 0 else dt.date.min
    hi = known[i][1] if i < len(known) else dt.date.max
    slack = dt.timedelta(days=3)
    fits = [r for r in readings if lo - slack <= r <= hi + slack]
    return fits[0] if len(fits) == 1 else None


def kind_of(title: str) -> str:
    t = title.lower()
    if "radio" in t:
        return "radio"
    if "tv" in t.split() or "(tv)" in t or t.startswith("tv"):
        return "tv"
    return "web"


def link_id(link: str) -> str | None:
    m = LINK_ID_RE.search(link)
    if not m:
        return None
    return m[1] or f"3-{m[2]}"


def feed_items(body: bytes) -> list[tuple[dt.date, str, str]]:
    """(date, kind, article id) for each edition item in an RSS snapshot."""
    out = []
    text = body.decode("utf-8", errors="replace")
    for item in re.findall(r"<item>(.*?)</item>", text, re.S):
        title = re.search(r"<title>(.*?)</title>", item, re.S)
        link = re.search(r"<link>(.*?)</link>", item, re.S)
        if not title or not link:
            continue
        title_text = html.unescape(re.sub(r"<!\[CDATA\[|\]\]>", "", title[1])).strip()
        m = TITLE_DATE_RE.search(title_text)
        aid = link_id(html.unescape(link[1]))
        if not m or not aid:
            continue
        try:
            day = dt.date(int(m[3]), int(m[2]), int(m[1]))
        except ValueError:
            continue
        out.append((day, kind_of(title_text), aid))
    return out


# --- collect ------------------------------------------------------------------


def archive_needs(root: Path, since: dt.date) -> set[dt.date]:
    """Days the archive lacks an edition of its own for.

    Every day from `since` up to the archive's first day, plus each archived
    day whose file holds an older edition. Many of the latter are holidays
    when Yle published nothing; the rest are editions that were lost.
    """
    files = sorted(root.glob("[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/selkouutiset_*.html"))
    need: set[dt.date] = set()
    if not files:
        raise SystemExit(f"no archive under {root}")
    first = dt.date(*map(int, files[0].parts[-4:-1]))
    d = since
    while d < first:
        need.add(d)
        d += dt.timedelta(days=1)
    for f in files:
        day = dt.date(*map(int, f.parts[-4:-1]))
        m = PUBLISHED_RE.search(f.read_bytes())
        if not m or dt.date.fromisoformat(m[1].decode()) != day:
            need.add(day)
    return need


def collect(args: argparse.Namespace) -> None:
    out: Path = args.out
    cache = out / "cache"
    since = dt.date.fromisoformat(args.since)
    need = archive_needs(args.archive, since)
    print(f"{len(need)} day(s) need an edition ({min(need)} .. {max(need)})")

    found: dict[dt.date, dict[str, tuple[str, str]]] = {}  # day -> id -> (kind, source)

    def add(day: dt.date, kind: str, aid: str, source: str) -> None:
        if day in need:
            found.setdefault(day, {}).setdefault(aid, (kind, source))

    # 1. Slugs of the 2016-2023 site.
    rows = cdx(cache, "osasto-prefix", url="yle.fi/uutiset/osasto/selkouutiset/", matchType="prefix",
               fl="original", filter="statuscode:200", collapse="urlkey", limit="200000")
    parsed, ambiguous = [], 0
    for (url,) in rows:
        m = SLUG_RE.search(url)
        if m:
            parsed.append((int(m["id"]), slug_dates(m["wd"], m["digits"]), m["kind"] or "plain"))
    known = sorted({(aid, r[0]) for aid, r, _ in parsed if len(r) == 1})
    for aid, readings, kind in parsed:
        day = readings[0] if len(readings) == 1 else resolve_by_id(aid, readings, known) if readings else None
        if len(readings) > 1:
            ambiguous += 1
        if day:
            add(day, kind, f"3-{aid}", "slug")
    print(f"slug:  {len(parsed)} URL(s), {ambiguous} ambiguous by weekday; {len(found)} day(s) covered")

    # 2. RSS snapshots, old feed and new.
    for name, feed_url in [
        ("feed-old", "feeds.yle.fi/uutiset/v1/recent.rss?publisherIds=YLE_SELKOUUTISET"),
        ("feed-new", "yle.fi/rss/selkouutiset"),
    ]:
        snaps = cdx(cache, name, url=feed_url, fl="timestamp,original", filter="statuscode:200", collapse="timestamp:8")
        for ts, original in snaps:
            body = cached(cache, f"{name}/{ts}.xml", WAYBACK_RAW.format(ts=ts, url=original))
            for day, kind, aid in feed_items(body or b""):
                add(day, kind, aid, name)
        print(f"{name}: {len(snaps)} snapshot(s); {len(found)} day(s) covered")

    # 3. Front-page snapshots, only for days still uncovered. A snapshot names
    #    the edition it shows by datePublished, so one from the small hours of
    #    the next day still counts for the day before.
    snaps = cdx(cache, "front", url="yle.fi/selkouutiset", fl="timestamp,original",
                filter="statuscode:200", collapse="timestamp:10")
    by_day: dict[dt.date, list[tuple[str, str]]] = {}
    for ts, original in snaps:
        by_day.setdefault(dt.date(int(ts[:4]), int(ts[4:6]), int(ts[6:8])), []).append((ts, original))
    uncovered = sorted(d for d in need if d not in found)
    fetched = 0
    for d in uncovered:
        if d in found:
            continue
        for snap_day in (d, d + dt.timedelta(days=1)):
            for ts, original in by_day.get(snap_day, [])[-2:]:
                body = cached(cache, f"front/{ts}.html", WAYBACK_RAW.format(ts=ts, url=original)) or b""
                fetched += 1
                pub, aid = PUBLISHED_RE.search(body), JSONLD_ID_RE.search(body)
                if pub and aid:
                    add(dt.date.fromisoformat(pub[1].decode()), "web", aid[1].decode(), "front")
            if d in found:
                break
    print(f"front: {fetched} snapshot(s) read; {len(found)} day(s) covered")

    lines = []
    for day in sorted(found):
        for aid, (kind, source) in sorted(found[day].items(), key=lambda kv: KIND_PREFERENCE[kv[1][0]]):
            lines.append(f"{day}\t{kind}\t{aid}\t{source}\n")
    (out / "candidates.tsv").write_text("".join(lines))
    missing = sorted(d for d in need if d not in found)
    (out / "uncovered.txt").write_text("".join(f"{d}\t{d:%a}\n" for d in missing))
    print(f"\n{len(found)} of {len(need)} day(s) have a candidate -> {out / 'candidates.tsv'}")
    print(f"{len(missing)} uncovered -> {out / 'uncovered.txt'}")


# --- fetch --------------------------------------------------------------------


def fetch(args: argparse.Namespace) -> None:
    out: Path = args.out
    pages = out / "pages"
    pages.mkdir(parents=True, exist_ok=True)
    by_day: dict[str, list[tuple[str, str]]] = {}
    for line in (out / "candidates.tsv").read_text().splitlines():
        day, kind, aid, _source = line.split("\t")
        by_day.setdefault(day, []).append((kind, aid))

    log = (out / "fetch-log.tsv").open("a")
    order, failed = [], []
    for n, day in enumerate(sorted(by_day), 1):
        chosen = None
        for kind, aid in by_day[day]:
            path = pages / f"{day}.{kind}.{aid}.html"
            if path.exists():
                chosen = path
                break
            status, body = get(YLE_ARTICLE.format(id=aid))
            pub = PUBLISHED_RE.search(body)
            pub_day = pub[1].decode() if pub else "-"
            ok = status == 200 and len(body) >= MIN_PAGE_BYTES and pub_day == day
            log.write(f"{day}\t{kind}\t{aid}\t{status}\t{len(body)}\t{pub_day}\t{'ok' if ok else 'rejected'}\n")
            log.flush()
            if ok:
                path.write_bytes(body)
                chosen = path
                break
        if chosen:
            order.append(str(chosen.resolve()))
        else:
            failed.append(day)
        if n % 50 == 0:
            print(f"{n}/{len(by_day)} days, {len(failed)} without a usable page")
    (out / "backfill-order.txt").write_text("".join(p + "\n" for p in order))
    (out / "fetch-failed.txt").write_text("".join(d + "\n" for d in failed))
    print(f"\n{len(order)} page(s) ready -> {out / 'backfill-order.txt'}")
    print(f"{len(failed)} day(s) had no usable page -> {out / 'fetch-failed.txt'}")


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("collect", help="build candidates.tsv from the Wayback Machine")
    c.add_argument("--archive", type=Path, default=Path("."), help="archive root (default: .)")
    c.add_argument("--since", default="2016-09-01", help="earliest day wanted (default: 2016-09-01)")
    c.add_argument("--out", type=Path, required=True, help="work directory for caches and results")
    f = sub.add_parser("fetch", help="fetch the preferred candidate for each day from yle.fi")
    f.add_argument("--out", type=Path, required=True, help="the work directory collect wrote to")
    args = p.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    {"collect": collect, "fetch": fetch}[args.cmd](args)


if __name__ == "__main__":
    main()
