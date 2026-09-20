package utils

import (
	"strings"
	"unicode"
)

// codeStopwords are tokens that carry no retrieval signal. Language keywords
// and the reflexive noise of method chains are dropped so matches are driven
// by project-specific names.
var codeStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "func": true, "function": true,
	"return": true, "var": true, "const": true, "let": true, "type": true,
	"struct": true, "interface": true, "class": true, "import": true,
	"export": true, "from": true, "package": true, "if": true, "else": true,
	"range": true, "new": true, "make": true, "nil": true, "true": true,
	"false": true, "this": true, "self": true, "public": true, "private": true,
	"static": true, "final": true, "void": true, "string": true, "bool": true,
	"byte": true, "error": true, "context": true, "value": true, "values": true,
	"key": true, "keys": true, "when": true, "then": true,
}

// splitIdentifier expands one identifier into lowercase word parts.
// CamelCase, PascalCase, and snake_case bounds all split, so FooBarBaz and
// foo_bar_baz both yield foo, bar, baz.
func splitIdentifier(id string) []string {
	var parts []string
	var current []rune
	runes := []rune(id)

	flush := func() {
		if len(current) > 0 {
			parts = append(parts, strings.ToLower(string(current)))
			current = current[:0]
		}
	}

	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
		case unicode.IsUpper(r):
			// Split before an upper run that starts a new word, but keep
			// acronym runs together: HTTPServer -> http, server.
			if len(current) > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) ||
				(i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
				flush()
			}
			current = append(current, r)
		default:
			current = append(current, r)
		}
	}
	flush()
	return parts
}

// walkIdentifiers calls fn for every identifier-like run in s, in order.
func walkIdentifiers(s string, fn func(string)) {
	start := -1
	for i, r := range s {
		if unicode.IsLetter(r) || r == '_' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			fn(s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		fn(s[start:])
	}
}

// TokenizeCode splits source into distinct lowercase identifier tokens in
// first-seen order, expanding camelCase and snake_case and dropping keywords
// and short noise.
func TokenizeCode(s string) []string {
	seen := make(map[string]bool)
	var tokens []string
	walkIdentifiers(s, func(id string) {
		for _, part := range splitIdentifier(id) {
			if len(part) < 3 || codeStopwords[part] || seen[part] {
				continue
			}
			seen[part] = true
			tokens = append(tokens, part)
		}
	})
	return tokens
}
