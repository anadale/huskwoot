package usecase

import (
	"strings"
	"unicode"
)

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func Slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsDigit(r) || (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		} else if lat, ok := translit[r]; ok {
			b.WriteString(lat)
		} else {
			b.WriteByte('-')
		}
	}
	s := collapseDashes(b.String())
	s = strings.Trim(s, "-")
	if s == "" {
		return "project"
	}
	return s
}

func collapseDashes(s string) string {
	var b strings.Builder
	var prev byte
	for i := 0; i < len(s); i++ {
		if s[i] == '-' && prev == '-' {
			continue
		}
		b.WriteByte(s[i])
		prev = s[i]
	}
	return b.String()
}
