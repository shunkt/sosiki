CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE meetings (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    topic      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- speaking_order is explicit rather than implied by insertion: the moderator
-- must call participants in a deterministic order for a meeting to be
-- reproducible, and "the order the client sent them" is that order.
CREATE TABLE meeting_participants (
    meeting_id     uuid NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    persona_slug   text NOT NULL,
    persona_name   text NOT NULL,
    speaking_order int  NOT NULL,
    PRIMARY KEY (meeting_id, persona_slug),
    UNIQUE (meeting_id, speaking_order)
);

-- speaker_slug is null for the human's turns. round is 0 for the human's
-- opening topic, 1..n for the persona rounds that answer it.
CREATE TABLE turns (
    id           uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    meeting_id   uuid NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    seq          int  NOT NULL,
    round        int  NOT NULL,
    role         text NOT NULL CHECK (role IN ('user', 'persona')),
    speaker_slug text,
    speaker_name text NOT NULL,
    content      text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (meeting_id, seq),
    CHECK ((role = 'user') = (speaker_slug IS NULL))
);

-- chunk_id carries NO foreign key: chunks live in kaigi_knowledge and Postgres
-- cannot reference across databases. title/object_key/scores are denormalised
-- copies so a citation stays readable even if the chunk is later reseeded —
-- which is exactly what the FK would have prevented, and now cannot.
CREATE TABLE turn_citations (
    turn_id         uuid NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    chunk_id        uuid NOT NULL,
    document_id     uuid NOT NULL,
    rank            int  NOT NULL,
    title           text NOT NULL,
    object_key      text NOT NULL,
    relevance_score real NOT NULL,
    affinity_score  real NOT NULL,
    PRIMARY KEY (turn_id, chunk_id)
);
