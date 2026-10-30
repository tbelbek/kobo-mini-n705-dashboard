package main

// One uplifting passage a day, each from a different public-domain novel.
// quotes.tsv: Turkish translation, author, work, original English sentence,
// Project Gutenberg book number (the last two let verify_quotes.py check every quote).

import (
	_ "embed"
	"math/rand"
	"strings"
	"time"
)

//go:embed quotes.tsv
var quotesTSV string

type Quote struct{ Text, Author, Work string }

var quotes = func() []Quote {
	var out []Quote
	for _, l := range strings.Split(quotesTSV, "\n") {
		f := strings.Split(l, "\t")
		if len(f) >= 3 && f[0] != "tr" {
			out = append(out, Quote{strings.ReplaceAll(f[0], `\n`, "\n"), f[1], f[2]}) // `\n` = verse line break
		}
	}
	return out
}()

// quoteFor: random but stable for the whole day. Each year gets its own shuffle,
// so with more than 366 passages none repeats within a year.
func quoteFor(t time.Time) (Quote, bool) {
	if len(quotes) == 0 {
		return Quote{}, false
	}
	order := rand.New(rand.NewSource(int64(t.Year()))).Perm(len(quotes))
	return quotes[order[(t.YearDay()-1)%len(quotes)]], true
}
