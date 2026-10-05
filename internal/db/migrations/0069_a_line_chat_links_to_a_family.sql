-- A LINE chat linked to the parent or student it is with.
--
-- LINE names a person only by a userId issued for this channel, learned when
-- they first message or follow; nothing in it matches a family. So the office
-- links a chat by hand, from the Messages screen, and the linked record's page
-- then opens that chat. One chat per parent and per student, so "their LINE
-- chat" is never ambiguous; a chat links to one of the two, or neither.
ALTER TABLE line_contact ADD COLUMN parent_id TEXT REFERENCES parent(parent_id);
ALTER TABLE line_contact ADD COLUMN student_id TEXT REFERENCES student(student_id);

CREATE UNIQUE INDEX idx_line_contact_parent ON line_contact(parent_id) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX idx_line_contact_student ON line_contact(student_id) WHERE student_id IS NOT NULL;
