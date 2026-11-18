package i18n

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// moMagic is the first word of a GNU .mo file, read in the file's own byte
// order: it is how a reader tells little- from big-endian files apart.
const moMagic = 0x950412de

// moCatalog is a decoded .mo file.
type moCatalog struct {
	msgs     map[string]string   // msgid -> msgstr; a plural entry's first form too
	plurals  map[string][]string // singular msgid -> every plural form
	nplurals int
	rule     pluralRule
}

// parseMO decodes a GNU .mo catalog. The header entry (empty msgid) supplies
// the plural rule; entries with a context (msgctxt, stored as
// "ctxt\x04msgid") are skipped, as bentoolkit uses none. A plural entry
// ("singular\x00plural" -> "form0\x00form1...") is keyed by its singular.
func parseMO(data []byte) (*moCatalog, error) {
	if len(data) < 28 {
		return nil, errors.New("file too short for a .mo header")
	}
	var order binary.ByteOrder
	switch {
	case binary.LittleEndian.Uint32(data) == moMagic:
		order = binary.LittleEndian
	case binary.BigEndian.Uint32(data) == moMagic:
		order = binary.BigEndian
	default:
		return nil, errors.New("not a .mo file (bad magic number)")
	}
	if rev := order.Uint32(data[4:]); rev>>16 > 1 {
		return nil, fmt.Errorf("unsupported .mo major revision %d", rev>>16)
	}
	n := order.Uint32(data[8:])
	origTab := order.Uint32(data[12:])
	transTab := order.Uint32(data[16:])
	// Each entry takes 16 bytes of descriptor tables, so a count above this
	// is a corrupt file; checked before the count sizes an allocation.
	if uint64(n) > uint64(len(data))/16 {
		return nil, fmt.Errorf("entry count %d exceeds the file size", n)
	}

	str := func(table, i uint32) (string, error) {
		off := uint64(table) + uint64(i)*8
		if off+8 > uint64(len(data)) {
			return "", fmt.Errorf("string table entry %d out of range", i)
		}
		length := uint64(order.Uint32(data[off:]))
		start := uint64(order.Uint32(data[off+4:]))
		if start+length > uint64(len(data)) {
			return "", fmt.Errorf("string %d out of range", i)
		}
		return string(data[start : start+length]), nil
	}

	c := &moCatalog{msgs: make(map[string]string, n), plurals: map[string][]string{}, nplurals: 2, rule: germanic}
	for i := range n {
		id, err := str(origTab, i)
		if err != nil {
			return nil, err
		}
		tr, err := str(transTab, i)
		if err != nil {
			return nil, err
		}
		if id == "" {
			np, rule, err := parsePluralForms(tr)
			if err != nil {
				return nil, err
			}
			c.nplurals, c.rule = np, rule
			continue
		}
		if strings.Contains(id, "\x04") {
			continue
		}
		singular, _, isPlural := strings.Cut(id, "\x00")
		forms := strings.Split(tr, "\x00")
		if isPlural {
			c.plurals[singular] = forms
		}
		c.msgs[singular] = forms[0]
	}
	return c, nil
}
