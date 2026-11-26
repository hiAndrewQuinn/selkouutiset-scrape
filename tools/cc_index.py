# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""List old-site Selkouutiset editions that Common Crawl indexed, for given days.

The 2016-2022 site put the date in each edition's URL:
  yle.fi/uutiset/osasto/selkouutiset/keskiviikko_11102017_radio/9878058
Common Crawl's public URL index (index.commoncrawl.org) records which of these
it crawled, independently of the Wayback Machine. This queries every crawl from
2016 to 2024, decodes the slugs (the weekday settles which day/month split is
meant) and prints the candidate IDs for the wanted days, ready for fetch_ids.py.

  uv run tools/cc_index.py /tmp/cc <(grep -v '^#' lost.txt | cut -f1) > /tmp/cc/candidates.tsv

usage: cc_index.py CACHE_DIR DAYS_FILE
Index answers are cached per crawl in CACHE_DIR; failed queries (the index
server times out often) are retried and can be re-run. Output lines: day,
kind (radio/tv/plain), id.
"""
import datetime as dt, json, pathlib, re, sys, time, urllib.error, urllib.parse, urllib.request

UA = "selkouutiset-scrape history backfill (+https://github.com/hiAndrewQuinn/selkouutiset-scrape)"
FIRST, LAST = "CC-MAIN-2016-36", "CC-MAIN-2024-10"
PATTERN = "yle.fi/uutiset/osasto/selkouutiset/*"
WD = {"maanantai": 0, "tiistai": 1, "keskiviikko": 2, "torstai": 3, "perjantai": 4, "lauantai": 5, "sunnuntai": 6}
SLUG = re.compile(r"/selkouutiset/(" + "|".join(WD) + r")_([0-9.]{6,10})(?:_(radio|tv))?/(\d{6,9})")


def get(url):
    for attempt in range(6):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": UA}), timeout=180) as r:
                return r.read()
        except urllib.error.HTTPError as e:
            if e.code == 404:
                return b""
        except Exception:
            pass
        time.sleep(15 * (attempt + 1))
    return None


def slug_days(weekday, digits):
    if "." in digits:
        parts = digits.split(".")
        readings = [tuple(parts)] if len(parts) == 3 else []
    else:
        year, rest = digits[-4:], digits[:-4]
        readings = [(rest[:i], rest[i:], year) for i in range(1, len(rest))]
    for d, m, y in readings:
        try:
            day = dt.date(int(y), int(m), int(d))
        except ValueError:
            continue
        if day.weekday() == WD[weekday]:
            yield day


def main():
    cache = pathlib.Path(sys.argv[1]); cache.mkdir(parents=True, exist_ok=True)
    wanted = {dt.date.fromisoformat(x) for x in pathlib.Path(sys.argv[2]).read_text().split()}
    crawls = [c["id"] for c in json.loads(get("https://index.commoncrawl.org/collinfo.json") or b"[]")
              if FIRST <= c["id"] <= LAST]
    found = {}
    for crawl in crawls:
        path = cache / f"{crawl}.jsonl"
        if not path.exists():
            body = get(f"https://index.commoncrawl.org/{crawl}-index?" + urllib.parse.urlencode({"url": PATTERN, "output": "json"}))
            if body is None:
                print(f"index query failed for {crawl}; re-run to retry", file=sys.stderr)
                continue
            path.write_bytes(body)
            time.sleep(2)
        for line in path.read_text().splitlines():
            try:
                rec = json.loads(line)
            except ValueError:
                continue
            m = SLUG.search(rec.get("url", ""))
            if rec.get("status") != "200" or not m:
                continue
            for day in slug_days(m[1], m[2]):
                if day in wanted:
                    found.setdefault(day, {})[f"3-{m[4]}"] = m[3] or "plain"
    for day in sorted(found):
        for aid, kind in sorted(found[day].items()):
            print(f"{day}\t{kind}\t{aid}")
    print(f"{len(found)} of {len(wanted)} day(s) have a candidate", file=sys.stderr)


if __name__ == "__main__":
    main()
