CREATE TABLE IF NOT EXISTS noema_sources (
    id text PRIMARY KEY,
    canonical_url text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS noema_ingestion_requests (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id text NOT NULL REFERENCES noema_sources(id),
    fingerprint text NOT NULL UNIQUE,
    focus text NOT NULL,
    state text NOT NULL DEFAULT 'PENDING' CHECK (state IN (
        'PENDING', 'CAPTURING', 'CONSOLIDATING', 'PUBLISHING', 'COMPLETED',
        'RETRY_SCHEDULED', 'ACCESS_REQUIRED', 'NEEDS_REVIEW', 'FAILED'
    )),
    attempt integer NOT NULL DEFAULT 0,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS noema_artifacts (
    path text PRIMARY KEY,
    source_id text NOT NULL REFERENCES noema_sources(id),
    kind text NOT NULL,
    sha256 text NOT NULL,
    size integer NOT NULL,
    verified_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS noema_publication_intents (
    request_id bigint PRIMARY KEY REFERENCES noema_ingestion_requests(id),
    note_path text NOT NULL,
    sha256 text NOT NULL
);

CREATE TABLE IF NOT EXISTS noema_publications (
    source_id text PRIMARY KEY REFERENCES noema_sources(id),
    request_id bigint NOT NULL REFERENCES noema_ingestion_requests(id),
    note_path text NOT NULL,
    blob_sha text NOT NULL,
    published_at timestamptz NOT NULL DEFAULT now()
);
