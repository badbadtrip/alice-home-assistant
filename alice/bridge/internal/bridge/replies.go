package bridge

import "sync/atomic"

var replies atomic.Pointer[map[string]string]

// SetReplies installs the owner's texts from the panel; an empty one keeps the default.
func SetReplies(texts map[string]string) {
	copied := make(map[string]string, len(texts))
	for key, text := range texts {
		if text != "" {
			copied[key] = text
		}
	}
	replies.Store(&copied)
}

// Reply returns the owner's text for a situation, or the built-in one.
func Reply(key, fallback string) string {
	if texts := replies.Load(); texts != nil {
		if text, ok := (*texts)[key]; ok {
			return text
		}
	}
	return fallback
}
