package main

import (
	"net/http"
	"testing"
	"time"
)

// TestNewServerTimeouts guards docs/plans/213-timeouts.md Decision 1: catches an accidental
// revert or copy-paste drop of a connection-level timeout field.
func TestNewServerTimeouts(t *testing.T) {
	srv := newServer(":3000", http.NewServeMux())

	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, 5*time.Second)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", srv.IdleTimeout, 120*time.Second)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes = %v, want %v", srv.MaxHeaderBytes, 1<<20)
	}
}
