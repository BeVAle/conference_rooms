CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL
);

CREATE TABLE rooms (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE,
    capacity integer NOT NULL CHECK (capacity > 0)
);

CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE bookings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id),
    room_id bigint NOT NULL REFERENCES rooms(id),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'Active' CHECK (status IN ('Active', 'Cancelled')),
    CHECK (ends_at - starts_at BETWEEN interval '30 minutes' AND interval '4 hours'),
    CONSTRAINT bookings_no_overlap EXCLUDE USING gist (
        room_id WITH =,
        tstzrange(starts_at, ends_at, '[)') WITH &&
    ) WHERE (status = 'Active')
);
CREATE INDEX bookings_user_time_idx ON bookings(user_id, starts_at, id);

INSERT INTO rooms (name, capacity) VALUES ('Малая переговорная', 4),
    ('Большая переговорная', 10), ('Комната для встреч', 6);

-- Пароль обоих учебных аккаунтов: conference123.
INSERT INTO users (name, email, password_hash) VALUES
    ('Сотрудник', 'user@example.com', '$argon2id$v=19$m=65536,t=3,p=2$4LroozmH65DuZZPCvVc6Ug$Ku4gckbJm9Q+ILRYdzVVeR3wSXAlegnv8Zlvjv0KuP0'),
    ('Коллега', 'colleague@example.com', '$argon2id$v=19$m=65536,t=3,p=2$HIGVVtUQMFPPCZvC9bmbTw$AZm1geHcYecub8c+OYYsi3Q8E36qBCDUsmSBKdlp5VU');
