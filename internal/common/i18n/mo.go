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

// parseMO decodes a GNU .mo catalog into msgid -> msgstr. The header entry
// (empty msgid) and entries with a context (msgctxt, stored as
// "ctxt\x04msgid") are skipped: bentoolkit uses neither. For a plural entry
// ("singular\x00plural") the singular is the key and the first form the value.
func parseMO(data []byte) (map[string]string, error) {
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

	m := make(map[string]string, n)
	for i := range n {
		id, err := str(origTab, i)
		if err != nil {
			return nil, err
		}
		tr, err := str(transTab, i)
		if err != nil {
			return nil, err
		}
		if id == "" || strings.Contains(id, "\x04") {
			continue
		}
		id, _, _ = strings.Cut(id, "\x00")
		tr, _, _ = strings.Cut(tr, "\x00")
		m[id] = tr
	}
	return m, nil
}
