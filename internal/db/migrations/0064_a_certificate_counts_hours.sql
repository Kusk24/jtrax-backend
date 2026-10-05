-- 0064_a_certificate_counts_hours.sql — the certificate milestone counts
-- hours of class, not classes.
--
-- A non-formal school's course is set in hours (OPEC: "ใช้เวลาเรียนตลอด
-- หลักสูตร … ชั่วโมง"), so the academy's milestone is too: 50 hours of class,
-- whatever their lengths. The number carries over from the old key;
-- certificate_sessions stays for apps that still read it.

INSERT OR IGNORE INTO system_configuration (config_key, config_value)
SELECT 'certificate_hours',
       COALESCE((SELECT config_value FROM system_configuration WHERE config_key = 'certificate_sessions'), '50');
