// Move submission, game outcomes, and staff lifecycle operations.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Kusk24/jtrax-backend/internal/game"
	"github.com/Kusk24/jtrax-backend/internal/httpx"
)

// handleMove grades and records one half-move.
func handleMove(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		room, err := loadRoom(d, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		mySeat := room.seatOf(id.UserAccountID)
		if mySeat == "" {
			// Staff can watch a game; nobody may play someone else's side.
			httpx.Error(w, http.StatusForbidden, "you are not playing in this game", nil)
			return
		}
		if room.Status != "Active" {
			httpx.Error(w, http.StatusConflict, "that game is not in play", nil)
			return
		}
		var in struct {
			Move string `json:"move"`
		}
		if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Move) == "" {
			httpx.Error(w, http.StatusBadRequest, "a move is required", err)
			return
		}
		if len(in.Move) > 6 {
			httpx.Error(w, http.StatusBadRequest, "that is not a move", nil)
			return
		}
		moves, ucis, err := roomMoves(d, room.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		// Whose turn it is comes from the position, not from the move count, so
		// there is one source of truth for turn order.
		before, err := game.Describe(ucis)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not replay game", err)
			return
		}
		if before.Turn != mySeat {
			httpx.Error(w, http.StatusConflict, "it is not your turn", nil)
			return
		}
		applied, err := game.Apply(ucis, in.Move)
		if err != nil {
			if errors.Is(err, game.ErrIllegalMove) {
				httpx.Error(w, http.StatusBadRequest, "that move is not legal here", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not apply move", err)
			return
		}

		ply := len(moves) + 1
		// The (room, ply) primary key is the referee for a race: if the same
		// player double-submits, or a stale client replays, the second insert
		// fails here instead of appending a second move to one turn.
		if _, err := d.Exec(`INSERT INTO game_move (game_room_id, ply, san, uci, fen_after)
		                     VALUES (?, ?, ?, ?, ?)`,
			room.ID, ply, applied.SAN, applied.UCI, applied.FEN); err != nil {
			httpx.Error(w, http.StatusConflict, "the position has already moved on", err)
			return
		}
		// A move answers any draw offer standing: offering and then playing on
		// withdraws it, and replying with a move declines it.
		if applied.Result != "" {
			_, err = d.Exec(`UPDATE game_room SET fen = ?, status = 'Finished', draw_offer = NULL,
			                 result = ?, result_reason = ?, ended_at = datetime('now')
			                 WHERE game_room_id = ?`,
				applied.FEN, applied.Result, applied.Reason, room.ID)
		} else {
			_, err = d.Exec(`UPDATE game_room SET fen = ?, draw_offer = NULL WHERE game_room_id = ?`,
				applied.FEN, room.ID)
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "move stored but room not updated", err)
			return
		}
		publishRoom(d, h, room.ID)
		// Forwarded after the move is stored and published, never before: the
		// pupil's own piece must not wait on a round trip to lichess.org.
		relay.forward(room.ID, mySeat, applied.UCI)

		after, err := game.Describe(append(ucis, applied.UCI))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not replay game", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"move": moveView{Ply: ply, SAN: applied.SAN, UCI: applied.UCI, FENAfter: applied.FEN},
			"fen":  applied.FEN, "turn": after.Turn, "check": applied.Check,
			"result": applied.Result, "resultReason": applied.Reason,
			"legalMoves": after.Legal,
		})
	}
}

// handleResign ends a game in the opponent's favour. The resigning colour comes
// from the seat the caller holds, so nobody can resign on another player's
// behalf by naming a colour in the body.
func handleResign(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		room, err := loadRoom(d, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		mySeat := room.seatOf(id.UserAccountID)
		if mySeat == "" {
			httpx.Error(w, http.StatusForbidden, "you are not playing in this game", nil)
			return
		}
		if room.Status != "Active" {
			httpx.Error(w, http.StatusConflict, "that game is not in play", nil)
			return
		}
		_, ucis, err := roomMoves(d, room.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "query failed", err)
			return
		}
		result, reason, err := game.Resign(ucis, mySeat)
		if err != nil {
			httpx.Error(w, http.StatusConflict, "that game is not in play", err)
			return
		}
		if _, err := d.Exec(`UPDATE game_room SET status = 'Finished', result = ?,
		                     result_reason = ?, ended_at = datetime('now')
		                     WHERE game_room_id = ? AND status = 'Active'`,
			result, reason, room.ID); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record the resignation", err)
			return
		}
		publishRoom(d, h, room.ID)
		// Resigning here has to resign there too. Without this the Lichess game
		// would run on until it timed out, and the rating would move minutes
		// later for a reason neither pupil would recognise.
		relay.resign(room.ID, mySeat)
		httpx.JSON(w, http.StatusOK, map[string]any{"result": result, "resultReason": reason})
	}
}

// handleDraw offers, accepts or declines a draw.
//
// One seat offers; only the other may accept or decline, and only while the
// offer stands. Accepting ends the game drawn "by agreement". Each step is a
// conditional UPDATE, so an offer withdrawn by a move cannot be accepted a
// moment later by a stale screen.
func handleDraw(d *sql.DB, h *hub, relay *lichessRelay, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		room, err := loadRoom(d, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "not found", nil)
			return
		}
		mySeat := room.seatOf(id.UserAccountID)
		if mySeat == "" {
			httpx.Error(w, http.StatusForbidden, "you are not playing in this game", nil)
			return
		}
		opponent := "Black"
		if mySeat == "Black" {
			opponent = "White"
		}
		var res sql.Result
		switch action {
		case "offer":
			res, err = d.Exec(`UPDATE game_room SET draw_offer = ?
			                   WHERE game_room_id = ? AND status = 'Active' AND draw_offer IS NULL`,
				mySeat, room.ID)
		case "accept":
			res, err = d.Exec(`UPDATE game_room SET status = 'Finished', result = '1/2-1/2',
			                   result_reason = 'Agreement', draw_offer = NULL, ended_at = datetime('now')
			                   WHERE game_room_id = ? AND status = 'Active' AND draw_offer = ?`,
				room.ID, opponent)
		case "decline":
			res, err = d.Exec(`UPDATE game_room SET draw_offer = NULL
			                   WHERE game_room_id = ? AND status = 'Active' AND draw_offer = ?`,
				room.ID, opponent)
		}
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not record that", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			msg := "there is no draw offer to answer"
			if action == "offer" {
				msg = "a draw cannot be offered now"
			}
			httpx.Error(w, http.StatusConflict, msg, nil)
			return
		}
		publishRoom(d, h, room.ID)
		// A drawn board here has to be drawn on Lichess too, or the rated game
		// runs on until somebody's clock falls.
		if action == "accept" {
			relay.draw(room.ID, opponent)
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": action})
	}
}

// handleStopRoom pauses a game in play so it can be finished another day.
//
// The game goes back to Open with every move kept; nobody can move until the
// office resumes it (handleResumeRoom) — the players cannot resume it
// themselves. Staff only, and only a game in play: a waiting one has nothing
// to hold and is removed instead.
//
// A rated game cannot be paused on Lichess, whose clock keeps running, so
// stopping one ends the Lichess side and the game carries on here unrated,
// saying why. The console warns before it does this.
func handleStopRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		roomID := r.PathValue("id")
		var rated int
		if err := d.QueryRow(`SELECT lichess_rated FROM game_room WHERE game_room_id = ? AND status = 'Active'`,
			roomID).Scan(&rated); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				httpx.Error(w, http.StatusConflict, "only a game in play can be stopped", nil)
				return
			}
			httpx.Error(w, http.StatusInternalServerError, "could not stop the game", err)
			return
		}
		res, err := d.Exec(`UPDATE game_room
		                    SET status = 'Open', stopped_at = datetime('now'), draw_offer = NULL
		                    WHERE game_room_id = ? AND status = 'Active'`, roomID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not stop the game", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusConflict, "only a game in play can be stopped", nil)
			return
		}
		if rated == 1 {
			relay.abandon(roomID)
			if _, err := d.Exec(`UPDATE game_room SET lichess_rated = 0, lichess_detached_reason = 'stopped'
			                     WHERE game_room_id = ?`, roomID); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "stopped, but could not record it as unrated", err)
				return
			}
		}
		publishRoom(d, h, roomID)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "stopped"})
	}
}

// handleResumeRoom lets a paused game carry on, from where it stopped.
// Staff only: the office decides when the two may finish it. It does not put
// the game straight back in play — the two may not be at the board yet — but
// back to waiting for both to press Enter, the way a game the office sets up
// starts. Moves, and the day it began, are kept.
func handleResumeRoom(d *sql.DB, h *hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		roomID := r.PathValue("id")
		res, err := d.Exec(`UPDATE game_room
		                    SET stopped_at = NULL, white_entered_at = NULL, black_entered_at = NULL
		                    WHERE game_room_id = ? AND status = 'Open' AND stopped_at IS NOT NULL`, roomID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not resume the game", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusConflict, "only a paused game can be resumed", nil)
			return
		}
		publishRoom(d, h, roomID)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "resumed"})
	}
}

// handleCancelRoom pulls a room. Staff only, and it never deletes: a played
// game is a record the academy keeps, so cancelling marks it and stops play.
func handleCancelRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		res, err := d.Exec(`UPDATE game_room SET status = 'Cancelled', ended_at = datetime('now')
		                    WHERE game_room_id = ? AND status IN ('Open','Active')`, r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not cancel room", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			httpx.Error(w, http.StatusNotFound, "no room to cancel", nil)
			return
		}
		// Otherwise the Lichess game runs on with nobody watching it and this
		// server keeps a stream goroutine open for a board that is gone.
		relay.abandon(r.PathValue("id"))
		publishRoom(d, h, r.PathValue("id"))
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	}
}

// handleDeleteRoom removes a room and its moves for good.
//
// Cancelling and deleting are different acts and both are wanted. Cancelling
// ends a game that is happening; the row stays, because a game that was played
// is a record. Deleting throws the record away, which is what the office needs
// for the rooms that are not records at all — a code minted for a lesson that
// did not happen, a board opened twice by mistake, the three test rooms from
// the afternoon somebody was learning the screen. Those accumulate at the top
// of the list forever and there was no way to be rid of one.
//
// So they are separate endpoints rather than one DELETE that changed meaning.
// An older console still sends DELETE for "Stop this game", and having that
// destroy a game mid-deploy is exactly the accident worth designing out.
//
// A game being played is refused. The two players are mid-move, and the fix
// for a game that should not be running is to stop it — which is the other
// endpoint, and reversible in a way this is not.
func handleDeleteRoom(d *sql.DB, h *hub, relay *lichessRelay) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := requireIdentity(d, w, r)
		if id == nil {
			return
		}
		if !isStaff(id.Role) {
			httpx.Error(w, http.StatusForbidden, "not allowed", nil)
			return
		}
		roomID := r.PathValue("id")

		var status string
		switch err := d.QueryRow(`SELECT status FROM game_room WHERE game_room_id = ?`,
			roomID).Scan(&status); {
		case errors.Is(err, sql.ErrNoRows):
			httpx.Error(w, http.StatusNotFound, "no such room", nil)
			return
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "could not read room", err)
			return
		}
		if status == "Active" {
			httpx.Error(w, http.StatusConflict, "stop the game before deleting it", nil)
			return
		}

		gone, _ := loadRoom(d, roomID)
		tx, err := d.Begin()
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete room", err)
			return
		}
		defer tx.Rollback()

		// game_move.game_room_id is NOT NULL, so the moves go with the room
		// rather than being orphaned. A challenge's link is nullable and the
		// challenge itself is a separate record of who asked whom — that one is
		// unhooked, not destroyed.
		for _, step := range []struct {
			q    string
			args []any
		}{
			{`DELETE FROM game_move WHERE game_room_id = ?`, []any{roomID}},
			{`UPDATE game_challenge SET game_room_id = NULL WHERE game_room_id = ?`, []any{roomID}},
			{`DELETE FROM game_room WHERE game_room_id = ?`, []any{roomID}},
		} {
			if _, err := tx.Exec(step.q, step.args...); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "could not delete room", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not delete room", err)
			return
		}

		// An Open room can still hold a relay slot if it was minted rated, and
		// the watcher would outlive the row it is watching.
		relay.abandon(roomID)
		// Anyone with the board open learns it is over, rather than being left
		// looking at a position that no longer exists anywhere.
		// The last position goes with it, so the board stays drawn.
		if gone != nil {
			ev := roomEvent{RoomID: roomID, Status: "Cancelled", FEN: gone.FEN, Ply: gone.MoveCount,
				White: gone.White, Black: gone.Black, Finished: true, Removed: true}
			if payload, err := json.Marshal(ev); err == nil {
				h.broadcast(roomID, payload)
			}
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}
