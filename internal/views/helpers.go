package views

import "strings"

// CouncillorSlug creates a URL-safe slug from a councillor name (lowercase last name, no punctuation).
func CouncillorSlug(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return ""
	}
	last := strings.ToLower(parts[len(parts)-1])
	// Remove apostrophes and other punctuation
	slug := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r
		}
		return -1
	}, last)
	return slug
}

// Initials returns the first letter of each word in name (max 2 letters)
func Initials(name string) string {
	if len(name) < 2 {
		return name
	}
	initials := ""
	inWord := false
	count := 0
	for _, r := range name {
		if r == ' ' {
			inWord = false
		} else if !inWord {
			inWord = true
			initials += string(r)
			count++
			if count >= 2 {
				break
			}
		}
	}
	return initials
}

// itoa converts an integer to string without importing strconv
func itoa(i int) string {
	if i < 0 {
		return "-" + itoa(-i)
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

// CouncillorID generates a unique identifier for a councillor based on type prefix and index
func CouncillorID(typePrefix string, index int) string {
	return typePrefix + "-" + itoa(index)
}
