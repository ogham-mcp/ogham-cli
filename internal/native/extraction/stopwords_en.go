package extraction

// englishStopwords is the standard Snowball-style English stopword list
// used by Python's stop_words package (which is what src/ogham/extraction.py
// loads at module import time). Used here only by the person-name detector
// to avoid emitting "The Company" or "Of Course" as person: tags.
//
// Kept deliberately conservative (~175 words). v0.6 will port equivalents
// for de, fr, es, zh via YAML word lists -- do NOT inline further languages
// in this file.
var englishStopwords = buildStopwordSet([]string{
	"a", "about", "above", "after", "again", "against", "all", "am", "an",
	"and", "any", "are", "aren't", "as", "at", "be", "because", "been",
	"before", "being", "below", "between", "both", "but", "by", "can't",
	"cannot", "could", "couldn't", "did", "didn't", "do", "does", "doesn't",
	"doing", "don't", "down", "during", "each", "few", "for", "from",
	"further", "had", "hadn't", "has", "hasn't", "have", "haven't", "having",
	"he", "he'd", "he'll", "he's", "her", "here", "here's", "hers", "herself",
	"him", "himself", "his", "how", "how's", "i", "i'd", "i'll", "i'm",
	"i've", "if", "in", "into", "is", "isn't", "it", "it's", "its", "itself",
	"let's", "me", "more", "most", "mustn't", "my", "myself", "no", "nor",
	"not", "of", "off", "on", "once", "only", "or", "other", "ought", "our",
	"ours", "ourselves", "out", "over", "own", "same", "shan't", "she",
	"she'd", "she'll", "she's", "should", "shouldn't", "so", "some", "such",
	"than", "that", "that's", "the", "their", "theirs", "them", "themselves",
	"then", "there", "there's", "these", "they", "they'd", "they'll",
	"they're", "they've", "this", "those", "through", "to", "too", "under",
	"until", "up", "very", "was", "wasn't", "we", "we'd", "we'll", "we're",
	"we've", "were", "weren't", "what", "what's", "when", "when's", "where",
	"where's", "which", "while", "who", "who's", "whom", "why", "why's",
	"with", "won't", "would", "wouldn't", "you", "you'd", "you'll", "you're",
	"you've", "your", "yours", "yourself", "yourselves",
})

// buildStopwordSet turns the literal list into a membership set keyed by
// lowercase form. Encapsulated so we can swap the backing representation
// later (a sorted slice + binary search is measurably faster for small
// sets at this size, but the map keeps the callsites cleaner).
func buildStopwordSet(words []string) map[string]struct{} {
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		out[w] = struct{}{}
	}
	return out
}

// isEnglishStopword reports whether the lowercase form of w is in the
// English stopword list. Callers pass w already lowercased.
func isEnglishStopword(wLower string) bool {
	_, ok := englishStopwords[wLower]
	return ok
}
