package autotag

import (
	"testing"

	"doujin/internal/nhentai"
)

func sr(id int64, en, jp string, pages, favs int) nhentai.SearchResult {
	return nhentai.SearchResult{ID: id, EnglishTitle: en, JapaneseTitle: jp, NumPages: pages, NumFavorites: favs}
}

// noLang is a candLang resolver reporting no language for any candidate.
func noLang(nhentai.SearchResult) string { return "" }

// langByID builds a candLang resolver from a gallery-id -> language map.
func langByID(m map[int64]string) func(nhentai.SearchResult) string {
	return func(r nhentai.SearchResult) string { return m[r.ID] }
}

func applyIDs(cs []Candidate) []int64 {
	ids := make([]int64, len(cs))
	for i, c := range cs {
		ids[i] = c.Gallery.ID
	}
	return ids
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[Artist] Cool Title (English) {scan}", "cool title"},
		{"Hello, World!! 123", "hello world 123"},
		{"  spaced   out  ", "spaced out"},
		{"UPPER Case", "upper case"},
		{"nested ([a] b) keep", "nested keep"},
		{"からきし傭兵団", "からきし傭兵団"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	if got := Similarity("Naruto", "naruto"); got != 1 {
		t.Errorf("identical (case) = %v, want 1", got)
	}
	if got := Similarity("a b c", "c b a"); got != 1 {
		t.Errorf("reordered tokens = %v, want 1 (Jaccard)", got)
	}
	if got := Similarity("apple", "xyzzy"); got >= 0.3 {
		t.Errorf("unrelated = %v, want < 0.3", got)
	}
	if got := Similarity("", "x"); got != 0 {
		t.Errorf("empty side = %v, want 0", got)
	}
}

// A short candidate must not score ~20-30% against a much longer, unrelated local
// title just because edit distance "aligns" it by chance. Brackets are stripped by
// Normalize, so the decorated candidate is passed verbatim to prove that too.
func TestSimilarityLopsidedLengthsUseTokenOverlap(t *testing.T) {
	const local = "A Summer to Become an Adult - A Rural Retreat in Memory of H"
	if got := Similarity(local, "[Imuneko] A mischievous little game [Digital]"); got >= 0.15 {
		t.Errorf("lopsided unrelated = %v, want < 0.15 (token overlap, not edit-distance floor)", got)
	}
	// A genuine near-duplicate of comparable length still benefits from the
	// Levenshtein term (minor transliteration drift, low token overlap).
	if got := Similarity("Okosama ni mo Osusume", "Okosama nimo Osusume"); got < 0.8 {
		t.Errorf("comparable near-duplicate = %v, want >= 0.8 (levRatio still applies)", got)
	}
}

func TestScoreTakesMaxOverTitlesAndPageBonus(t *testing.T) {
	// Local is romaji; only the english online title matches. Page count is exact.
	c := Score([]string{"Karakishi Youhei-dan"}, 50, "", "", sr(1, "Karakishi Youhei-dan", "からきし傭兵団", 50, 5))
	if c.TitleScore < 0.99 {
		t.Errorf("title score = %v, want ~1 from english match", c.TitleScore)
	}
	if !c.PagesExact || !c.PagesClose || c.PageDelta != 0 {
		t.Errorf("page flags wrong: %+v", c)
	}
	if c.Score < c.TitleScore+pageBonus-1e-9 {
		t.Errorf("score = %v, want titleScore+bonus", c.Score)
	}

	// No page count known -> no bonus, not exact/close, delta unknown.
	c2 := Score([]string{"Karakishi Youhei-dan"}, 0, "", "", sr(1, "Karakishi Youhei-dan", "", 50, 5))
	if c2.PagesExact || c2.PagesClose || c2.PageDelta != -1 || c2.Score != c2.TitleScore {
		t.Errorf("zero local pages should give no page signal: %+v", c2)
	}
}

func TestScorePageToleranceAndBonusScaling(t *testing.T) {
	mk := func(localPages, candPages int) Candidate {
		return Score([]string{"x"}, localPages, "", "", sr(1, "x", "", candPages, 0))
	}
	if c := mk(242, 244); !c.PagesClose || c.PagesExact || c.PageDelta != 2 {
		t.Errorf("±2 should be close (not exact): %+v", c)
	}
	if c := mk(242, 245); c.PagesClose {
		t.Errorf("±3 should NOT be close: %+v", c)
	}
	// Exact pages must outrank a within-tolerance match (bonus scales down with delta).
	if exact, near := mk(242, 242), mk(242, 244); exact.Score <= near.Score {
		t.Errorf("exact score %v should beat ±2 score %v", exact.Score, near.Score)
	}
}

func TestScoreLanguageBoostAndFlags(t *testing.T) {
	match := Score([]string{"x"}, 10, "english", "english", sr(1, "x", "", 10, 0))
	if !match.LangMatch || match.LangMismatch {
		t.Errorf("expected LangMatch only: %+v", match)
	}
	mismatch := Score([]string{"x"}, 10, "english", "japanese", sr(1, "x", "", 10, 0))
	if mismatch.LangMatch || !mismatch.LangMismatch {
		t.Errorf("expected LangMismatch only: %+v", mismatch)
	}
	if match.Score <= mismatch.Score {
		t.Errorf("same-language boost: match %v should beat mismatch %v", match.Score, mismatch.Score)
	}
	// "translated" is english-family.
	if c := Score([]string{"x"}, 10, "translated", "english", sr(1, "x", "", 10, 0)); !c.LangMatch {
		t.Errorf("translated should match english-family: %+v", c)
	}
	// Unknown language on either side -> no flags, no boost.
	if c := Score([]string{"x"}, 10, "", "english", sr(1, "x", "", 10, 0)); c.LangMatch || c.LangMismatch {
		t.Errorf("unknown local language -> no flags: %+v", c)
	}
}

func TestScoreDualLanguageVariantBeatsWholeTitle(t *testing.T) {
	// A "romaji / english" title scores poorly as a whole against an english-only
	// online title, but the english *half* (a variant) matches strongly. Score must
	// take the best variant.
	whole := "Do Namaiki na Juma-kun o Mechakucha Wakaraseru / Teaching the Super Cheeky Juma-kun One Hell of a Lesson"
	englishHalf := "Teaching the Super Cheeky Juma-kun One Hell of a Lesson"
	online := sr(1, "Teaching the Super Cheeky Juma-kun One Hell of a Lesson", "", 0, 0)

	whole_only := Score([]string{whole}, 0, "", "", online)
	with_variants := Score([]string{whole, englishHalf}, 0, "", "", online)
	if with_variants.TitleScore <= whole_only.TitleScore {
		t.Errorf("variant score %v should beat whole-title score %v",
			with_variants.TitleScore, whole_only.TitleScore)
	}
	if with_variants.TitleScore < 0.99 {
		t.Errorf("english-half variant should match ~1.0, got %v", with_variants.TitleScore)
	}
}

func TestScoreSplitsCandidateDualLanguageTitle(t *testing.T) {
	// The candidate's english_title is itself "romaji | english" with a leading event
	// and trailing brackets. The english half must match a clean english local title
	// strongly enough to qualify — the romaji half + decorations must not dilute it.
	local := "A Dreadful Diet Method that Surprisingly Feels Good"
	cand := sr(1, "Okosama ni mo Osusume Odoroku Hodo Kimochi Ii Kyoui no Diet Jutsu | Also Recommended for Kids: A Dreadful Diet Method that Surprisingly Feels Good (COMIC LO 2019-10) [English] [SakuraCircle] [Digital]", "", 19, 4)
	c := Score([]string{local}, 19, "", "", cand)
	if c.TitleScore < qualifyTitle {
		t.Errorf("title score = %v, want >= %v (candidate english half should match)", c.TitleScore, qualifyTitle)
	}
	if d := Decide([]Candidate{c}); d.Action != ActionAuto {
		t.Errorf("action = %q, want auto for the dual-language candidate", d.Action)
	}
}

func TestDecideAutoRomajiLocalEnglishOnline(t *testing.T) {
	cands := ScoreAll([]string{"Karakishi Youhei-dan Compilation"}, 50, "", []nhentai.SearchResult{
		sr(10, "Karakishi Youhei-dan Compilation", "からきし傭兵団総集編", 50, 12),
		sr(11, "Some Other Doujin", "別の作品", 30, 3),
	}, noLang)
	d := Decide(cands)
	if d.Action != ActionAuto {
		t.Fatalf("action = %q, want auto", d.Action)
	}
	if d.Ranked[0].Gallery.ID != 10 {
		t.Errorf("top = %d, want 10", d.Ranked[0].Gallery.ID)
	}
	if got := applyIDs(d.Apply); len(got) != 1 || got[0] != 10 {
		t.Errorf("apply = %v, want [10] (only the strong match)", got)
	}
}

func TestDecideAutoJapaneseLocalJapaneseOnline(t *testing.T) {
	cands := ScoreAll([]string{"からきし傭兵団"}, 20, "", []nhentai.SearchResult{
		sr(10, "Mercenary Group", "からきし傭兵団", 20, 8),
		sr(11, "Unrelated", "全然違う", 21, 1),
	}, noLang)
	if d := Decide(cands); d.Action != ActionAuto {
		t.Errorf("action = %q, want auto (japanese-vs-japanese match)", d.Action)
	}
}

func TestDecideMergesDuplicateExactPageVariants(t *testing.T) {
	// Two strong candidates share the title + page count — the same work in two
	// variations (e.g. different group/translation). The model merges their tags and
	// auto-applies rather than asking for review.
	cands := ScoreAll([]string{"alpha beta gamma"}, 10, "", []nhentai.SearchResult{
		sr(10, "alpha beta gamma", "", 10, 5),
		sr(11, "alpha beta gamma", "", 11, 9), // +1 page, still within tolerance
	}, noLang)
	d := Decide(cands)
	if d.Action != ActionAuto {
		t.Fatalf("action = %q, want auto (merge variants)", d.Action)
	}
	if got := applyIDs(d.Apply); len(got) != 2 {
		t.Errorf("apply = %v, want both variants merged", got)
	}
}

func TestDecideMergeWindowExcludesWeakerTitle(t *testing.T) {
	// A clearly-better title exists; a weaker (but still qualifying) candidate that
	// shares the page count is dropped from the merge to avoid tag pollution.
	cands := ScoreAll([]string{"one two three four five six"}, 10, "", []nhentai.SearchResult{
		sr(10, "one two three four five six", "", 10, 5), // title ~1.0
		sr(11, "one two three four nine ten", "", 10, 9), // qualifies (~0.7) but weaker
	}, noLang)
	d := Decide(cands)
	if d.Action != ActionAuto {
		t.Fatalf("action = %q, want auto", d.Action)
	}
	if got := applyIDs(d.Apply); len(got) != 1 || got[0] != 10 {
		t.Errorf("apply = %v, want only the strong-title [10]", got)
	}
}

func TestDecideSameLanguageBecomesPrimary(t *testing.T) {
	// Identical title + pages; an English and a Japanese variant. Local is English, so
	// the English gallery becomes the primary (Apply[0]) even though the Japanese one
	// has more favorites; both still merge.
	results := []nhentai.SearchResult{
		sr(10, "same title", "", 20, 50), // english
		sr(11, "same title", "", 20, 99), // japanese, more favorites
	}
	cands := ScoreAll([]string{"same title"}, 20, "english", results,
		langByID(map[int64]string{10: "english", 11: "japanese"}))
	d := Decide(cands)
	if d.Action != ActionAuto {
		t.Fatalf("action = %q, want auto", d.Action)
	}
	if d.Apply[0].Gallery.ID != 10 {
		t.Errorf("primary = %d, want 10 (same language wins over favorites)", d.Apply[0].Gallery.ID)
	}
	if len(d.Apply) != 2 {
		t.Errorf("both variants should merge, apply = %v", applyIDs(d.Apply))
	}
}

func TestDecideLanguageMismatchStillAutoApplies(t *testing.T) {
	// The only confident match is a different language — a full title + close pages
	// still auto-applies (language never forces review), and the candidate carries the
	// mismatch flag for display.
	cands := ScoreAll([]string{"lonely title"}, 30, "english", []nhentai.SearchResult{
		sr(10, "lonely title", "", 30, 5),
	}, langByID(map[int64]string{10: "japanese"}))
	d := Decide(cands)
	if d.Action != ActionAuto {
		t.Fatalf("action = %q, want auto despite language mismatch", d.Action)
	}
	if !d.Apply[0].LangMismatch {
		t.Errorf("applied candidate should be flagged LangMismatch for display")
	}
}

func TestDecideReviewWhenTitleTooWeak(t *testing.T) {
	// Page count within tolerance but the title is unrelated -> not close enough.
	cands := ScoreAll([]string{"alpha beta"}, 10, "", []nhentai.SearchResult{
		sr(10, "totally unrelated title", "", 10, 5),
	}, noLang)
	if d := Decide(cands); d.Action != ActionReview {
		t.Errorf("action = %q, want review (weak title)", d.Action)
	}
}

func TestDecideReviewWhenPagesFarOff(t *testing.T) {
	// Perfect title but the page count is well outside tolerance -> review.
	cands := ScoreAll([]string{"exact title match"}, 10, "", []nhentai.SearchResult{
		sr(10, "exact title match", "", 25, 5),
	}, noLang)
	if d := Decide(cands); d.Action != ActionReview || len(d.Apply) != 0 {
		t.Errorf("action = %q apply = %v, want review/none (pages far off)", d.Action, applyIDs(d.Apply))
	}
}

func TestDecideReviewOnEmptyAndGarbage(t *testing.T) {
	if d := Decide(nil); d.Action != ActionReview || len(d.Ranked) != 0 {
		t.Errorf("empty: action=%q ranked=%d, want review/0", d.Action, len(d.Ranked))
	}
	cands := ScoreAll([]string{}, 0, "", []nhentai.SearchResult{sr(10, "anything", "", 5, 1)}, noLang)
	if d := Decide(cands); d.Action != ActionReview {
		t.Errorf("garbage local title: action=%q, want review", d.Action)
	}
}
