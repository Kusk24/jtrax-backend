-- One email, several players.
--
-- 0014 allowed one entry per contact email in a tournament, to stop a double-
-- tapped submit becoming two entries. It also stopped a parent entering two
-- children with the one address they have. The guard is now the player: the
-- same email, name and date of birth twice is the same child, refused; a
-- second child on the same email is a second entry.
--
-- Names are compared the way a person would: ignoring case and the spaces
-- either side. Rejected entries still do not count, as before.
DROP INDEX IF EXISTS idx_registration_one_per_email;

CREATE UNIQUE INDEX idx_registration_one_per_player
    ON tournament_registration(
        tournament_id,
        lower(contact_email),
        lower(trim(participant_name)),
        COALESCE(participant_date_of_birth, ''))
    WHERE contact_email <> '' AND status <> 'Rejected';
