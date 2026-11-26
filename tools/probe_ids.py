# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Probe yle.fi article IDs for every lost Selkouutiset edition.

Yle assigns article IDs in increasing order, and yle.fi still serves old
editions at https://yle.fi/a/<id> under titles that name their day
("Tiistai 6.6.2017 (radio)", "Radio Selkouutiset keskiviikko 13.9.2023"). So a
lost edition can be found without any index: estimate its ID from the
editions on either side, then read the <title> of the IDs around the estimate,
outward, until one names the day.

  uv run tools/probe_ids.py . /tmp/probe/cache <(grep -v '^#' lost.txt | cut -f1) /tmp/probe/found.tsv
  uv run tools/fetch_ids.py /tmp/probe/fetched /tmp/probe/found.tsv
  go run ./scripts backfill $(cat /tmp/probe/fetched/order.txt)

usage: probe_ids.py ARCHIVE_ROOT CACHE_DIR LOST_LIST OUT_TSV [DELAY]

DELAY is seconds between requests (default 1.0). Titles are cached in
CACHE_DIR, so a re-run never asks yle.fi twice for the same ID, and disjoint
date ranges can be probed in parallel against the same cache. Days that already
have a verified page in a directory named by EXTRA_PAGES (colon-separated,
files named <day>.<kind>.<id>.html as fetch_ids.py writes them) are skipped and
serve as anchors. Output lines: day, kind (radio/tv/web, or none), id, title.
When no title names a day, every edition-shaped title between its anchors is
listed as a candidate marked "(misdated?)": Yle's titles carry typos, and only
the page's datePublished, checked by fetch_ids.py, says which day it belongs to.

Anchors are archive editions whose datePublished equals their day, plus every
edition found while probing (so runs of lost days are walked one day at a
time, each found day anchoring the next). Every probed title that looks like
an edition counts for the day it names, whichever day was being searched.
"""
import bisect, datetime as dt, json, os, pathlib, re, sys, time, urllib.error, urllib.request

UA = "selkouutiset-scrape history backfill (+https://github.com/hiAndrewQuinn/selkouutiset-scrape)"
PUB = re.compile(rb'"datePublished":\s*"(\d{4}-\d{2}-\d{2})')
AID = re.compile(rb'"@id":\s*"(?:https://yle\.fi/a/)?((?:74|3)-\d{6,9})"')
TITLE = re.compile(r"<title[^>]*>([^<]*)</title>")
DATE = re.compile(r"(\d{1,2})\.(\d{1,2})\.(\d{4})(?!\d)")
WD = r"(maanantai|tiistai|keskiviikko|torstai|perjantai|lauantai|sunnuntai)"
OLD_TITLE = re.compile(r"^" + WD + r"\s+\d{1,2}\.\d{1,2}\.\d{4}", re.I)
WINDOW = {"3": 1300, "74": 450}

root, cache_dir, lost_list, out_tsv = map(pathlib.Path, sys.argv[1:5])
delay = float(sys.argv[5]) if len(sys.argv) > 5 else 1.0
lost = sorted(dt.date.fromisoformat(l.strip()) for l in lost_list.read_text().split() if l.strip())
lost_set = set(lost)
cache_dir.mkdir(parents=True, exist_ok=True)
cache_file = cache_dir / "titles.jsonl"
titles = {}
if cache_file.exists():
    for line in cache_file.read_text().splitlines():
        r = json.loads(line); titles[r["id"]] = r["title"]
cache = cache_file.open("a")


def kind_of(t):
    t = t.lower()
    return "radio" if "radio" in t else "tv" if "tv" in t else "web"


def edition_day(t):
    """The day an edition title names, or None if it is not an edition title."""
    if t.startswith("#"):
        return None
    m = DATE.search(t)
    if not m or not ("selkouutiset" in t.lower() or OLD_TITLE.match(t)):
        return None
    try:
        return dt.date(int(m[3]), int(m[2]), int(m[1]))
    except ValueError:
        return None


# Yle's titles are hand-typed and sometimes wrong: "Perjantai 19.1. 2018",
# "Tiistai 21.2.2020" for 21.1., "Sunnuntai 21.4.2109". Any edition-shaped
# title between a lost day's anchors is therefore also a candidate for that
# day, whatever date it names; fetch_ids.py's datePublished check decides.
SHAPE = re.compile(r"^" + WD + r"\b.*\((radio|tv)\)|selkouutiset", re.I)


def misdated(day, pre, lo, hi):
    """Edition-shaped titles in (lo, hi) that do not name day."""
    out = []
    for aid, t in titles.items():
        p, _, n = aid.partition("-")
        if p == pre and lo < int(n) < hi and not t.startswith("#") and SHAPE.search(t) and edition_day(t) != day:
            out.append((aid, t))
    return sorted(out)


found = {}  # day -> {id: title}
def note(aid, t):
    d = edition_day(t)
    if d in lost_set:
        found.setdefault(d, {})[aid] = t
for aid, t in titles.items():
    note(aid, t)

anchors = {}  # (prefix) -> sorted list of (date, num)
def add_anchor(d, aid):
    pre, num = aid.split("-")
    lst = anchors.setdefault(pre, [])
    bisect.insort(lst, (d, int(num)))
extra = [pathlib.Path(x) for x in os.environ.get("EXTRA_PAGES", "").split(":") if x]
skip = set()
for f in sorted(root.glob("[0-9]" * 4 + "/[0-9][0-9]/[0-9][0-9]/selkouutiset_*.html")) + [g for e in extra for g in sorted(e.glob("*.html"))]:
    day = dt.date(*map(int, f.parts[-4:-1])) if f.parent.parent.parent.name.isdigit() else dt.date.fromisoformat(f.name[:10])
    if f.parent in extra: skip.add(day)
    body = f.read_bytes()
    p, a = PUB.search(body), AID.search(body)
    if p and a and p[1].decode() == str(day):
        add_anchor(day, a[1].decode())
for d, ids in found.items():
    for aid in ids:
        add_anchor(d, aid)

last = 0.0
def title_of(aid):
    global last
    if aid in titles:
        return titles[aid], 0
    wait = last + delay - time.monotonic()
    if wait > 0:
        time.sleep(wait)
    last = time.monotonic()
    for attempt in range(5):
        try:
            req = urllib.request.Request(f"https://yle.fi/a/{aid}", headers={"User-Agent": UA})
            with urllib.request.urlopen(req, timeout=60) as r:
                head = r.read(80000).decode("utf-8", "replace")
            m = TITLE.search(head)
            t = m[1].strip() if m else "?"
            break
        except urllib.error.HTTPError as e:
            if e.code in (404, 410):
                t = f"#{e.code}"; break
            time.sleep(20 * (attempt + 1))
        except Exception:
            time.sleep(20 * (attempt + 1))
    else:
        return "#error", 1
    titles[aid] = t
    cache.write(json.dumps({"id": aid, "title": t}, ensure_ascii=False) + "\n"); cache.flush()
    note(aid, t)
    return t, 1


out = out_tsv.open("a")
for day in lost:
    if day in skip:
        print(f"{day}\tverified elsewhere", flush=True); continue
    # the ID scheme of the nearest anchor before the day
    def latest_before(p):
        j = bisect.bisect_left(anchors[p], (day, 0))
        return anchors[p][j - 1][0] if j > 0 else dt.date.min
    pre = max(anchors, key=latest_before)
    lst = anchors[pre]
    i = bisect.bisect_left(lst, (day, 0))
    if i == 0 or i == len(lst):
        print(f"{day}\tskip\tno anchors on both sides", flush=True); continue
    (d0, lo), (d1, hi) = lst[i - 1], lst[i]
    if day in found:
        status, reqs = "cached", 0
    else:
        est = round(lo + (day - d0).days / (d1 - d0).days * (hi - lo))
        reqs, k, hit_k = 0, 0, None
        while k <= WINDOW[pre]:
            for n in ([est] if k == 0 else [est + k, est - k]):
                if lo < n < hi:
                    _, r = title_of(f"{pre}-{n}"); reqs += r
            if day in found and hit_k is None:
                hit_k = k
            if hit_k is not None and k >= hit_k + 8:  # catch radio/tv siblings
                break
            if est + k >= hi and est - k <= lo:
                break
            k += 1
        status = "found" if day in found else "none"
        for aid in found.get(day, {}):
            add_anchor(day, aid)
    for aid, t in found.get(day, {}).items():
        out.write(f"{day}\t{kind_of(t)}\t{aid}\t{t}\n")
    if day not in found:
        extra = misdated(day, pre, lo, hi)
        for aid, t in extra:
            out.write(f"{day}\t{kind_of(t)}\t{aid}\t{t} (misdated?)\n")
        if extra:
            status = f"{len(extra)} misdated candidate(s)"
        else:
            out.write(f"{day}\tnone\t-\t-\n")
    out.flush()
    print(f"{day}\t{status}\t{reqs} req\t{len(found)} days found so far", flush=True)
