package main

/* The server's own limits. A client that never finishes its headers has to be
   cut off; a live event stream — the game board, the LINE inbox — must not be,
   however long it runs. Both are exercised over a real socket, with the header
   timeout shortened so the test does not take ten seconds. */

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, handler http.Handler, headerTimeout time.Duration) string {
	t.Helper()
	srv := newHTTPServer("", handler)
	srv.ReadHeaderTimeout = headerTimeout
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return l.Addr().String()
}

func TestTheServerHasLimitsButNoneThatCutStreams(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != readHeaderTimeout || srv.ReadHeaderTimeout <= 0 {
		t.Fatalf("header timeout = %v, want %v", srv.ReadHeaderTimeout, readHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Fatal("idle keep-alive connections are never closed")
	}
	// A whole-request deadline would end the game board's stream mid-game.
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatalf("ReadTimeout %v / WriteTimeout %v would cut event streams off", srv.ReadTimeout, srv.WriteTimeout)
	}
}

func TestAClientThatDribblesItsHeadersIsCutOff(t *testing.T) {
	addr := serve(t, http.NotFoundHandler(), 300*time.Millisecond)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A request line and one header, and then nothing: the headers never end.
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: jtrax\r\n"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadAll(conn)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the server was still holding the connection open after 5s")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("the connection was closed only after %v", waited)
	}
}

func TestAnEventStreamOutlivesTheHeaderTimeout(t *testing.T) {
	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 8; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			flusher.Flush()
			time.Sleep(150 * time.Millisecond)
		}
	})
	// 8 events 150ms apart is about 1.2s: four times the header timeout.
	addr := serve(t, stream, 300*time.Millisecond)

	res, err := http.Get("http://" + addr + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	events := 0
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			events++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("the stream broke after %d events: %v", events, err)
	}
	if events != 8 {
		t.Fatalf("got %d of 8 events: the stream was cut off", events)
	}
}
