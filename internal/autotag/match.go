// Package autotag matches a local title against a provider's search results and
// decides whether the best candidate is safe to auto-apply or should go to review.
//
// The matching problem is cross-language: a local title may be romaji while the
// online entry is english (or vice-versa), and one title can return several
// candidate galleries. So a candidate's title score is the max similarity against
// both the online english and japanese titles. Page count ranks candidates and is one
// of several routes to auto-apply, but it no longer gates on its own (doujin page
// counts drift with covers/decensoring/scan group); see qualifies for the routes.
//
// This package is pure and dependency-free apart from the neutral source result
// type, so the scoring and the auto-vs-review decision are fully unit-testable
// without any network access and work identically for every provider.
package autotag

import (
	"sort"
	"strings"
	"unicode"

	"doujin/internal/source"
)

// Tuning constants for the qualify/merge/decide model. A candidate *qualifies* (is
// safe to auto-apply) via one of the routes in qualifies — a strong-enough title or a
// matching artist, with page count as corroboration rather than a hard gate. Several
// qualifiers are the same work in different variations (group/translation/language), so
// their tags are merged rather than sent to review. Language never gates the decision —
// it only ranks the primary and is preserved on apply.
const (
	pageTolerance        = 2    // |localPages - candPages| within this counts as a page match
	qualifyTitle         = 0.6  // minimum title similarity to be an auto-applicable match
	strongTitleBar       = 0.85 // a near-perfect title alone qualifies (no artist/page corroboration)
	strongTitleMinTokens = 2    // ...but only when the matched title has at least this many tokens
	mergeTitleDelta      = 0.1  // merge only candidates within this of the best qualifying title
	mergeCap             = 4    // max galleries unioned on a multi-match auto-apply
	langBoost            = 0.15 // RANKING ONLY — picks the primary; never gates the decision
	pageBonus            = 0.5  // RANKING ONLY — full when exact, scaled down within tolerance
)

// Candidate is one scored provider search result for a local title. The page and
// language fields feed ranking + display; the auto-vs-review *gate* is a mix of
// TitleScore, PagesClose/PagesExact, and ArtistMatch (see Decide/qualifies), not the
// composite Score. ArtistMatch is supplied by the caller (a catalog hit is the artist's
// by construction); like language it never feeds the ranking Score, only the action.
type Candidate struct {
	Gallery      source.SearchResult
	TitleScore   float64 // max similarity vs the english/japanese online titles, [0,1]
	MatchTokens  int     // token count of the shorter side of the best-scoring title pair
	PageDelta    int     // |localPages - gallery pages|; -1 when either count is unknown
	PagesExact   bool    // page counts match exactly (delta 0, both > 0)
	PagesClose   bool    // page counts within pageTolerance (both > 0)
	Lang         string  // language detected for this candidate ("" if unknown)
	LangMatch    bool    // candidate language equals the local language (both known)
	LangMismatch bool    // candidate language differs from a known local language
	ArtistMatch  bool    // candidate's artist equals the local artist (gates the action, not Score)
	Score        float64 // ranking score (title + page + language); ordering only
}

// Action is the outcome of Decide.
type Action string

const (
	ActionAuto   Action = "auto"   // at least one candidate qualifies; apply the merge set
	ActionReview Action = "review" // nothing close enough; present the shortlist to the user
)

// Decision pairs an action with the candidates sorted best-first and, for an auto
// action, the qualifying variants to merge. Apply[0] is the primary (the gallery to
// link and whose language fills an empty slot). Apply is empty on review.
type Decision struct {
	Action Action
	Ranked []Candidate
	Apply  []Candidate
}

// sameLanguage reports whether two language names match for the purpose of matching.
// "translated" is treated as english-family (most translated doujin are English), so
// a local [Translated] doesn't spuriously mismatch an [English] gallery.
func sameLanguage(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return englishFamily(a) && englishFamily(b)
}

func englishFamily(s string) bool { return s == "english" || s == "translated" }

// Score scores one search result against the local title variants, page count, and
// language. TitleScore is the best similarity across every local variant × every
// candidate title *part*. Both sides are split into dual-language halves first (titles
// are often "romaji | english"), so a clean english local title matches the english
// half of a "romaji | english" online title without the romaji half — or a leading
// event/magazine — diluting the score. The page and language terms feed the ranking
// Score (which picks the primary among equally-good titles); they are not the auto gate.
func Score(titleVariants []string, localPages int, localLang, candLang string, artistMatch bool, c source.SearchResult) Candidate {
	ts := 0.0
	tokens := 0
	candParts := append(titleParts(c.EnglishTitle), titleParts(c.JapaneseTitle)...)
	for _, v := range titleVariants {
		for _, cp := range candParts {
			if s := Similarity(v, cp); s > ts {
				ts = s
				// Specificity of the winning pair: the shorter side's token count, so a
				// one-word generic title can't clear the strong-title-alone bar unaided.
				tokens = min(len(strings.Fields(Normalize(v))), len(strings.Fields(Normalize(cp))))
			}
		}
	}

	cand := Candidate{Gallery: c, TitleScore: ts, MatchTokens: tokens, Lang: candLang, ArtistMatch: artistMatch, PageDelta: -1, Score: ts}

	if localPages > 0 && c.NumPages > 0 {
		delta := localPages - c.NumPages
		if delta < 0 {
			delta = -delta
		}
		cand.PageDelta = delta
		cand.PagesExact = delta == 0
		cand.PagesClose = delta <= pageTolerance
		switch {
		case cand.PagesExact:
			cand.Score += pageBonus
		case cand.PagesClose:
			cand.Score += pageBonus - 0.1*float64(delta) // 0.4 at ±1, 0.3 at ±2
		}
	}

	if localLang != "" && candLang != "" {
		if sameLanguage(localLang, candLang) {
			cand.LangMatch = true
			cand.Score += langBoost
		} else {
			cand.LangMismatch = true
		}
	}
	return cand
}

// ScoreAll scores every result against the local title variants. candLang resolves a
// result's language (the app injects doujin.DetectLanguage over its titles) and
// artistMatch reports whether the result is by the local artist (the app marks catalog
// hits true by construction); a nil resolver means "unknown"/false for every candidate.
func ScoreAll(titleVariants []string, localPages int, localLang string, results []source.SearchResult, candLang func(source.SearchResult) string, artistMatch func(source.SearchResult) bool) []Candidate {
	out := make([]Candidate, 0, len(results))
	for _, r := range results {
		cl := ""
		if candLang != nil {
			cl = candLang(r)
		}
		am := false
		if artistMatch != nil {
			am = artistMatch(r)
		}
		out = append(out, Score(titleVariants, localPages, localLang, cl, am, r))
	}
	return out
}

// qualifies reports whether a candidate is safe to auto-apply. Page count is an
// unreliable gate for doujin (covers, decensored, scan group all shift it), so a strong
// enough signal on *either* the title or the artist carries it — page closeness is only
// one of several routes:
//
//	(a) close pages + a decent title   — the original strong signal
//	(b) the right artist + a decent title (pages need not be close)
//	(c) the right artist + exact pages  (covers a weak title, e.g. romaji-vs-english)
//	(d) a near-perfect, specific title alone (when no artist could be resolved)
//
// Language never participates (it only ranks the primary).
func qualifies(c Candidate) bool {
	switch {
	case c.PagesClose && c.TitleScore >= qualifyTitle:
		return true
	case c.ArtistMatch && c.TitleScore >= qualifyTitle:
		return true
	case c.ArtistMatch && c.PagesExact:
		return true
	case c.TitleScore >= strongTitleBar && c.MatchTokens >= strongTitleMinTokens:
		return true
	}
	return false
}

// Decide ranks candidates best-first and chooses auto vs review. If any candidate
// qualifies (see qualifies) the action is auto, and Apply holds the qualifying variants
// whose title is within mergeTitleDelta of the best one AND whose page count is close to
// the primary's (the same work in different group/translation/language — their tags get
// merged; the page guard stops a near-identically-titled but differently-sized work, e.g.
// a zenpen/kouhen part pair, from merging now that pages no longer gate qualification),
// capped at mergeCap and ordered best-first so Apply[0] is the primary. With no qualifier
// the match isn't close enough → review. Language never changes the action.
func Decide(cands []Candidate) Decision {
	ranked := make([]Candidate, len(cands))
	copy(ranked, cands)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		if ranked[i].LangMatch != ranked[j].LangMatch {
			return ranked[i].LangMatch // same-language variant becomes the primary on a tie
		}
		if ranked[i].Gallery.NumFavorites != ranked[j].Gallery.NumFavorites {
			return ranked[i].Gallery.NumFavorites > ranked[j].Gallery.NumFavorites
		}
		return ranked[i].Gallery.ID < ranked[j].Gallery.ID
	})

	d := Decision{Action: ActionReview, Ranked: ranked}

	var qualifying []Candidate
	maxTitle := 0.0
	for _, c := range ranked {
		if qualifies(c) {
			qualifying = append(qualifying, c)
			if c.TitleScore > maxTitle {
				maxTitle = c.TitleScore
			}
		}
	}
	if len(qualifying) == 0 {
		return d // nothing close enough — human review
	}

	// Merge the near-identical variants: qualifiers within mergeTitleDelta of the best
	// title (so a weak-but-qualifying straggler is dropped when a clearly better match
	// exists), in ranked order, capped. Apply[0] is the primary; a secondary must also be
	// page-close to the primary so a same-titled but differently-sized work isn't merged.
	for _, c := range qualifying {
		if c.TitleScore < maxTitle-mergeTitleDelta {
			continue
		}
		if len(d.Apply) > 0 && !pagesCloseTo(d.Apply[0].Gallery.NumPages, c.Gallery.NumPages) {
			continue
		}
		d.Apply = append(d.Apply, c)
		if len(d.Apply) >= mergeCap {
			break
		}
	}
	d.Action = ActionAuto
	return d
}

// pagesCloseTo reports whether two known page counts are within pageTolerance. Unknown
// counts (<= 0 on either side) are treated as close, so a missing count never blocks a
// merge that the title already justified.
func pagesCloseTo(a, b int) bool {
	if a <= 0 || b <= 0 {
		return true
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= pageTolerance
}

// lenRatioFloor is the minimum short/long normalized-length ratio for the
// Levenshtein term to be trusted. Below it the strings are too lopsided for edit
// distance to mean anything (see Similarity).
const lenRatioFloor = 0.5

// Similarity returns a [0,1] title-similarity score, robust to word order and
// minor transliteration drift: it normalizes both sides, then takes the larger of
// token-set Jaccard and the Levenshtein ratio. Empty input on either side -> 0.
//
// The Levenshtein ratio is only meaningful when the two strings are of comparable
// length. A short title aligns by chance inside a much longer one and reports a
// misleading 20-30% even when they share no words (e.g. "A mischievous little game"
// vs "A Summer to Become an Adult - A Rural Retreat in Memory of H"), so when the
// lengths are lopsided we fall back to token overlap (Jaccard) as the honest score.
func Similarity(a, b string) float64 {
	na, nb := Normalize(a), Normalize(b)
	if na == "" || nb == "" {
		return 0
	}
	ra, rb := []rune(na), []rune(nb)
	j := jaccard(strings.Fields(na), strings.Fields(nb))

	short, long := len(ra), len(rb)
	if short > long {
		short, long = long, short
	}
	if long == 0 || float64(short)/float64(long) < lenRatioFloor {
		return j // too lopsided for edit distance — trust token overlap only
	}
	l := levRatio(ra, rb)
	if j > l {
		return j
	}
	return l
}

// titleParts returns a title plus its dual-language halves, split on the separators
// doujin titles use between romaji and english ("|", "/", and their fullwidth forms).
// The whole title is always kept too, so a legitimate "/"-containing title still matches
// as a whole. Empty/blank input yields nothing.
func titleParts(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := []string{s}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '|' || r == '/' || r == '｜' || r == '／'
	})
	if len(parts) > 1 {
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// Normalize lowercases, strips bracketed decorations common in doujin titles
// (`[artist]`, `(convention)`, `{...}`), turns punctuation into word boundaries,
// and collapses whitespace. Non-latin scripts (e.g. Japanese) pass through so
// japanese-vs-japanese comparisons still work.
func Normalize(s string) string {
	s = strings.ToLower(stripBrackets(s))
	var b strings.Builder
	lastSpace := true // leading -> no space emitted
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		// whitespace or punctuation both act as a single separator
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// stripBrackets removes the contents of (), [], and {} groups, including the
// brackets, handling nesting. Unbalanced closers are ignored.
func stripBrackets(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}
	inter := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// levRatio is 1 - editDistance/maxLen over rune slices, in [0,1].
func levRatio(a, b []rune) float64 {
	maxLen := max(len(a), len(b))
	if maxLen == 0 {
		return 0
	}
	return 1 - float64(levenshtein(a, b))/float64(maxLen)
}

// levenshtein is the standard two-row edit-distance DP over runes.
func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
