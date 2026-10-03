-- 0038 — what the academy's entry form actually collects.
--
-- Three columns, from the form the academy runs on Google today.
--
-- terms_accepted_at: the entry form states five numbered conditions — no
-- refunds, the organiser may change the schedule, the organiser is not liable —
-- and says that submitting is accepting them. A tick nobody records is a tick
-- that was never asked for, and "did this person agree not to be refunded" is
-- exactly the question somebody asks three weeks later. The timestamp rather
-- than a boolean, because *when* is the whole value of the record.
--
-- participant_age: the form asks for an age, not a date of birth. Both are kept
-- rather than one derived from the other: the age is what the entrant claimed
-- on the day, the date of birth is what their ID card says, and the interesting
-- case for an age-limited group is precisely when the two disagree.
--
-- nickname: printed on the pairing card and called across the hall. Every Thai
-- junior event uses one and the console had nowhere to put it.
ALTER TABLE tournament_registration ADD COLUMN terms_accepted_at TEXT;
ALTER TABLE tournament_registration ADD COLUMN participant_age INTEGER;
ALTER TABLE tournament_registration ADD COLUMN nickname TEXT NOT NULL DEFAULT '';

-- No column for the ID card image, and that is the decision rather than an
-- omission. The card is read for a name and a date of birth, the values are
-- kept, and the bytes are dropped with the request. A store of children's
-- identity documents is a thing to leak and a thing somebody would later have
-- to be asked to delete.
