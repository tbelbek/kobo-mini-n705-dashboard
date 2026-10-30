#!/usr/bin/env python3
"""Check every passage in kobo/quotes.tsv against its Gutenberg source text.

quotes.tsv columns: tr, author, work, en, gutenberg_id
Each `en` must appear verbatim (whitespace-normalised) in texts/<gutenberg_id>.txt,
and no book may be used twice. Fewer than 366 passages is allowed (some then
repeat within a year).

Usage: verify_quotes.py kobo/quotes.tsv <dir with texts/>
"""
import re
import sys

tsv, root = sys.argv[1], sys.argv[2]
norm = lambda s: re.sub(r"\s+", " ", s).strip()
cache, bad, books = {}, 0, set()
for n, line in enumerate(open(tsv, encoding="utf-8"), 1):
    f = line.rstrip("\n").split("\t")
    if n == 1:
        continue
    if len(f) != 5 or not all(f):
        print(f"line {n}: expected 5 non-empty columns, got {len(f)}"); bad += 1; continue
    tr, author, work, en, gid = f
    if gid not in cache:
        try:
            cache[gid] = norm(open(f"{root}/texts/{gid}.txt", encoding="utf-8").read())
        except OSError:
            print(f"line {n}: no text for book {gid}"); bad += 1; continue
    if norm(en) not in cache[gid]:
        print(f"line {n}: not found verbatim in {gid} ({work}): {en[:70]}"); bad += 1
    if gid in books:
        print(f"line {n}: book {gid} used twice"); bad += 1
    books.add(gid)
if len(books) < 366:
    print(f"note: {len(books)} passages, so some repeat within a year (quality over count)")
print(f"{len(books)} passages from {len(books)} books, {bad} problem(s)")
sys.exit(1 if bad else 0)
