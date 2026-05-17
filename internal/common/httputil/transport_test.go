package httputil

import (
	"testing"
	"time"
)

// TestBuildTransport_DefaultsTuned verifies that BuildTransport returns a
// transport configured with every tuned field at its expected default value
// when HTTP/2 is not disabled.
func TestBuildTransport_DefaultsTuned(t *testing.T) {
	tr := BuildTransport()
	if tr == nil {
		t.Fatal("BuildTransport() returned nil")
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"MaxIdleConnsPerHost", tr.MaxIdleConnsPerHost, 16},
		{"MaxConnsPerHost", tr.MaxConnsPerHost, 32},
		{"IdleConnTimeout", tr.IdleConnTimeout, 90 * time.Second},
		{"TLSHandshakeTimeout", tr.TLSHandshakeTimeout, 10 * time.Second},
		{"ExpectContinueTimeout", tr.ExpectContinueTimeout, 1 * time.Second},
		{"ForceAttemptHTTP2", tr.ForceAttemptHTTP2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}

	// With HTTP/2 enabled, TLSNextProto must be left at its zero value (nil)
	// so the standard library performs automatic HTTP/2 negotiation.
	if tr.TLSNextProto != nil {
		t.Errorf("TLSNextProto = %v, want nil when HTTP/2 is enabled", tr.TLSNextProto)
	}
}

// TestBuildTransport_HTTP2Disabled verifies that setting EnvDisableHTTP2 to "1"
// causes BuildTransport to disable HTTP/2 negotiation.
func TestBuildTransport_HTTP2Disabled(t *testing.T) {
	t.Setenv(EnvDisableHTTP2, "1")

	tr := BuildTransport()
	if tr == nil {
		t.Fatal("BuildTransport() returned nil")
	}

	if tr.ForceAttemptHTTP2 {
		t.Errorf("ForceAttemptHTTP2 = true, want false when %s=1", EnvDisableHTTP2)
	}

	if tr.TLSNextProto == nil {
		t.Fatalf("TLSNextProto = nil, want non-nil empty map when %s=1", EnvDisableHTTP2)
	}
	if len(tr.TLSNextProto) != 0 {
		t.Errorf("len(TLSNextProto) = %d, want 0", len(tr.TLSNextProto))
	}
}

// TestMaxBodyBytes_Value verifies that the MaxBodyBytes constant equals 10 MiB.
func TestMaxBodyBytes_Value(t *testing.T) {
	const want int64 = 10485760 // 10 * 1024 * 1024
	if MaxBodyBytes != want {
		t.Errorf("MaxBodyBytes = %d, want %d", MaxBodyBytes, want)
	}
}
