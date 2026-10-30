#!/usr/bin/env python3
"""Build kobodash's daily-passage candidates from public-domain novels.

Input : a Gutendex JSON dump (popular English fiction, with download counts).
Output: candidates.tsv  book_id, author, title, sentence   (up to 10 per book)
        texts/<id>.txt  the plain text, Project Gutenberg header/footer stripped

Only authors who died before 1955 are used, so the works are public domain in the
US, EU and Turkey (life + 70). A human/LLM pass then picks one joyful sentence per
book and translates it; verify_quotes.py checks every pick against texts/.

Usage: quote_candidates.py gutendex.json outdir [max_books]
"""
import json
import os
import re
import sys
import urllib.request

POS = set("""happy happiness happier joy joyful joyous glad gladness delight delighted
delightful smile smiled smiling laugh laughed laughing laughter hope hopes hopeful
sunshine sunny bright brightly beautiful beauty wonderful lovely love loved kind
kindness cheer cheerful cheery merry bless blessed blessing grateful gratitude
peace peaceful warm warmth courage dream dreams tomorrow morning spring blossom
blossoms sing singing song gay gaiety sweet friend friends friendship garden
flowers glorious splendid rejoice rejoiced content contentment hearty heart""".split())
NEG = set("""death die died dying dead kill killed murder cry cried crying tears
weep wept sad sadness sorrow sorrowful grief pain hate hated fear afraid war blood
poor sick misery miserable terrible cruel wicked alas despair devil hell damn
grave coffin funeral starve hungry lonely ugly fool stupid wretched horror
never""".split())
SENT = re.compile(r'(?<=[.!?])["”’]?\s+(?=["“‘]?[A-Z])')


def strip_gutenberg(t):
    s = re.search(r"\*\*\* ?START OF (THE|THIS) PROJECT GUTENBERG.*?\*\*\*", t)
    e = re.search(r"\*\*\* ?END OF (THE|THIS) PROJECT GUTENBERG", t)
    return t[s.end() if s else 0: e.start() if e else len(t)]


def candidates(text):
    flat = re.sub(r"\s+", " ", text)
    out = []
    for s in SENT.split(flat):
        s = s.strip()
        if not 60 <= len(s) <= 200 or s.isupper() or "CHAPTER" in s or "_" in s:
            continue
        words = re.findall(r"[a-z']+", s.lower())
        if any(w in NEG for w in words):
            continue
        score = sum(w in POS for w in words)
        if score >= 2:
            out.append((score, s))
    out.sort(key=lambda x: -x[0])
    seen, best = set(), []
    for _, s in out:
        if s not in seen:
            seen.add(s)
            best.append(s)
        if len(best) == 10:
            break
    return best


def main():
    src, outdir = sys.argv[1], sys.argv[2]
    max_books = int(sys.argv[3]) if len(sys.argv) > 3 else 420
    os.makedirs(os.path.join(outdir, "texts"), exist_ok=True)
    books = json.load(open(src))
    books.sort(key=lambda b: -b.get("download_count", 0))
    chosen, titles, per_author = [], set(), {}
    for b in books:
        if not b["authors"] or "en" not in b["languages"]:
            continue
        a = b["authors"][0]
        if not a.get("death_year") or a["death_year"] >= 1955:
            continue
        title = re.split(r"[:;(]|, or,? ", b["title"])[0].strip()
        key = re.sub(r"\W+|vol(ume)?\s*\w+", "", title.lower())
        if key in titles or re.search(r"complete|collected|works of|anthology|poems|volume", b["title"], re.I):
            continue
        if per_author.get(a["name"], 0) >= 4:
            continue
        titles.add(key)
        per_author[a["name"]] = per_author.get(a["name"], 0) + 1
        chosen.append((b["id"], a["name"], title))
        if len(chosen) >= max_books:
            break

    rows = 0
    with open(os.path.join(outdir, "candidates.tsv"), "w") as f:
        f.write("book_id\tauthor\ttitle\tsentence\n")
        for bid, author, title in chosen:
            path = os.path.join(outdir, "texts", f"{bid}.txt")
            if not os.path.exists(path):
                try:
                    raw = urllib.request.urlopen(
                        f"https://www.gutenberg.org/cache/epub/{bid}/pg{bid}.txt", timeout=60).read()
                except Exception as e:
                    print("skip", bid, e, file=sys.stderr)
                    continue
                open(path, "w").write(strip_gutenberg(raw.decode("utf-8", "replace")))
            name = re.sub(r"\s*\([^)]*\)", "", author)  # "Miles, Alfred H. (Alfred Henry)" -> "Miles, Alfred H."
            name = " ".join(reversed(name.split(", ", 1))) if ", " in name else name  # "Dumas, Alexandre" -> "Alexandre Dumas"
            for s in candidates(open(path).read()):
                f.write(f"{bid}\t{name}\t{title}\t{s}\n")
                rows += 1
    print(f"{len(chosen)} books, {rows} candidate sentences")


if __name__ == "__main__":
    main()
