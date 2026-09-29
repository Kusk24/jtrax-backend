-- An announcement is addressed to someone.
--
-- Every announcement used to go to every parent and every student, and every
-- account could read every one. The office now chooses: all parents, the
-- parents of one or more classes, or particular parents by name. Students are
-- no longer an audience at all — announcements are for families.
--
-- `audience` says which kind; `audience_ids` is what the office picked (class
-- ids or parent ids, a JSON array), kept so the console can show "sent to
-- Master, King Slayer" afterwards.
--
-- `announcement_recipient` is who it actually reached: the parents resolved at
-- the moment it was posted. For a class audience that is a snapshot — a family
-- joining the class next month does not inherit its old notices, and one that
-- leaves keeps the ones it was sent. An "all" announcement has no rows here;
-- every parent can read it.
ALTER TABLE announcement ADD COLUMN audience TEXT NOT NULL DEFAULT 'all'
    CHECK (audience IN ('all', 'classes', 'parents'));
ALTER TABLE announcement ADD COLUMN audience_ids TEXT NOT NULL DEFAULT '[]';

CREATE TABLE announcement_recipient (
    announcement_id TEXT NOT NULL REFERENCES announcement(announcement_id) ON DELETE CASCADE,
    parent_id       TEXT NOT NULL REFERENCES parent(parent_id) ON DELETE CASCADE,
    PRIMARY KEY (announcement_id, parent_id)
);

-- A parent's own list is "all, plus the ones addressed to me" on every load.
CREATE INDEX idx_announcement_recipient_parent ON announcement_recipient(parent_id);
