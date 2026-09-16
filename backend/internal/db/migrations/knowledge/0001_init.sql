CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

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
    embedding   vector(1536) NOT NULL,
    UNIQUE (document_id, chunk_index)
);

-- vector_cosine_ops, so queries must use <=> (cosine distance) to hit the index.
CREATE INDEX chunks_embedding_hnsw
    ON chunks USING hnsw (embedding vector_cosine_ops);
