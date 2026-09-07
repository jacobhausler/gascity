package beads

import (
	"fmt"
	"strings"
)

// Comment appends a comment to a bead through `bd comment`, the same verb the
// `gc bd comment` passthrough reaches today. Text is passed as a single argv
// element, so no shell quoting applies and a multi-word or newline-bearing
// comment survives verbatim.
//
// Append-only by construction: bd exposes no edit or delete for a comment, so
// neither does this. An empty comment is refused rather than written, because
// bd would otherwise open its editor-less "no text" path and the caller would
// learn nothing about why nothing landed.
func (s *BdStore) Comment(id, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("commenting on bead %q: %w", id, ErrEmptyComment)
	}
	if err := s.runBDTransientWrite("comment", id, text); err != nil {
		if isBdNotFound(err) {
			return fmt.Errorf("commenting on bead %q: %w", id, ErrNotFound)
		}
		return fmt.Errorf("commenting on bead %q: %w", id, err)
	}
	return nil
}
