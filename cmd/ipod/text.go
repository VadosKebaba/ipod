package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What track names look like on their way to the head unit (--text): a head unit's font may
// not have Cyrillic (the 2014 Infiniti Q60's Clarion X9U6 is unknown), so they can be
// transliterated to Latin.
const (
	textAsIs     = "asis"
	textTranslit = "translit"
)

// textOut prepares the track names sent to the head unit.
type textOut struct {
	mode string // textAsIs or textTranslit
	max  int    // bytes of UTF-8 at most, cut on a character boundary; 0: no limit
}

func newTextOut(mode string, max int) (textOut, error) {
	if mode != textAsIs && mode != textTranslit {
		return textOut{}, fmt.Errorf("--text=%s: expected %s or %s", mode, textAsIs, textTranslit)
	}
	if max < 0 {
		return textOut{}, fmt.Errorf("--text-max=%d: must be 0 (no limit) or more", max)
	}
	return textOut{mode: mode, max: max}, nil
}

func (t textOut) apply(s string) string {
	if t.mode == textTranslit {
		s = translit(s)
	}
	return truncateUTF8(s, t.max)
}

// truncateUTF8 cuts s to at most max bytes without splitting a character; max 0: no limit.
func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := 0
	for cut < len(s) {
		_, size := utf8.DecodeRuneInString(s[cut:])
		if cut+size > max {
			break
		}
		cut += size
	}
	return s[:cut]
}

// Russian, Ukrainian and Belarusian letters, lower case; upper case is derived.
var cyrillicLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya", 'є': "ye", 'і': "i", 'ї': "yi", 'ґ': "g", 'ў': "u",
}

// translit writes Cyrillic in Latin letters ("Кино — Группа крови" -> "Kino — Gruppa krovi");
// everything else is left as it is. A capital becomes "Zh" before a small letter and "ZH"
// in a word in capitals.
func translit(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		lat, ok := cyrillicLatin[unicode.ToLower(r)]
		if !ok {
			b.WriteRune(r)
			continue
		}
		if !unicode.IsUpper(r) || lat == "" {
			b.WriteString(lat)
			continue
		}
		next := i+1 < len(runes) && unicode.IsUpper(runes[i+1])
		prev := i > 0 && unicode.IsUpper(runes[i-1])
		lastOfWord := i+1 >= len(runes) || !unicode.IsLetter(runes[i+1])
		if next || (prev && lastOfWord) {
			b.WriteString(strings.ToUpper(lat))
		} else {
			b.WriteString(strings.ToUpper(lat[:1]) + lat[1:])
		}
	}
	return b.String()
}
