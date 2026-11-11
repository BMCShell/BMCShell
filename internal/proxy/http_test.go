package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPTransportFailsClosedWithConfiguredProxy(t *testing.T) {
	var direct atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		direct.Add(1)
	}))
	defer origin.Close()

	rejecting, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer rejecting.Close()
	go func() {
		conn, err := rejecting.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	t.Setenv("NO_PROXY", "*")
	transport := HTTPTransport(rejecting.Addr().String())
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get(origin.URL)
	if err == nil {
		response.Body.Close()
		t.Fatal("failed SOCKS5 proxy should fail the request")
	}
	if direct.Load() != 0 {
		t.Fatal("request bypassed the SOCKS5 proxy")
	}
}
