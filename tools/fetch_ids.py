# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Fetch and verify one page per recovered day.

usage: fetch_ids.py OUT_DIR CANDIDATE_TSV...

Candidates come from probe_ids.py or cc_index.py. Pages already fetched are
kept, so a re-run only asks for what is missing.
Each candidate TSV line: day<TAB>kind<TAB>id[<TAB>...]. Candidates per day are
tried radio, tv, web, plain; a page is kept only if yle.fi serves it with
status 200, >= 20000 bytes, and a datePublished equal to the day.
Writes OUT_DIR/pages/<day>.<kind>.<id>.html, OUT_DIR/order.txt, OUT_DIR/failed.txt,
and logs every attempt to OUT_DIR/log.tsv.
"""
import pathlib, re, sys, time, urllib.error, urllib.request

UA = "selkouutiset-scrape history backfill (+https://github.com/hiAndrewQuinn/selkouutiset-scrape)"
PUB = re.compile(rb'"datePublished":\s*"(\d{4}-\d{2}-\d{2})')
PREF = {"radio": 0, "tv": 1, "web": 2, "plain": 3}

out = pathlib.Path(sys.argv[1]); pages = out / "pages"; pages.mkdir(parents=True, exist_ok=True)
by_day = {}
for f in sys.argv[2:]:
    for line in open(f):
        p = line.rstrip("\n").split("\t")
        if len(p) >= 3 and p[1] != "none":
            by_day.setdefault(p[0], {}).setdefault(p[2], p[1])

log = (out / "log.tsv").open("a")
order, failed, last = [], [], 0.0
for day in sorted(by_day):
    chosen = None
    for aid, kind in sorted(by_day[day].items(), key=lambda kv: PREF.get(kv[1], 9)):
        path = pages / f"{day}.{kind}.{aid}.html"
        if path.exists():
            chosen = path; break
        time.sleep(max(0, last + 1.0 - time.monotonic())); last = time.monotonic()
        status, body = 0, b""
        for attempt in range(4):
            try:
                req = urllib.request.Request(f"https://yle.fi/a/{aid}", headers={"User-Agent": UA})
                with urllib.request.urlopen(req, timeout=90) as r:
                    status, body = r.status, r.read()
                break
            except urllib.error.HTTPError as e:
                status = e.code
                if e.code in (404, 410): break
            except Exception:
                pass
            time.sleep(15 * (attempt + 1))
        m = PUB.search(body)
        pub = m[1].decode() if m else "-"
        ok = status == 200 and len(body) >= 20000 and pub == day
        log.write(f"{day}\t{kind}\t{aid}\t{status}\t{len(body)}\t{pub}\t{'ok' if ok else 'rejected'}\n"); log.flush()
        if ok:
            path.write_bytes(body); chosen = path; break
    (order if chosen else failed).append(str(chosen.resolve()) if chosen else day)
(out / "order.txt").write_text("".join(p + "\n" for p in order))
(out / "failed.txt").write_text("".join(d + "\n" for d in failed))
print(f"{len(order)} page(s) verified, {len(failed)} day(s) without a usable page")
