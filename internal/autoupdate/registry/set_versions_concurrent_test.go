package registry

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// ApplyAll promotes packages concurrently, and each promotion pins its record
// with its own SetPackageVersions call. Every one of those calls must land: a
// read-modify-write that is not serialised lets the last writer drop the pins
// of the others, which is how one of five promoted records kept its old
// version.
func TestSetPackageVersions_ConcurrentCallsKeepEveryPin(t *testing.T) {
	const n = 24
	var content strings.Builder
	for i := range n {
		fmt.Fprintf(&content, "[\"dev-test/p%02d\"]\nurl = \"https://x/y\"\nparser = \"json\"\npath = \"v\"\nversion = \"1.0.0\"\n# END\n\n", i)
	}
	overlay, configPath := writePackagesTOML(t, content.String())

	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- SetPackageVersions(overlay, map[string]string{fmt.Sprintf("dev-test/p%02d", i): "2.0.0"})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SetPackageVersions: %v", err)
		}
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `version = "2.0.0"`); got != n {
		t.Errorf("%d of %d concurrent pins landed in packages.toml; the others were overwritten", got, n)
	}
}
