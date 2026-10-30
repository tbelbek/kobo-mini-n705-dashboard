#!/usr/bin/env python3
"""Check every passage in kobo/quotes.tsv against its Gutenberg source text.

quotes.tsv columns: tr, author, work, en, gutenberg_id
Each `en` must appear verbatim (whitespace-normalised; "\\n" marks a verse line break)
in texts/<gutenberg_id>.txt (or yunus/<name>.txt for ids starting "ws:"), a book may be
used at most 3 times (poetry collections), and no passage twice. Fewer than 366 passages is allowed (some then
repeat within a year).

Usage: verify_quotes.py kobo/quotes.tsv <dir with texts/>
"""
import re
import sys

tsv, root = sys.argv[1], sys.argv[2]
norm = lambda s: re.sub(r"\s+", " ", s).strip()
cache, bad, books, used = {}, 0, {}, set()
for n, line in enumerate(open(tsv, encoding="utf-8"), 1):
    f = line.rstrip("\n").split("\t")
    if n == 1:
        continue
    if len(f) != 5 or not all(f):
        print(f"line {n}: expected 5 non-empty columns, got {len(f)}"); bad += 1; continue
    tr, author, work, en, gid = f
    if gid not in cache:
        try:
            src = f"{root}/yunus/{gid[3:]}.txt" if gid.startswith("ws:") else f"{root}/texts/{gid}.txt"
            cache[gid] = norm(open(src, encoding="utf-8").read())
        except OSError:
            print(f"line {n}: no text for book {gid}"); bad += 1; continue
    if norm(en.replace("\\n", " ")) not in cache[gid]:
        print(f"line {n}: not found verbatim in {gid} ({work}): {en[:70]}"); bad += 1
    books[gid] = books.get(gid, 0) + 1
    if books[gid] > 3:
        print(f"line {n}: book {gid} used more than 3 times"); bad += 1
    if en in used:
        print(f"line {n}: duplicate passage"); bad += 1
    used.add(en)
if len(used) < 366:
    print(f"note: {len(used)} passages, so some repeat within a year (quality over count)")
print(f"{len(used)} passages from {len(books)} sources, {bad} problem(s)")
sys.exit(1 if bad else 0)
