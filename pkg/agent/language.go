package agent

import "unicode"

func detectMessageLanguageHint(text string) string {
	hasCyrillic := false
	hasLatin := false
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Cyrillic):
			hasCyrillic = true
		case unicode.In(r, unicode.Latin):
			hasLatin = true
		}
		if hasCyrillic && hasLatin {
			break
		}
	}
	if hasCyrillic {
		return "ru"
	}
	if hasLatin {
		return "en"
	}
	return "en"
}
