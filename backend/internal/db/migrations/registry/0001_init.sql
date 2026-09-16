CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- The agent card is cached as raw jsonb rather than exploded into columns: it
-- is an A2A wire type owned by the spec, and re-parsing it on read keeps this
-- table from needing a migration every time the card gains a field.
CREATE TABLE agents (
    slug          text PRIMARY KEY,
    base_url      text NOT NULL,
    card          jsonb NOT NULL,
    persona_id    uuid NOT NULL,
    registered_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agents_last_seen_at ON agents (last_seen_at DESC);
