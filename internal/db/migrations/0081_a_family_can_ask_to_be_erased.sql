-- 0081_a_family_can_ask_to_be_erased.sql — requests to erase a family's data.
--
-- PDPA s.33 gives a person the right to have their data erased. A parent asks
-- from the portal; the office sees the request, carries it out with the
-- people delete it already has (which keeps payments, detached and named, as
-- accounting law requires), and marks it done. The request outlives the
-- account it came from — it is the record that the right was honoured — so
-- it names the family rather than pointing at rows that will be gone.
CREATE TABLE data_request (
    data_request_id   TEXT PRIMARY KEY,
    kind              TEXT NOT NULL CHECK (kind IN ('erasure')),
    status            TEXT NOT NULL DEFAULT 'Open' CHECK (status IN ('Open', 'Done', 'Declined')),
    user_account_id   TEXT REFERENCES user_account(user_account_id) ON DELETE SET NULL,
    parent_id         TEXT REFERENCES parent(parent_id) ON DELETE SET NULL,
    requested_by_name TEXT NOT NULL,
    note              TEXT NOT NULL DEFAULT '',
    requested_at      TEXT NOT NULL DEFAULT (datetime('now')),
    resolved_at       TEXT,
    resolved_by       TEXT,
    resolution_note   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_data_request_status ON data_request(status);
