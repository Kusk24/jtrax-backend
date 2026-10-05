-- A LINE sticker kept as the sticker it was, not as a blank "[Sticker]".
--
-- The webhook names a sticker by package and sticker id, plus how it is drawn
-- (STATIC, ANIMATION, SOUND, MESSAGE, …). LINE offers no API that returns the
-- image a user sent, so the ids are what the console draws it from: an image
-- where the academy has the sticker locally, a placeholder where it does not.
-- Null on every other kind of message, and on stickers received before this.
ALTER TABLE line_message ADD COLUMN sticker_package_id TEXT;
ALTER TABLE line_message ADD COLUMN sticker_id TEXT;
ALTER TABLE line_message ADD COLUMN sticker_resource_type TEXT;
