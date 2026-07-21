-- Seeded test schema for the pgpilot dev cluster.
--
-- Created on the primary during initialization; because the replicas clone the
-- primary with pg_basebackup, they inherit this schema and its seed rows and
-- then keep it current over streaming replication.

CREATE TABLE IF NOT EXISTS accounts (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      text        NOT NULL UNIQUE,
    balance    bigint      NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ledger (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id bigint      NOT NULL REFERENCES accounts (id),
    delta      bigint      NOT NULL,
    note       text,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The smoke test writes a unique token here and reads it back from each
-- replica to prove that primary writes reach every standby.
CREATE TABLE IF NOT EXISTS replication_probe (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO accounts (email, balance) VALUES
    ('alice@example.com', 1000),
    ('bob@example.com',    500),
    ('carol@example.com',    0);

INSERT INTO ledger (account_id, delta, note) VALUES
    (1, 1000, 'initial deposit'),
    (2,  500, 'initial deposit');
