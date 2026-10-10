// Package jsonpath parses the path syntax the autoupdate configuration uses to
// point at a value inside a JSON document, such as "tag_name", "[0].sha" or
// "versions[0].version".
//
// A path is a non-empty sequence of segments. A field segment is one or more
// characters other than '.', '[' and ']', and every field except a leading one
// is preceded by exactly one '.'. An index segment is "[N]", N being one or
// more ASCII digits; it follows a field, another index or the start of the
// path, with no '.' before it. Any other path is rejected with an error that
// wraps ErrInvalidJSONPath and quotes the path.
//
// The package imports only the standard library, so the response parser and
// the registry validator can share one grammar and reach the same verdict.
package jsonpath

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalidJSONPath is wrapped by every error Parse returns.
var ErrInvalidJSONPath = errors.New("invalid JSON path syntax")

// Kind tells a field segment from an index segment.
type Kind int

const (
	// KindField selects a member of a JSON object by name.
	KindField Kind = iota
	// KindIndex selects an element of a JSON array by position.
	KindIndex
)

// Segment is one step of a parsed path.
type Segment struct {
	Kind  Kind
	Field string // member name, set when Kind is KindField
	Index int    // array position, set when Kind is KindIndex
}

// Parse splits path into its segments, or rejects it when it is outside the
// grammar described in the package documentation.
func Parse(path string) ([]Segment, error) {
	if path == "" {
		return nil, invalid(path, 0, "empty path")
	}

	var segments []Segment
	i := 0
	if path[0] != '[' {
		seg, next, err := parseField(path, 0)
		if err != nil {
			return nil, err
		}
		segments = append(segments, seg)
		i = next
	}

	for i < len(path) {
		var (
			seg  Segment
			next int
			err  error
		)
		switch path[i] {
		case '[':
			seg, next, err = parseIndex(path, i)
		case '.':
			seg, next, err = parseField(path, i+1)
		case ']':
			err = invalid(path, i, "']' closes no '['")
		default:
			// Only an index can stop in front of another character: a
			// field runs until '.', '[', ']' or the end of the path.
			err = invalid(path, i, "missing '.' or '[' after ']'")
		}
		if err != nil {
			return nil, err
		}
		segments = append(segments, seg)
		i = next
	}

	return segments, nil
}

// parseField reads the field that starts at offset start and returns it with
// the offset of the first byte after it.
func parseField(path string, start int) (Segment, int, error) {
	end := start
	for end < len(path) && !isDelimiter(path[end]) {
		end++
	}
	if end == start {
		if end < len(path) && path[end] == ']' {
			return Segment{}, 0, invalid(path, end, "']' closes no '['")
		}
		return Segment{}, 0, invalid(path, start, "empty field")
	}
	return Segment{Kind: KindField, Field: path[start:end]}, end, nil
}

// parseIndex reads the "[N]" whose '[' sits at offset open and returns it
// with the offset of the first byte after its ']'.
func parseIndex(path string, open int) (Segment, int, error) {
	end := open + 1
	for end < len(path) && isDigit(path[end]) {
		end++
	}
	switch {
	case end == len(path):
		return Segment{}, 0, invalid(path, open, "unclosed '['")
	case path[end] != ']':
		return Segment{}, 0, invalid(path, end, "array index is not ASCII digits")
	case end == open+1:
		return Segment{}, 0, invalid(path, open, "empty array index")
	}

	// The digits-only scan above runs first because strconv.Atoi would also
	// accept a sign ("+1", "-1").
	index, err := strconv.Atoi(path[open+1 : end])
	if err != nil {
		return Segment{}, 0, invalid(path, open+1, "array index out of range")
	}
	return Segment{Kind: KindIndex, Index: index}, end + 1, nil
}

func isDelimiter(c byte) bool {
	return c == '.' || c == '[' || c == ']'
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// CutWildcard splits a versions path at a leading "[*]", the wildcard that
// applies the rest of the path to every item of an array. It reports whether
// the wildcard is present and returns the path each item is read with: "" for
// the item itself, or a path Parse accepts. After "[*]" the rest is either
// nothing, an index ("[*][0]"), or a '.' followed by a non-empty path. A path
// without the wildcard is checked by Parse and returned unchanged.
func CutWildcard(path string) (rest string, wildcard bool, err error) {
	rest, wildcard = strings.CutPrefix(path, "[*]")
	if !wildcard {
		if _, err := Parse(path); err != nil {
			return "", false, err
		}
		return path, false, nil
	}
	switch {
	case rest == "":
		return "", true, nil
	case rest[0] == '.':
		if len(rest) == 1 {
			return "", true, invalid(path, len(path), "no path after the '.' that follows [*]")
		}
		rest = rest[1:]
	case rest[0] != '[':
		return "", true, invalid(path, len("[*]"), "missing '.' or '[' after [*]")
	}
	if _, err := Parse(rest); err != nil {
		return "", true, fmt.Errorf("%w: after [*] in %q", err, path)
	}
	return rest, true, nil
}

// invalid builds the rejection of path, naming the byte offset where parsing
// stopped.
func invalid(path string, offset int, reason string) error {
	return fmt.Errorf("%w: %s at offset %d in %q", ErrInvalidJSONPath, reason, offset, path)
}
