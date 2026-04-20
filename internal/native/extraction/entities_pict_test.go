package extraction

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestEntities_PICT consumes the pairwise matrix at
// testdata/entities.pict.tsv -- a PICT-generated combinatorial test
// matrix over 6 axes covering the input space of Entities(). For each
// row a content fixture is synthesised and the output is checked against
// category-specific invariants. The hand-picked tests in
// entities_test.go remain the readable regression layer; this test is
// the stress layer underneath.
func TestEntities_PICT(t *testing.T) {
	rows := readPICTMatrix(t, "testdata/entities.pict.tsv")
	if len(rows) == 0 {
		t.Fatal("no rows in PICT matrix; regenerate via `pict testdata/entities.pict > testdata/entities.pict.tsv`")
	}

	for i, row := range rows {
		row := row
		name := fmt.Sprintf("row_%02d_%s_%s_%s", i,
			row["PatternCategory"], row["UnicodeClass"], row["StopwordPresence"])
		t.Run(name, func(t *testing.T) {
			fixture := buildPICTFixture(row)
			got := Entities(fixture.Content)

			// --- Invariants enforced on every row ---

			if len(got) > MaxEntities {
				t.Errorf("output length %d exceeds MaxEntities=%d", len(got), MaxEntities)
			}
			if !isSorted(got) {
				t.Errorf("output not sorted: %v", got)
			}
			if hasDuplicates(got) {
				t.Errorf("output contains duplicates: %v", got)
			}

			// --- Category-conditional invariants ---

			cat := row["PatternCategory"]
			if cat == "None" {
				if len(got) != 0 {
					t.Errorf("None category must produce zero entities, got %v", got)
				}
				return
			}

			uni := row["UnicodeClass"]
			stop := row["StopwordPresence"]
			count := row["FinalEntityCount"]

			if count == "GtTwenty" {
				if len(got) != MaxEntities {
					t.Errorf("GtTwenty must saturate at %d, got %d: %v", MaxEntities, len(got), got)
				}
			}

			prefixCounts := countByPrefix(got)

			switch cat {
			case "CamelCase":
				if uni == "NonLatin" {
					// RE2 \b[A-Z][a-z]+ requires ASCII letters; CJK content
					// with the concept expressed in non-Latin script should
					// match zero CamelCase.
					if prefixCounts["entity"] > 0 {
						t.Errorf("non-latin content produced %d entity: tags, want 0",
							prefixCounts["entity"])
					}
				} else if prefixCounts["entity"] == 0 {
					t.Errorf("CamelCase + %s produced no entity: tags: %v", uni, got)
				}
			case "FilePath":
				if prefixCounts["file"] == 0 {
					t.Errorf("FilePath category produced no file: tags: %v", got)
				}
				if prefixCounts["file"] > filePathCap {
					t.Errorf("file: tag count %d exceeds filePathCap=%d",
						prefixCounts["file"], filePathCap)
				}
			case "ErrorType":
				if uni == "NonLatin" {
					if prefixCounts["error"] > 0 {
						t.Errorf("non-latin content produced %d error: tags, want 0",
							prefixCounts["error"])
					}
				} else if prefixCounts["error"] == 0 {
					t.Errorf("ErrorType + %s produced no error: tags: %v", uni, got)
				}
			case "PersonName":
				wantPerson := stop == "Absent" && uni == "ASCII"
				if wantPerson && prefixCounts["person"] == 0 {
					t.Errorf("PersonName/Absent/ASCII produced no person: tags: %v", got)
				}
				if stop == "Present" && prefixCounts["person"] > 0 {
					t.Errorf("PersonName/Present should NOT produce person: tags (stopword filter), got: %v", got)
				}
				if uni == "NonLatin" && prefixCounts["person"] > 0 {
					t.Errorf("non-latin person name leaked through ASCII-only heuristic: %v", got)
				}
			case "Multiple":
				// Multiple means "two or more categories together". Two
				// legitimate reasons only one prefix kind can show up:
				//   - NonLatin content suppresses ASCII-only regexes
				//   - LtFive fixture is a single-item mix
				//   - GtTwenty saturates the cap with CamelCase alone
				//     (entity: sorts first alphabetically so 20 CamelCase
				//      identifiers displace everything else)
				permissive := uni == "NonLatin" || count == "LtFive" || count == "GtTwenty"
				if !permissive && len(prefixCounts) < 2 {
					t.Errorf("Multiple + %s produced only %d prefix kind(s): %v",
						uni, len(prefixCounts), got)
				}
			}
		})
	}
}

// --- fixtures ----------------------------------------------------------

type pictFixture struct {
	Content string
}

// buildPICTFixture synthesises a content string from a PICT row's axis
// values. The synthesis is deterministic so failures are reproducible.
func buildPICTFixture(row map[string]string) pictFixture {
	cat := row["PatternCategory"]
	punc := row["PunctuationPosition"]
	uni := row["UnicodeClass"]
	dup := row["DuplicateDensity"]
	count := row["FinalEntityCount"]

	if cat == "None" {
		return pictFixture{Content: noneFixture(uni)}
	}

	// Build one or more blocks per category + concat.
	var blocks []string
	switch cat {
	case "CamelCase":
		blocks = append(blocks, camelCaseBlock(uni, punc, count))
	case "FilePath":
		blocks = append(blocks, filePathBlock(uni, punc, count))
	case "ErrorType":
		blocks = append(blocks, errorTypeBlock(uni, punc, count))
	case "PersonName":
		blocks = append(blocks, personNameBlock(uni, punc, row["StopwordPresence"]))
	case "Multiple":
		// Mix categories so multiple prefix kinds appear. GtTwenty
		// saturates the 20-cap with 25 letter-only CamelCase ids.
		n := 3
		if count == "FiveToTwenty" {
			n = 6
		} else if count == "GtTwenty" {
			n = 25
		}
		for i := 0; i < n; i++ {
			a := 'A' + byte(i%26)
			b := 'A' + byte((i/26)%26)
			blocks = append(blocks, fmt.Sprintf("ModuleName%c%c", a, b))
		}
		blocks = append(blocks, "./pkg/file.go")
		blocks = append(blocks, "RuntimeException")
		// Person name comes last with a lowercase preamble so it isn't
		// paired with the preceding "RuntimeException" by the detector.
		blocks = append(blocks, "with Kevin Burns")
	}

	one := strings.Join(blocks, " ")
	if dup == "Duplicated" {
		// Duplicate the whole block set (not just the first block) and
		// separate copies with a lowercase non-stopword so cross-copy
		// word pairs don't confuse the person-name detector.
		return pictFixture{Content: one + " plus " + one + " plus " + one}
	}
	return pictFixture{Content: one}
}

func noneFixture(uni string) string {
	switch uni {
	case "Latin1":
		return "voici une phrase simple sans motifs techniques."
	case "NonLatin":
		return "これは単純な文で技術パターンはありません。"
	default:
		return "plain prose with no identifiers, paths, errors, or names."
	}
}

func camelCaseBlock(uni, punc, count string) string {
	var ids []string
	n := 1
	if count == "FiveToTwenty" {
		n = 6
	} else if count == "GtTwenty" {
		n = 25
	}
	base := []string{
		"PaymentGateway", "WebhookRouter", "UserRepository", "CacheLayer",
		"AuthMiddleware", "MetricsPublisher", "QueueWorker", "SchedulerCore",
	}
	// Build letter-only suffixes so the whole identifier is [A-Za-z]+.
	// Digit suffixes break the CamelCase regex because the tail digit
	// keeps the \w+ region going without producing the trailing \b the
	// regex anchors on.
	for i := 0; i < n; i++ {
		a := 'A' + byte(i%26)
		b := 'A' + byte((i/26)%26)
		id := fmt.Sprintf("%sName%c%c", base[i%len(base)], a, b)
		ids = append(ids, id)
	}
	joined := strings.Join(ids, " ")
	joined = applyPunctuation(joined, punc)
	if uni == "NonLatin" {
		// Replace the pattern with CJK (which won't match the regex).
		return "支付网关 网络钩子 缓存层"
	}
	if uni == "Latin1" {
		return "François a déployé " + joined
	}
	return joined
}

func filePathBlock(uni, punc, count string) string {
	var paths []string
	n := 1
	if count == "FiveToTwenty" {
		n = 7
	} else if count == "GtTwenty" {
		n = 25
	}
	for i := 0; i < n; i++ {
		paths = append(paths, fmt.Sprintf("./src/pkg%d/file%d.go", i, i))
	}
	joined := strings.Join(paths, " ")
	joined = applyPunctuation(joined, punc)
	if uni == "NonLatin" {
		// Mix Japanese prose with a path -- still matches because \w in
		// Go RE2 is ASCII. This is deliberate parity with Python behaviour.
		return "ファイルは " + joined + " にあります"
	}
	if uni == "Latin1" {
		return "Le fichier est à " + joined
	}
	return joined
}

func errorTypeBlock(uni, punc, count string) string {
	errs := []string{
		"RuntimeException", "ValueError", "IOError", "TypeError",
		"ConnectionError", "TimeoutException", "NullPointerException",
	}
	n := 1
	if count == "FiveToTwenty" {
		n = 5
	} else if count == "GtTwenty" {
		n = len(errs)
	}
	joined := strings.Join(errs[:n], " ")
	joined = applyPunctuation(joined, punc)
	if uni == "NonLatin" {
		return "エラーが発生しました"
	}
	if uni == "Latin1" {
		return "Erreur: " + joined + " levée"
	}
	return joined
}

func personNameBlock(uni, punc, stop string) string {
	pair := "Kevin Burns"
	if stop == "Present" {
		// Both tokens capitalised but one is a stopword -> must be filtered.
		pair = "The Company"
	}
	pair = applyPunctuation(pair, punc)
	if uni == "NonLatin" {
		// Non-Latin "name" -- our ASCII heuristic must not extract.
		return "会议参加者: 王小明 李大明"
	}
	if uni == "Latin1" {
		return "Le chef s'appelle " + pair
	}
	return "Talked with " + pair + " today."
}

// applyPunctuation injects punctuation at the requested position
// around the first token of text (where position matters most for
// the person-name detector's Fields-based tokenisation).
func applyPunctuation(text, pos string) string {
	switch pos {
	case "Leading":
		return "," + text
	case "Trailing":
		return text + ","
	case "Embedded":
		// Insert a comma mid-phrase. For "Kevin Burns" this becomes
		// "Kevin, Burns" which stresses whether the detector still
		// sees a capitalised pair (it does, because punctuation is
		// stripped per-word before the pair check).
		parts := strings.Fields(text)
		if len(parts) >= 2 {
			return parts[0] + ", " + strings.Join(parts[1:], " ")
		}
		return text + ","
	default:
		return text
	}
}

// --- helpers -----------------------------------------------------------

// readPICTMatrix accepts testing.TB so *testing.T (TestEntities_PICT)
// and *testing.F (FuzzEntities seed corpus) can share one parser.
func readPICTMatrix(tb testing.TB, path string) []map[string]string {
	tb.Helper()
	f, err := os.Open(path)
	if err != nil {
		tb.Fatalf("open matrix: %v", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		tb.Fatalf("parse tsv: %v", err)
	}
	if len(records) < 2 {
		return nil
	}
	header := records[0]
	out := make([]map[string]string, 0, len(records)-1)
	for _, r := range records[1:] {
		m := make(map[string]string, len(header))
		for i, h := range header {
			if i < len(r) {
				m[strings.TrimSpace(h)] = strings.TrimSpace(r[i])
			}
		}
		out = append(out, m)
	}
	return out
}

func countByPrefix(entities []string) map[string]int {
	out := map[string]int{}
	for _, e := range entities {
		if idx := strings.IndexByte(e, ':'); idx > 0 {
			out[e[:idx]]++
		}
	}
	return out
}

func isSorted(ss []string) bool {
	return sort.StringsAreSorted(ss)
}

func hasDuplicates(ss []string) bool {
	seen := map[string]struct{}{}
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			return true
		}
		seen[s] = struct{}{}
	}
	return false
}
