package main

import (
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

func (a *app) bilingualName(title, slug string) string {
	chinese := a.modNames.Name(slug)
	if chinese != "" && chinese != title {
		return chinese + " | " + title
	}
	return title
}

func compactSearch(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

func pinyinKeys(text string) (string, string) {
	args := pinyin.NewArgs()
	args.Fallback = func(r rune, _ pinyin.Args) []string { return []string{string(r)} }
	var full, initials strings.Builder
	for _, syllable := range pinyin.LazyPinyin(text, args) {
		full.WriteString(syllable)
		for _, r := range syllable {
			initials.WriteRune(r)
			break
		}
	}
	return compactSearch(full.String()), compactSearch(initials.String())
}

func matchesSearch(text, query string) bool {
	if strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(query))) {
		return true
	}
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		return false
	}
	key := compactSearch(query)
	full, initials := pinyinKeys(text)
	return key != "" && (strings.Contains(full, key) || strings.Contains(initials, key))
}
