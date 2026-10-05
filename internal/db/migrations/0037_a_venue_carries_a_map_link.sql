-- 0037_a_venue_carries_a_map_link.sql — a tournament's venue now carries a
-- link to where it actually is, not just a name and an address a parent has
-- to paste into Maps themselves.
--
-- No geocoding, no Places autocomplete, no API key: the admin types the venue
-- name as free text, same as venue_name always was, and the console builds a
-- plain Google Maps search URL from it (https://www.google.com/maps/search/?
-- query=...) at creation time. That URL is what gets stored here — this
-- column is a link, not a coordinate, and nothing here validates that the
-- venue name it was built from resolves to anywhere in particular.

ALTER TABLE tournament ADD COLUMN venue_map_url TEXT;
