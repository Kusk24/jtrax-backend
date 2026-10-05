-- 0065_the_academy_contact_starts_from_the_website.sql — the academy's
-- contact details start as its website (jcachess.ac.th) gives them, so the
-- office edits them in Settings → Academy Contact rather than typing them
-- from scratch. A value already saved is kept.

INSERT OR IGNORE INTO system_configuration (config_key, config_value) VALUES
    ('academy_phone', '02-853-9836 / 099-0156-156'),
    ('academy_email', 'jcachess@gmail.com'),
    ('academy_line_id', 'https://lin.ee/7fhq3N1'),
    ('academy_facebook', 'https://www.facebook.com/jcasmartofficial'),
    ('academy_instagram', 'https://www.instagram.com/jcasmartofficial'),
    ('academy_website', 'https://jcachess.ac.th'),
    ('academy_address', 'Room As023, As024, 4th Floor, Paradise Park Mall, No. 61 Srinakarin Road, Nong Bon Subdistrict, Prawet District, Bangkok 10250');
