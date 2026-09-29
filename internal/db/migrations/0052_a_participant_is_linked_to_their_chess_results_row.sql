-- 0052 — one participant, whichever tab they are opened from.
--
-- A tournament participant is their registration. The results for them come
-- from chess-results.com, where the arbiter typed the name into Swiss-Manager,
-- so the two are joined by name: "Pavatt Uapongkitikul" on the entry form is
-- "Uapongkitikul, Pavatt" there. The console matches that on its own.
--
-- What it cannot match is a name spelled differently — a Thai name written in
-- English two ways. For those, staff pick the player from the chess-results
-- list, and the choice is kept here. Empty means "match by name".
--
-- The section is kept with the name because the same name could, in theory,
-- appear in two sections; a link to a section the event no longer has is
-- ignored rather than cleaned up.
ALTER TABLE tournament_registration ADD COLUMN results_section_id INTEGER;
ALTER TABLE tournament_registration ADD COLUMN results_player_name TEXT;
