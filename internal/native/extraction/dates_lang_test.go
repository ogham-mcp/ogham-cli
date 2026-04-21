package extraction

import (
	"reflect"
	"testing"
	"time"
)

// TestDatesForLang_German verifies the German date pack parses the
// anchors ("heute", "morgen", "gestern"), modifier+weekday phrases
// ("nächsten Montag"), month names ("15. März 2026"), and "vor N
// Wochen" past offsets. The pack is YAML-driven so this test guards
// against silent YAML drift (e.g. a missing modifier entry would
// regress this output without visibly failing a parity test because
// the parity corpus doesn't exercise every axis).
func TestDatesForLang_German(t *testing.T) {
	// Wed 2026-04-15, same ref the English tests use so the relative
	// arithmetic lines up.
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name, content string
		want          []string
	}{
		{
			name:    "heute anchor",
			content: "Wir liefern heute.",
			want:    []string{iso(2026, time.April, 15)},
		},
		{
			name:    "morgen anchor resolves to next day",
			content: "Wir machen das morgen.",
			want:    []string{iso(2026, time.April, 16)},
		},
		{
			name:    "gestern anchor resolves to previous day",
			content: "Das war gestern erledigt.",
			want:    []string{iso(2026, time.April, 14)},
		},
		{
			name:    "nächsten Montag",
			content: "Wir treffen uns nächsten Montag.",
			want:    []string{iso(2026, time.April, 20)}, // Mon after Wed 15
		},
		{
			name:    "letzte Woche",
			content: "Das wurde letzte Woche entschieden.",
			want:    []string{iso(2026, time.April, 8)},
		},
		{
			name:    "vor 2 Wochen",
			content: "Wir haben das vor 2 Wochen besprochen.",
			want:    []string{iso(2026, time.April, 1)},
		},
		{
			name:    "in 3 Tagen",
			content: "Der Release erfolgt in 3 Tagen.",
			want:    []string{iso(2026, time.April, 18)},
		},
		{
			name:    "natural date 15. März 2026",
			content: "Die Konferenz war am 15. März 2026.",
			want:    []string{iso(2026, time.March, 15)},
		},
		{
			name:    "natural date 15 Maerz 2026 (ascii variant)",
			content: "Konferenz am 15 Maerz 2026.",
			want:    []string{iso(2026, time.March, 15)},
		},
		{
			name:    "absolute ISO still normalised under DE",
			content: "Geliefert am 2026/03/15.",
			want:    []string{iso(2026, time.March, 15)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DatesAtForLang(tc.content, ref, "de")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DatesAtForLang(de) = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDatesForLang_UnknownFallsBackToEnglish makes sure an unknown
// language code resolves the English anchors rather than returning
// empty. "klingon" is never in the registry.
func TestDatesForLang_UnknownFallsBackToEnglish(t *testing.T) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	got := DatesAtForLang("yesterday", ref, "klingon")
	want := []string{iso(2026, time.April, 14)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DatesAtForLang(klingon) = %v, want %v", got, want)
	}
}

// TestDatesForLang_EmptyCodeDefaultsToEnglish asserts the ""->"en" path
// in datePackFor -- a common caller shape when StoreOptions.Language
// isn't set.
func TestDatesForLang_EmptyCodeDefaultsToEnglish(t *testing.T) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	got := DatesAtForLang("tomorrow", ref, "")
	want := []string{iso(2026, time.April, 16)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DatesAtForLang('') = %v, want %v", got, want)
	}
}

// TestDatesForLang_PrefixAgoAllLanguages hand-picks one canonical
// prefix-ago or suffix-ago example per populated language to verify the
// YAML anchors + markers resolve correctly. Languages with CJK
// no-whitespace grammar (ja, zh) and postposed-modifier grammar for
// quantified phrases are covered by anchor-only checks below.
//
// The 18 language codes (as enumerated by ListLanguages()): ar, de, en,
// es, fr, ga, hi, it, ja, ko, nl, pl, pt, pt-br, ru, tr, uk, zh.
func TestDatesForLang_PrefixAgoAllLanguages(t *testing.T) {
	// Ref: Wed 2026-04-15.
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		lang    string
		content string
		want    string // single expected ISO date
	}{
		// Prefix-ago family.
		{"fr", "nous avons déployé il y a 2 semaines.", iso(2026, 4, 1)},
		{"es", "lanzamos hace 2 semanas.", iso(2026, 4, 1)},
		{"pt", "lançámos há 2 semanas.", iso(2026, 4, 1)},
		{"pt-br", "lançamos há 2 semanas.", iso(2026, 4, 1)},
		{"de", "wir haben das vor 2 Wochen besprochen.", iso(2026, 4, 1)},
		{"ar", "أنهينا قبل 2 أسبوع.", iso(2026, 4, 1)},

		// Suffix-ago family.
		{"en", "we shipped 2 weeks ago.", iso(2026, 4, 1)},
		{"it", "abbiamo rilasciato 2 settimane fa.", iso(2026, 4, 1)},
		{"nl", "we hebben 2 weken geleden gereleased.", iso(2026, 4, 1)},
		{"pl", "wdrożyliśmy 2 tygodnie temu.", iso(2026, 4, 1)},
		{"ru", "мы выпустили 2 недели назад.", iso(2026, 4, 1)},
		{"uk", "ми випустили 2 тижні тому.", iso(2026, 4, 1)},
		{"tr", "2 hafta önce yayınladık.", iso(2026, 4, 1)},
		{"hi", "हमने 2 हफ्ते पहले जारी किया.", iso(2026, 4, 1)},
		{"ko", "우리는 2 주 전 배포했습니다.", iso(2026, 4, 1)},
		{"ga", "sheolamar 2 seachtaini ó shin.", iso(2026, 4, 1)},
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			got := DatesAtForLang(tc.content, ref, tc.lang)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("lang=%s content=%q: got %v, want [%s]",
					tc.lang, tc.content, got, tc.want)
			}
		})
	}
}

// TestDatesForLang_PrefixInAllLanguages exercises prefix-in ("in N
// units") for languages that populate a prefix in_marker. Turkish and
// Korean use suffix in_markers ("sonra", "후") -- covered in a
// separate case.
func TestDatesForLang_PrefixInAllLanguages(t *testing.T) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		lang    string
		content string
		want    string
	}{
		{"en", "we ship in 3 days.", iso(2026, 4, 18)},
		{"fr", "nous livrons dans 3 jours.", iso(2026, 4, 18)},
		{"es", "entregamos dentro de 3 días.", iso(2026, 4, 18)},
		{"it", "consegniamo fra 3 giorni.", iso(2026, 4, 18)},
		{"pt", "entregamos daqui a 3 dias.", iso(2026, 4, 18)},
		{"pt-br", "entregamos daqui a 3 dias.", iso(2026, 4, 18)},
		{"de", "wir liefern in 3 Tagen.", iso(2026, 4, 18)},
		{"nl", "we leveren over 3 dagen.", iso(2026, 4, 18)},
		{"pl", "dostarczymy za 3 dni.", iso(2026, 4, 18)},
		{"ru", "доставим через 3 дня.", iso(2026, 4, 18)},
		{"uk", "доставимо через 3 дні.", iso(2026, 4, 18)},
		{"ar", "سنسلم بعد 3 أيام.", iso(2026, 4, 18)},
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			got := DatesAtForLang(tc.content, ref, tc.lang)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("lang=%s content=%q: got %v, want [%s]",
					tc.lang, tc.content, got, tc.want)
			}
		})
	}
}

// TestDatesForLang_SuffixInAllLanguages exercises suffix-in forms
// (Turkish "sonra", Korean "후"/"뒤").
func TestDatesForLang_SuffixInAllLanguages(t *testing.T) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		lang    string
		content string
		want    string
	}{
		{"tr", "3 gün sonra çıkacak.", iso(2026, 4, 18)},
		{"ko", "3 일 후 배포합니다.", iso(2026, 4, 18)},
		{"hi", "हम 3 दिन बाद जारी करेंगे.", iso(2026, 4, 18)},
	}

	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			got := DatesAtForLang(tc.content, ref, tc.lang)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("lang=%s content=%q: got %v, want [%s]",
					tc.lang, tc.content, got, tc.want)
			}
		})
	}
}

// TestDatesForLang_AnchorsCJKOnly covers ja + zh -- anchors only.
// Quantified forms in CJK are deferred to v0.8 (no-whitespace grammar).
func TestDatesForLang_AnchorsCJKOnly(t *testing.T) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		lang, content, want string
	}{
		{"ja", "今日 の リリース", iso(2026, 4, 15)},
		{"ja", "明日 に ミーティング", iso(2026, 4, 16)},
		{"ja", "昨日 の 作業", iso(2026, 4, 14)},
		{"zh", "今天 发布", iso(2026, 4, 15)},
		{"zh", "明天 开会", iso(2026, 4, 16)},
		{"zh", "昨天 的 工作", iso(2026, 4, 14)},
	}

	for _, tc := range cases {
		t.Run(tc.lang+"/"+tc.want, func(t *testing.T) {
			got := DatesAtForLang(tc.content, ref, tc.lang)
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("lang=%s content=%q: got %v, want [%s]",
					tc.lang, tc.content, got, tc.want)
			}
		})
	}
}
