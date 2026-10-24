package main

import (
	"net/http"
	"testing"
	"time"
)

func TestServerBoundsSlowRequestsWithoutSSEWriteDeadline(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 15*time.Second || srv.IdleTimeout != 60*time.Second || srv.MaxHeaderBytes != 1<<20 {
		t.Fatalf("unsafe timeout config: %+v", srv)
	}
	if srv.WriteTimeout != 0 {
		t.Fatal("SSE must not have an absolute write deadline")
	}
}
