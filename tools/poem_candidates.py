#!/usr/bin/env python3
"""Pull 2-4 line extracts out of public-domain poetry books for kobodash.

Picks poetry books from the Project Gutenberg catalog by the poets listed below
(author died before 1955), downloads them, and writes stanza-local windows of
2-4 consecutive lines that contain positive words and none of the negative ones.
Line breaks are kept as a literal "\\n" so the display can show real verse lines.

Output: poem_candidates.tsv  book_id, author, title, lines   (up to 15 per book)
Usage : poem_candidates.py pg_catalog.csv popular_ids.txt outdir
"""
import csv
import os
import re
import sys
import urllib.request

sys.path.insert(0, os.path.dirname(__file__))
from quote_candidates import NEG, POS, strip_gutenberg  # noqa: E402

POETS = """Tagore Gibran Dickinson Whitman Blake Burns Wordsworth Keats Shelley
Longfellow Browning Rossetti Stevenson Tennyson Emerson FitzGerald Kipling Yeats
Teasdale Wilcox Kilmer Dyke Kabir Rumi Hafiz Riley Dunbar Herrick Shakespeare
Bryant Whittier Lowell Hunt Cowper Clare Moore Field Guest""".split()


def all_pd(authors):
    """Every contributor except illustrators must have died before 1955 (life + 70 in TR/EU)."""
    for p in authors.split(";"):
        if not p.strip() or "Illustrator" in p:
            continue
        m = re.search(r"-(\d{1,4})\b", p)  # death year; handles "552 BCE-480 BCE" and "-1916"
        if not m or int(m.group(1)) >= 1955:
            return False
    return True


def windows(text):
    for stanza in re.split(r"\n\s*\n", text):
        lines = [l.strip() for l in stanza.splitlines() if l.strip()]
        if not 2 <= len(lines) <= 40 or any(l.isupper() for l in lines):
            continue
        for size in (2, 3, 4):
            for i in range(0, len(lines) - size + 1):
                part = lines[i:i + size]
                flat = " ".join(part)
                if not 40 <= len(flat) <= 220 or "_" in flat or re.search(r"\d", flat):
                    continue
                words = re.findall(r"[a-z']+", flat.lower())
                if any(w in NEG for w in words):
                    continue
                score = sum(w in POS for w in words)
                if score >= 2:
                    yield score, "\\n".join(part)


def main():
    catalog, popular, outdir = sys.argv[1:4]
    rank = {int(x): i for i, x in enumerate(open(popular).read().split())}
    books, per_poet = [], {}
    for r in csv.DictReader(open(catalog, encoding="utf-8")):
        try:
            bid = int(r["Text#"])
        except ValueError:
            continue
        if r["Language"] != "en" or r["Type"] != "Text":
            continue
        if not re.search(r"poetry|poems|verse|songs", (r["Subjects"] + r["Title"]).lower()):
            continue
        m = re.search(r"^([^;]*?),\s*(\d{3,4})\??-(\d{3,4})", r["Authors"])
        if not m or int(m.group(3)) >= 1955:
            continue
        if not all_pd(r["Authors"]):
            continue
        surname = m.group(1).split(",")[0].strip()
        poet = next((p for p in POETS if p.lower() in surname.lower()), None)
        if poet and not re.search(r"complete works|plays|letters", r["Title"], re.I):
            books.append((rank.get(bid, 10**6), bid, m.group(1), r["Title"].split("\n")[0][:80], poet))
    books.sort()
    chosen = []
    for rk, bid, author, title, poet in books:
        if per_poet.get(poet, 0) < 4:
            per_poet[poet] = per_poet.get(poet, 0) + 1
            chosen.append((bid, author, title))

    os.makedirs(os.path.join(outdir, "texts"), exist_ok=True)
    rows = 0
    with open(os.path.join(outdir, "poem_candidates.tsv"), "w", encoding="utf-8") as f:
        f.write("book_id\tauthor\ttitle\tlines\n")
        for bid, author, title in chosen:
            path = os.path.join(outdir, "texts", f"{bid}.txt")
            if not os.path.exists(path):
                try:
                    raw = urllib.request.urlopen(f"https://www.gutenberg.org/cache/epub/{bid}/pg{bid}.txt", timeout=60).read()
                except Exception as e:
                    print("skip", bid, e, file=sys.stderr)
                    continue
                open(path, "w", encoding="utf-8").write(strip_gutenberg(raw.decode("utf-8", "replace")))
            name = re.sub(r"\s*\([^)]*\)", "", author)
            name = " ".join(reversed(name.split(", ", 1))) if ", " in name else name
            best, seen = [], set()
            for _, w in sorted(windows(open(path, encoding="utf-8").read()), key=lambda x: -x[0]):
                if w not in seen and not any(w in s or s in w for s in seen):
                    seen.add(w)
                    best.append(w)
                if len(best) == 15:
                    break
            for w in best:
                f.write(f"{bid}\t{name}\t{title}\t{w}\n")
                rows += 1
    print(f"{len(chosen)} poetry books, {rows} candidate extracts")


if __name__ == "__main__":
    main()
