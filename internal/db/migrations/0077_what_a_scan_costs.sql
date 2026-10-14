-- 0077_what_a_scan_costs.sql — the tokens each call to the scan service used.
--
-- One row per call Google answered: a scan of a form or an ID card, or a test
-- from Settings. A call Google refused is not billed and is not kept. Settings
-- prices the rows with ocr's price table to show what scanning has cost this
-- month, and what a scan costs on each model.
--
-- Nothing read off a document is kept here: the model, the kind of call and
-- the token counts only.
CREATE TABLE ocr_usage (
    usage_id       TEXT PRIMARY KEY,
    model          TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('form', 'id_card', 'test')),
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    -- Output includes the model's thinking, which Google bills as output.
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_ocr_usage_created ON ocr_usage(created_at);
