-- 0001_initial_schema.sql — full schema from jtrax-ermodel.pdf (2026-08-11).
-- SQLite dialect: enums become CHECK constraints, datetime/date/time are TEXT
-- (ISO 8601), decimal is stored as REAL for display-only amounts.

CREATE TABLE user_account (
    user_account_id     TEXT PRIMARY KEY,
    email               TEXT NOT NULL UNIQUE,
    password_hash       TEXT NOT NULL,
    role                TEXT NOT NULL CHECK (role IN ('Admin','Receptionist','Teacher','Parent','Student')),
    display_name        TEXT NOT NULL,
    language_preference TEXT NOT NULL DEFAULT 'EN' CHECK (language_preference IN ('EN','TH')),
    theme_preference    TEXT NOT NULL DEFAULT 'System' CHECK (theme_preference IN ('Light','Dark','System'))
);

CREATE TABLE parent (
    parent_id       TEXT PRIMARY KEY,
    user_account_id TEXT NOT NULL UNIQUE REFERENCES user_account(user_account_id),
    name            TEXT NOT NULL
);

CREATE TABLE teacher (
    teacher_id      TEXT PRIMARY KEY,
    user_account_id TEXT NOT NULL UNIQUE REFERENCES user_account(user_account_id),
    name            TEXT NOT NULL,
    phone           TEXT,
    email           TEXT,
    line_id         TEXT
);

CREATE TABLE admin (
    admin_id        TEXT PRIMARY KEY,
    user_account_id TEXT NOT NULL UNIQUE REFERENCES user_account(user_account_id),
    name            TEXT NOT NULL,
    phone           TEXT,
    email           TEXT,
    line_id         TEXT
);

CREATE TABLE student (
    student_id         TEXT PRIMARY KEY,
    user_account_id    TEXT UNIQUE REFERENCES user_account(user_account_id),
    name               TEXT NOT NULL,
    date_of_birth      TEXT,
    current_level      TEXT,
    fide_rating        REAL,
    last_attended_date TEXT,
    streak_count       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE parent_contact (
    parent_contact_id TEXT PRIMARY KEY,
    parent_id         TEXT NOT NULL REFERENCES parent(parent_id),
    contact_type      TEXT NOT NULL CHECK (contact_type IN ('phone','email','line_id')),
    value             TEXT NOT NULL
);

CREATE TABLE student_parent (
    student_id        TEXT NOT NULL REFERENCES student(student_id),
    parent_id         TEXT NOT NULL REFERENCES parent(parent_id),
    relationship_type TEXT,
    PRIMARY KEY (student_id, parent_id)
);

CREATE TABLE notification_preference (
    parent_id                    TEXT PRIMARY KEY REFERENCES parent(parent_id),
    check_in_alerts_enabled      INTEGER NOT NULL DEFAULT 1,
    credit_expiry_alerts_enabled INTEGER NOT NULL DEFAULT 1,
    announcement_alerts_enabled  INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE class (
    class_id    TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    class_type  TEXT NOT NULL CHECK (class_type IN ('Private','Group','Master'))
);

CREATE TABLE class_session (
    session_id     TEXT PRIMARY KEY,
    class_id       TEXT NOT NULL REFERENCES class(class_id),
    session_date   TEXT NOT NULL,
    start_time     TEXT NOT NULL,
    end_time       TEXT NOT NULL,
    duration_hours REAL,
    session_status TEXT NOT NULL DEFAULT 'Scheduled' CHECK (session_status IN ('Scheduled','Ongoing','Completed'))
);
CREATE INDEX idx_class_session_class ON class_session(class_id, session_date);

CREATE TABLE student_enrollment (
    enrollment_id TEXT PRIMARY KEY,
    student_id    TEXT NOT NULL REFERENCES student(student_id),
    class_id      TEXT NOT NULL REFERENCES class(class_id),
    enrolled_date TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'Active' CHECK (status IN ('Active','Completed','Withdrawn'))
);
CREATE INDEX idx_enrollment_student ON student_enrollment(student_id);
CREATE INDEX idx_enrollment_class ON student_enrollment(class_id);

CREATE TABLE attendance (
    attendance_id  TEXT PRIMARY KEY,
    student_id     TEXT NOT NULL REFERENCES student(student_id),
    session_id     TEXT NOT NULL REFERENCES class_session(session_id),
    check_in_time  TEXT,
    check_out_time TEXT,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at     TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (student_id, session_id)
);
CREATE INDEX idx_attendance_session ON attendance(session_id);

CREATE TABLE credit_package (
    credit_package_id TEXT PRIMARY KEY,
    class_id          TEXT NOT NULL REFERENCES class(class_id),
    credit_amount     REAL NOT NULL,
    standard_price    REAL NOT NULL,
    validity_days     INTEGER NOT NULL
);

CREATE TABLE payment (
    payment_id        TEXT PRIMARY KEY,
    student_id        TEXT NOT NULL REFERENCES student(student_id),
    enrollment_id     TEXT REFERENCES student_enrollment(enrollment_id),
    credit_package_id TEXT REFERENCES credit_package(credit_package_id),
    amount            REAL NOT NULL,
    discount_amount   REAL NOT NULL DEFAULT 0,
    final_amount      REAL NOT NULL,
    payment_method    TEXT NOT NULL CHECK (payment_method IN ('CreditCard','BankTransfer','Cash','PromptPay')),
    status            TEXT NOT NULL DEFAULT 'Paid' CHECK (status IN ('Paid')),
    payment_date      TEXT NOT NULL,
    reference_number  TEXT
);
CREATE INDEX idx_payment_student ON payment(student_id);

CREATE TABLE credit_transaction (
    credit_transaction_id TEXT PRIMARY KEY,
    enrollment_id         TEXT NOT NULL REFERENCES student_enrollment(enrollment_id),
    transaction_type      TEXT NOT NULL CHECK (transaction_type IN ('purchase','consumption','manual_adjustment')),
    amount                REAL NOT NULL,
    expiry_date           TEXT,
    transaction_date      TEXT NOT NULL,
    payment_id            TEXT REFERENCES payment(payment_id),
    attendance_id         TEXT REFERENCES attendance(attendance_id),
    notes                 TEXT
);
CREATE INDEX idx_credit_tx_enrollment ON credit_transaction(enrollment_id);

CREATE TABLE announcement (
    announcement_id        TEXT PRIMARY KEY,
    title                  TEXT NOT NULL,
    body                   TEXT NOT NULL,
    author_user_account_id TEXT NOT NULL REFERENCES user_account(user_account_id),
    posted_at              TEXT NOT NULL DEFAULT (datetime('now')),
    has_attachment         INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE tournament (
    tournament_id               TEXT PRIMARY KEY,
    name                        TEXT NOT NULL,
    tournament_status           TEXT NOT NULL DEFAULT 'Upcoming' CHECK (tournament_status IN ('Upcoming','Ongoing','Completed')),
    start_date                  TEXT,
    end_date                    TEXT,
    venue_name                  TEXT,
    venue_address               TEXT,
    organizer_name              TEXT,
    registration_deadline       TEXT,
    early_bird_fee              REAL,
    regular_fee                 REAL,
    max_participants            INTEGER,
    registration_website_url    TEXT,
    registration_qr_code_image  TEXT,
    regulations_document_url    TEXT
);

CREATE TABLE tournament_category (
    tournament_category_id TEXT PRIMARY KEY,
    tournament_id          TEXT NOT NULL REFERENCES tournament(tournament_id),
    name                   TEXT NOT NULL
);

CREATE TABLE tournament_registration (
    tournament_registration_id TEXT PRIMARY KEY,
    tournament_id              TEXT NOT NULL REFERENCES tournament(tournament_id),
    student_id                 TEXT NOT NULL REFERENCES student(student_id),
    participant_name           TEXT NOT NULL,
    participant_contact        TEXT,
    participant_date_of_birth  TEXT,
    tournament_category_id     TEXT REFERENCES tournament_category(tournament_category_id),
    fide_rating                REAL,
    fee_charged                REAL,
    registered_at              TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (tournament_id, student_id)
);

CREATE TABLE practice_activity (
    activity_id       TEXT PRIMARY KEY,
    student_id        TEXT NOT NULL REFERENCES student(student_id),
    activity_date     TEXT NOT NULL,
    minutes_practiced INTEGER NOT NULL DEFAULT 0,
    puzzles_completed INTEGER NOT NULL DEFAULT 0,
    points_earned     INTEGER NOT NULL DEFAULT 0,
    streak_count      INTEGER NOT NULL DEFAULT 0,
    UNIQUE (student_id, activity_date)
);

CREATE TABLE practice_settings (
    student_id                      TEXT PRIMARY KEY REFERENCES student(student_id),
    daily_screen_time_limit_minutes INTEGER NOT NULL DEFAULT 90
);

CREATE TABLE system_configuration (
    config_key   TEXT PRIMARY KEY,
    config_value TEXT NOT NULL
);
