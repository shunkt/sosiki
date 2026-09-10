CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE personas (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        text NOT NULL,
    stance      text NOT NULL DEFAULT '',
    verbosity   text NOT NULL DEFAULT 'balanced'
                  CHECK (verbosity IN ('concise', 'balanced', 'detailed')),
    skepticism  real NOT NULL DEFAULT 0.5
                  CHECK (skepticism >= 0 AND skepticism <= 1),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- An interest axis biases retrieval toward or away from a topic. The embedding
-- is of "検索クエリ: {topic}" rather than "トピック: {topic}": affinity is scored
-- against document chunks, and query-to-document is the pairing ruri-v3 was
-- trained on.
CREATE TABLE persona_interests (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    persona_id uuid NOT NULL REFERENCES personas(id) ON DELETE CASCADE,
    topic      text NOT NULL,
    weight     real NOT NULL CHECK (weight >= -1 AND weight <= 1),
    embedding  vector(768) NOT NULL
);
CREATE INDEX persona_interests_persona_id ON persona_interests (persona_id);

CREATE TABLE documents (
    id           uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    title        text NOT NULL,
    bucket       text NOT NULL,
    object_key   text NOT NULL,
    content_type text NOT NULL DEFAULT 'text/plain',
    size_bytes   bigint NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (bucket, object_key)
);

-- byte_start/byte_end locate the chunk inside the original object so a Range
-- GET can read the surrounding text: the chunk is the search unit, the expanded
-- span is the reading unit.
CREATE TABLE chunks (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index int  NOT NULL,
    content     text NOT NULL,
    byte_start  bigint NOT NULL,
    byte_end    bigint NOT NULL,
    embedding   vector(768) NOT NULL,
    UNIQUE (document_id, chunk_index)
);

-- vector_cosine_ops, so queries must use <=> (cosine distance) to hit the index.
CREATE INDEX chunks_embedding_hnsw
    ON chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE conversations (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    persona_id uuid NOT NULL REFERENCES personas(id),
    title      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE messages (
    id              uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq             int  NOT NULL,
    role            text NOT NULL CHECK (role IN ('user', 'assistant')),
    content         text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq)
);

-- relevance and affinity are stored separately, not just the blended score, so
-- the UI can show how much the persona moved each citation.
CREATE TABLE message_citations (
    message_id      uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    chunk_id        uuid NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    rank            int  NOT NULL,
    relevance_score real NOT NULL,
    affinity_score  real NOT NULL,
    PRIMARY KEY (message_id, chunk_id)
);
