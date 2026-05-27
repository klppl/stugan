package core

import (
	"fmt"
	"regexp"
)

// Highlighter decides whether an incoming message should be highlighted: a
// mention of our nick, or a match of a configured pattern, unless an
// exception pattern also matches. Patterns are case-insensitive regexes.
type Highlighter struct {
	patterns   []*regexp.Regexp
	exceptions []*regexp.Regexp
}

// NewHighlighter compiles the configured patterns and exceptions. Empty
// input yields a highlighter that still flags nick mentions.
func NewHighlighter(patterns, exceptions []string) (*Highlighter, error) {
	h := &Highlighter{}
	compile := func(src []string) ([]*regexp.Regexp, error) {
		out := make([]*regexp.Regexp, 0, len(src))
		for _, p := range src {
			re, err := regexp.Compile("(?i)" + p)
			if err != nil {
				return nil, fmt.Errorf("highlight pattern %q: %w", p, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	var err error
	if h.patterns, err = compile(patterns); err != nil {
		return nil, err
	}
	if h.exceptions, err = compile(exceptions); err != nil {
		return nil, err
	}
	return h, nil
}

// Match reports whether text should be highlighted for the given nick.
func (h *Highlighter) Match(text, nick string) bool {
	if h == nil {
		return false
	}
	for _, re := range h.exceptions {
		if re.MatchString(text) {
			return false
		}
	}
	if nick != "" && mentionsNick(text, nick) {
		return true
	}
	for _, re := range h.patterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// mentionsNick reports a word-boundary, case-insensitive nick mention.
func mentionsNick(text, nick string) bool {
	re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(nick) + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(text)
}
