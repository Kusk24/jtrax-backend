-- 0051 — a connected tournament's results use chess-results' own categories.
--
-- Two different lists of categories exist for one event, and they are not the
-- same thing:
--
--   * JTrax registration categories (tournament_category) are what families
--     enter before the day. The office sets them up and they drive sign-up.
--   * Chess-Results categories are how the arbiter actually ran it. Swiss-Manager
--     publishes each section (U08 + G08, U10 + G10, …) as its own tournament
--     with its own tnr number, tied together by a "Tournament selection" row.
--
-- The arbiter's list is the truth for results: it can have a section the
-- office never registered (a late U16), or merge two the office kept apart.
-- So when a tournament is connected, its results follow this table, never the
-- registration categories, and neither list is copied into the other.
--
-- One row per section. No foreign key to tournament: a stray row for a deleted
-- tournament is harmless, and a key would stop the tournament being deleted.
CREATE TABLE tournament_result_section (
    tournament_id    TEXT NOT NULL,
    chess_results_id INTEGER NOT NULL,
    -- The organiser's short name for the section, as chess-results shows it.
    name             TEXT NOT NULL,
    -- The organiser's order, which is the order the tabs are drawn in.
    position         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tournament_id, chess_results_id)
);

-- What the connected event is called there, and how many rounds it is
-- scheduled for. The ranking only says how many have been played, so without
-- the schedule an event four rounds into seven reads as finished.
ALTER TABLE tournament ADD COLUMN chess_results_event TEXT;
ALTER TABLE tournament ADD COLUMN chess_results_rounds INTEGER;
