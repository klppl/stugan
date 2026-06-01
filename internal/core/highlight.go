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
	// patternSrc/exceptionSrc are the original source strings, retained so the
	// rules can be projected back to the client (the compiled regexes can't be
	// stringified to their input). They mirror patterns/exceptions 1:1.
	patternSrc   []string
	exceptionSrc []string
}

// NewHighlighter compiles the configured patterns and exceptions. Empty
// input yields a highlighter that still flags nick mentions. Blank entries are
// skipped so a trailing empty line in user input doesn't match everything.
func NewHighlighter(patterns, exceptions []string) (*Highlighter, error) {
	h := &Highlighter{}
	compile := func(src []string) ([]*regexp.Regexp, []string, error) {
		out := make([]*regexp.Regexp, 0, len(src))
		kept := make([]string, 0, len(src))
		for _, p := range src {
			if p == "" {
				continue
			}
			re, err := regexp.Compile("(?i)" + p)
			if err != nil {
				return nil, nil, fmt.Errorf("highlight pattern %q: %w", p, err)
			}
			out = append(out, re)
			kept = append(kept, p)
		}
		return out, kept, nil
	}
	var err error
	if h.patterns, h.patternSrc, err = compile(patterns); err != nil {
		return nil, err
	}
	if h.exceptions, h.exceptionSrc, err = compile(exceptions); err != nil {
		return nil, err
	}
	return h, nil
}

// Patterns and Exceptions return the highlighter's source rules (nil for the
// default nick-mentions-only highlighter), for projecting to the client.
func (h *Highlighter) Patterns() []string {
	if h == nil {
		return nil
	}
	return h.patternSrc
}

func (h *Highlighter) Exceptions() []string {
	if h == nil {
		return nil
	}
	return h.exceptionSrc
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
