-- An age group does not have to be its own chess-results event.
--
-- 0037 added a link per category, because a chessfest publishes OPEN, U18,
-- U12, U10 and U08 as five separate tournaments with five separate tnr
-- numbers. That is one of the two ways Swiss-Manager uploads an age-group
-- event, and it is the only one the console understood.
--
-- The other way is one tournament holding every group, with each player's
-- group named in the ranking table's "Typ" column — which is how
-- tnr1193905, "WCIB CHESS CHAMPIONSHIP 2025 [U14 + G14]", is published.
-- There is one link to give, so linking per category cannot divide it, and
-- the console showed twenty children in one undivided list.
--
-- The column was being parsed and dropped. Keeping it is what lets the
-- Results tab build its own tabs from a single link.
--
-- Empty on the events that do not use it, which is most of them: a plain
-- open tournament has no groups to name.
ALTER TABLE external_standing ADD COLUMN player_type TEXT NOT NULL DEFAULT '';

-- Answering "which groups are in this event?" is a scan of every stored row
-- otherwise, and the Results tab asks it on each load to build its tabs.
CREATE INDEX idx_external_standing_type
    ON external_standing(external_tournament_id, player_type);
