# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Recover the 2006-2009 radio editions from Yle's old podcast feed.

From 2006 to early 2009 Yle published Selkouutiset as a podcast, and the RSS
feed's <description> carried the full text of each day's broadcast. The feed
lived at two addresses in turn (Radio Peili, then Yle Puhe) and only ever
listed the last few weeks, but the Wayback Machine kept snapshots of it.
Together they hold the text of about 630 broadcasts, April 2006 to February
2009. Yle's site no longer has any of them.

This tool reads every snapshot and files each day's <item> verbatim, one file
per broadcast:

  podcast-feed/YYYY-MM-DD.xml

prefixed with a comment naming the snapshot it came from. Nothing is
rewritten; selkouutiset-scrape-cleaned turns these into pages.

  uv run tools/podcast_feed.py --out podcast-feed/ --cache work/feed-cache/

Idempotent: snapshots are cached, and a day's file is only rewritten if the
chosen item changes. When snapshots disagree about a day (re-encoded
whitespace, or a short headline list later replaced by the full text) the
longest description wins, then the latest snapshot.
"""

from __future__ import annotations

import argparse
import datetime as dt
import html
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
POLITE_DELAY = 1.0

# The daily feed, at both of its addresses. (The weekly review,
# selkouutisten_viikkokatsaus.xml, is a different programme.)
FEEDS = [
    "www.yle.fi/radiopeili/podcast/selkouutiset.xml",
    "www.yle.fi/puhe/podcast/selkouutiset.xml",
]

# "Selkouutiset 30.4.2008": the broadcast date. pubDate is the next morning,
# when the podcast went up, so it is not used.
TITLE_RE = re.compile(r"^\s*Selkouutiset\s+(\d{1,2})\.(\d{1,2})\.(\d{4})\s*$")

_last = 0.0


def get(url: str, attempts: int = 4) -> bytes:
    global _last
    err = ""
    for attempt in range(1, attempts + 1):
        wait = _last + POLITE_DELAY - time.monotonic()
        if wait > 0:
            time.sleep(wait)
        _last = time.monotonic()
        try:
            req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
            with urllib.request.urlopen(req, timeout=120) as r:
                return r.read()
        except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
            err = str(e)
        if attempt < attempts:
            print(f"  retry {attempt}: {err} ({url})", file=sys.stderr)
            time.sleep(15 * attempt)
    raise RuntimeError(f"giving up on {url}: {err}")


def snapshots(feed: str) -> list[tuple[str, str]]:
    q = urllib.parse.urlencode({"url": feed, "fl": "timestamp,original", "filter": "statuscode:200",
                                "collapse": "digest", "output": "txt"})
    return [tuple(line.split()) for line in get(f"{CDX}?{q}").decode().splitlines() if line.strip()]


def items(body: bytes) -> list[tuple[dt.date, str, str]]:
    """(broadcast date, raw <item> XML, description text) for each daily item."""
    text = body.decode("utf-8", errors="replace")
    out = []
    for raw in re.findall(r"<item>.*?</item>", text, re.S):
        title = re.search(r"<title>(.*?)</title>", raw, re.S)
        m = TITLE_RE.match(html.unescape(title[1])) if title else None
        if not m:
            continue
        try:
            day = dt.date(int(m[3]), int(m[2]), int(m[1]))
        except ValueError:
            continue
        desc = re.search(r"<description>(.*?)</description>", raw, re.S)
        out.append((day, raw, html.unescape(desc[1]).strip() if desc else ""))
    return out


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--out", type=Path, required=True, help="where the per-day item files go")
    p.add_argument("--cache", type=Path, required=True, help="snapshot cache (not committed)")
    args = p.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    args.cache.mkdir(parents=True, exist_ok=True)

    best: dict[dt.date, tuple[int, str, str, str]] = {}  # day -> (words, ts, source url, raw item)
    for feed in FEEDS:
        snaps = snapshots(feed)
        print(f"{feed}: {len(snaps)} distinct snapshot(s)")
        for ts, original in snaps:
            path = args.cache / f"{ts}_{Path(feed).parent.name}.xml"
            if not path.exists():
                path.write_bytes(get(WAYBACK_RAW.format(ts=ts, url=original)))
            source = f"https://web.archive.org/web/{ts}/{original}"
            for day, raw, desc in items(path.read_bytes()):
                key = (len(desc.split()), ts)
                if day not in best or key > best[day][:2]:
                    best[day] = (key[0], ts, source, raw)

    written = 0
    for day, (_words, _ts, source, raw) in sorted(best.items()):
        content = f"<!-- {source} -->\n{raw}\n"
        path = args.out / f"{day}.xml"
        if not path.exists() or path.read_text(encoding="utf-8") != content:
            path.write_text(content, encoding="utf-8")
            written += 1
    years = {}
    for day in best:
        years[day.year] = years.get(day.year, 0) + 1
    print(f"{len(best)} broadcast(s), {min(best)} .. {max(best)}; {written} file(s) written")
    print("per year:", ", ".join(f"{y}: {n}" for y, n in sorted(years.items())))


if __name__ == "__main__":
    main()
