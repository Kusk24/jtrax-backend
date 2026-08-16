package api_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stream opens an SSE connection and returns a channel of decoded room events.
// The reader lives until the test ends, which closes the request context and
// unwinds the handler's select loop.
func (tb *table) stream(c *client, path string) (<-chan map[string]any, int) {
	tb.t.Helper()
	req, _ := http.NewRequest("GET", tb.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tb.t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, resp.StatusCode
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		tb.t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	events := make(chan map[string]any, 16)
	tb.t.Cleanup(func() { resp.Body.Close() })
	go func() {
		defer close(events)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue // event: / comment / blank
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err == nil {
				events <- ev
			}
		}
	}()
	return events, 200
}

func next(t *testing.T, events <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("stream closed early")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
		return nil
	}
}

// The whole point of the stream: the opponent's board updates without being
// asked. If this needed a poll, the feature would not feel live.
func TestAMoveReachesTheOpponentsStream(t *testing.T) {
	tb := openRoom(t).bothSeats()

	events, status := tb.stream(tb.black, "/api/v1/game-rooms/"+tb.id+"/events")
	if status != 200 {
		t.Fatalf("open stream: status %d", status)
	}
	// A stream opens with the current state, so a client that connects
	// mid-game is correct without a separate fetch.
	first := next(t, events)
	if first["status"] != "Active" || first["ply"].(float64) != 0 {
		t.Fatalf("opening snapshot is wrong: %v", first)
	}

	if code, obj := tb.move(tb.white, "e2e4"); code != 200 {
		t.Fatalf("move: %d (%v)", code, obj)
	}
	ev := next(t, events)
	if ev["lastSan"] != "e4" {
		t.Errorf("lastSan = %v, want e4", ev["lastSan"])
	}
	if ev["turn"] != "Black" {
		t.Errorf("turn = %v, want Black", ev["turn"])
	}
	if ev["ply"].(float64) != 1 {
		t.Errorf("ply = %v, want 1", ev["ply"])
	}
	if ev["finished"] != false {
		t.Errorf("finished = %v, want false", ev["finished"])
	}
}

func TestTheStreamReportsTheEndOfTheGame(t *testing.T) {
	tb := openRoom(t).bothSeats()
	events, _ := tb.stream(tb.admin, "/api/v1/game-rooms/"+tb.id+"/events")
	next(t, events) // opening snapshot

	for i, uci := range foolsMate {
		mover := tb.white
		if i%2 == 1 {
			mover = tb.black
		}
		if code, obj := tb.move(mover, uci); code != 200 {
			t.Fatalf("move %s: %d (%v)", uci, code, obj)
		}
	}
	var last map[string]any
	for i := 0; i < len(foolsMate); i++ {
		last = next(t, events)
	}
	if last["result"] != "0-1" || last["resultReason"] != "Checkmate" {
		t.Fatalf("final event = %v, want a checkmate result", last)
	}
	if last["finished"] != true {
		t.Errorf("finished = %v, want true", last["finished"])
	}
}

func TestJoiningIsAnnouncedToThePlayerAlreadyWaiting(t *testing.T) {
	tb := openRoom(t)
	penny, _ := tb.seat("penny@jca.ac.th")

	events, status := tb.stream(penny, "/api/v1/game-rooms/"+tb.id+"/events")
	if status != 200 {
		t.Fatalf("open stream: status %d", status)
	}
	first := next(t, events)
	if first["status"] != "Open" {
		t.Fatalf("status = %v, want Open before the second player arrives", first["status"])
	}

	tb.seat("uri@jca.ac.th")
	ev := next(t, events)
	if ev["status"] != "Active" {
		t.Errorf("status = %v, want Active once both seats are filled", ev["status"])
	}
	if ev["black"] == nil {
		t.Errorf("the arriving player is missing from the event: %v", ev)
	}
}

// A stream is a read, and reads are gated the same way everywhere: a room you
// are not in is indistinguishable from a room that does not exist.
func TestTheStreamIsClosedToOnlookers(t *testing.T) {
	tb := openRoom(t).bothSeats()
	outsider := &client{t: t, srv: tb.srv}
	outsider.login("serene@jca.ac.th")
	if _, status := tb.stream(outsider, "/api/v1/game-rooms/"+tb.id+"/events"); status != 404 {
		t.Fatalf("outsider opened the stream: status %d, want 404", status)
	}

	anon := &client{t: t, srv: tb.srv}
	if _, status := tb.stream(anon, "/api/v1/game-rooms/"+tb.id+"/events"); status != 401 {
		t.Fatalf("anonymous opened the stream: status %d, want 401", status)
	}
}

// A room code is a credential. The stream reaches both players and every staff
// watcher, so it must not carry one even though its subscribers are authorized.
func TestEventsNeverCarryTheRoomCode(t *testing.T) {
	tb := openRoom(t).bothSeats()
	events, _ := tb.stream(tb.admin, "/api/v1/game-rooms/"+tb.id+"/events")

	first := next(t, events)
	tb.move(tb.white, "e2e4")
	second := next(t, events)

	for _, ev := range []map[string]any{first, second} {
		if _, ok := ev["code"]; ok {
			t.Fatalf("event carries the room code: %v", ev)
		}
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), tb.code) {
			t.Fatalf("event body contains the code %q: %s", tb.code, raw)
		}
	}
}
