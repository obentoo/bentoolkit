// Package i18ntest builds GNU .mo catalogs for tests, so a test can load
// translations without msgfmt on the host. Only tests import it.
package i18ntest

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Order is a byte order that can also append: binary.LittleEndian or
// binary.BigEndian.
type Order interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// EncodeMO returns a .mo file holding msgs (msgid -> msgstr) in the given
// byte order, with the empty-msgid header entry msgfmt always writes.
func EncodeMO(msgs map[string]string, order Order) []byte {
	all := map[string]string{"": "Content-Type: text/plain; charset=UTF-8\n"}
	for k, v := range msgs {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	slices.Sort(keys) // msgfmt sorts by msgid; readers may rely on it

	const headerSize = 28
	n := uint32(len(keys)) //nolint:gosec // G115: a test catalog holds a handful of messages, far below 2^32.
	origTab := uint32(headerSize)
	transTab := origTab + 8*n
	strStart := transTab + 8*n

	var strs []byte
	origDesc := make([]uint32, 0, 2*n)
	transDesc := make([]uint32, 0, 2*n)
	for _, k := range keys {
		origDesc = append(origDesc, uint32(len(k)), strStart+uint32(len(strs))) //nolint:gosec // G115: test strings are tiny.
		strs = append(strs, k...)
		strs = append(strs, 0)
	}
	for _, k := range keys {
		v := all[k]
		transDesc = append(transDesc, uint32(len(v)), strStart+uint32(len(strs))) //nolint:gosec // G115: test strings are tiny.
		strs = append(strs, v...)
		strs = append(strs, 0)
	}

	out := make([]byte, 0, int(strStart)+len(strs))
	for _, w := range []uint32{0x950412de, 0, n, origTab, transTab, 0, 0} {
		out = order.AppendUint32(out, w)
	}
	for _, w := range origDesc {
		out = order.AppendUint32(out, w)
	}
	for _, w := range transDesc {
		out = order.AppendUint32(out, w)
	}
	return append(out, strs...)
}

// WriteCatalog writes msgs as <dir>/<lang>/LC_MESSAGES/<domain>.mo and
// returns dir, ready to be passed as BENTOO_LOCALEDIR.
func WriteCatalog(t testing.TB, dir, lang, domain string, msgs map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, lang, "LC_MESSAGES")
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatalf("creating %s: %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(p, domain+".mo"), EncodeMO(msgs, binary.LittleEndian), 0o600); err != nil {
		t.Fatalf("writing catalog: %v", err)
	}
	return dir
}
