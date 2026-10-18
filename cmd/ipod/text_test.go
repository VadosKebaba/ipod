package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTranslit(t *testing.T) {
	for in, want := range map[string]string{
		"Кино — Группа крови":    "Kino — Gruppa krovi",
		"Звезда по имени Солнце": "Zvezda po imeni Solntse",
		"Жанна Агузарова":        "Zhanna Aguzarova",
		"ЖУК":                    "ZHUK",
		"ДДТ":                    "DDT",
		"Щедрик":                 "Shchedrik",
		"ЩИ":                     "SHCHI",
		"Ёлка":                   "Yolka",
		"Їжак і Ґава":            "Yizhak i Gava",
		"Подъезд, объём":         "Podezd, obyom",
		"Мой Ж":                  "Moy Zh", // a capital alone: as before a small letter
		"Beatles: Help!":         "Beatles: Help!",
		"Рок-н-ролл 2026":        "Rok-n-roll 2026",
		"Ўладзімір":              "Uladzimir",
		"":                       "",
	} {
		if got := translit(in); got != want {
			t.Errorf("translit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateUTF8(t *testing.T) {
	for _, tt := range []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 0, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "hel"},
		{"Кино", 8, "Кино"},
		{"Кино", 7, "Кин"}, // 2 bytes a letter: never half of one
		{"Кино", 1, ""},
		{"a€b", 3, "a"},
		{"a€b", 4, "a€"},
	} {
		if got := truncateUTF8(tt.in, tt.max); got != tt.want {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
	long := strings.Repeat("Группа крови ", 30)
	got := truncateUTF8(long, 200)
	if len(got) > 200 || !utf8.ValidString(got) || !strings.HasPrefix(long, got) || len(got) < 199 {
		t.Fatalf("200-byte cap: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

func TestTextOut(t *testing.T) {
	if _, err := newTextOut("latin", 200); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err := newTextOut(textAsIs, -1); err == nil {
		t.Fatal("negative max accepted")
	}
	to, err := newTextOut(textTranslit, 10)
	if err != nil {
		t.Fatal(err)
	}
	// transliterated first, then cut
	if got := to.apply("Группа крови"); got != "Gruppa kro" {
		t.Fatalf("translit + cap: %q", got)
	}
	to, _ = newTextOut(textAsIs, 0)
	if got := to.apply("Группа крови"); got != "Группа крови" {
		t.Fatalf("asis, no cap: %q", got)
	}
}
