package fileutil

import (
	"os"
	"testing"
)

// TestCacheFileMode_IsRestrictive asserts the shared cache file mode is
// owner-only read/write (0600).
func TestCacheFileMode_IsRestrictive(t *testing.T) {
	if CacheFileMode != 0600 {
		t.Errorf("CacheFileMode = %#o, want %#o", CacheFileMode, os.FileMode(0600))
	}
}
