package speech

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var tag = regexp.MustCompile(`<[^>]*>`)

func StripSSML(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	return html.UnescapeString(tag.ReplaceAllString(s, " "))
}

type Sentence struct {
	Text   string
	Offset int
	Mark   string
}

var mark = regexp.MustCompile(`<mark\s+name\s*=\s*["']([^"']*)["']\s*/?>`)

func Pieces(text string, max int) []Sentence {
	var out []Sentence
	offset := 0
	markup := strings.Contains(text, "<")
	add := func(chunk string) {
		plain := chunk
		if markup {
			plain = html.UnescapeString(tag.ReplaceAllString(chunk, " "))
		}
		for _, s := range Split(plain, max) {
			s.Offset += offset
			out = append(out, s)
		}
		offset += utf8.RuneCountInString(plain)
	}
	last := 0
	for _, m := range mark.FindAllStringSubmatchIndex(text, -1) {
		add(text[last:m[0]])
		out = append(out, Sentence{Mark: text[m[2]:m[3]], Offset: offset})
		last = m[1]
	}
	add(text[last:])
	return out
}

func Split(text string, max int) []Sentence {
	var out []Sentence
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		chunk := strings.TrimRightFunc(text[start:end], unicode.IsSpace)
		if chunk != "" {
			out = append(out, Sentence{Text: chunk, Offset: utf8.RuneCountInString(text[:start])})
		}
		start = -1
	}
	for i, r := range text {
		if start < 0 {
			if unicode.IsSpace(r) {
				continue
			}
			start = i
		}
		size := utf8.RuneLen(r)
		switch {
		case r == '\n' && i+1 < len(text) && text[i+1] == '\n':
			flush(i)
		case strings.ContainsRune(".!?…。！？", r):
			next := i + size
			if next >= len(text) || unicode.IsSpace(rune(text[next])) || strings.ContainsRune("\"')]", rune(text[next])) {
				flush(next)
			}
		case max > 0 && i+size-start >= max && unicode.IsSpace(r):
			flush(i)
		}
	}
	flush(len(text))
	return out
}
