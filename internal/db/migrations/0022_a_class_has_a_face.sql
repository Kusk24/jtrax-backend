-- 0022_a_class_has_a_face.sql — the icon and badge a class is shown with.
--
-- The Academy screen has offered an icon picker and a badge field since it was
-- built, and neither has ever been saved. There was nowhere to put them: the
-- console derived both from class_type, so the picker set a value that the
-- next render immediately overwrote with `class_type == 'Master' ? trophy :
-- 'Private' ? king : queen`. Choosing the pawn and pressing Save did nothing
-- at all, which is the report — and the badge field was worse, since it looked
-- like free text but showed the class type back.
--
-- Two columns, both nullable, neither load-bearing. A class with no icon still
-- renders (the console falls back), and nothing joins on either.
--
-- Deliberately not an enum. The icon names come from the console's own icon
-- set, which is a front-end concern that changes when the design does; a CHECK
-- here would mean a migration every time somebody adds a piece to the picker,
-- and a class whose icon was retired would fail to save for reasons the office
-- could not act on. The console validates against the set it actually has.

ALTER TABLE class ADD COLUMN icon TEXT;
ALTER TABLE class ADD COLUMN badge TEXT;

-- Backfill with exactly what the console has been showing, so the deploy
-- changes nothing on screen. Every existing class keeps the icon it was drawn
-- with and the badge it displayed; the difference is that from now on both are
-- a stored choice rather than a guess re-made on every render.
UPDATE class
   SET icon = CASE class_type
                WHEN 'Master'  THEN 'trophy'
                WHEN 'Private' THEN 'king'
                ELSE                'queen'
              END
 WHERE icon IS NULL;

UPDATE class SET badge = class_type WHERE badge IS NULL;
