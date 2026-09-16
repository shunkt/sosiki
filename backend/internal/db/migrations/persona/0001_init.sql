CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- slug is new: a persona pod identifies itself by a stable, human-readable name
-- fixed in its Deployment manifest, not by a uuid that changes on every reseed.
CREATE TABLE personas (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug        text NOT NULL UNIQUE,
    name        text NOT NULL,
    stance      text NOT NULL DEFAULT '',
    verbosity   text NOT NULL DEFAULT 'balanced'
                  CHECK (verbosity IN ('concise', 'balanced', 'detailed')),
    skepticism  real NOT NULL DEFAULT 0.5
                  CHECK (skepticism >= 0 AND skepticism <= 1),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE persona_interests (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    persona_id uuid NOT NULL REFERENCES personas(id) ON DELETE CASCADE,
    topic      text NOT NULL,
    weight     real NOT NULL CHECK (weight >= -1 AND weight <= 1),
    embedding  vector(1536) NOT NULL
);
CREATE INDEX persona_interests_persona_id ON persona_interests (persona_id);
