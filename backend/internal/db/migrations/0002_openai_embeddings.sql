-- Switching from ruri-v3-310m (768d, self-hosted TEI) to OpenAI
-- text-embedding-3-small (1536d). The two live in unrelated vector spaces,
-- so stored vectors cannot be converted — the rows are dropped and
-- recreated by `go run ./cmd/seed`.
--
-- chunks is emptied first because ADD COLUMN ... NOT NULL without a default
-- fails on a non-empty table. Deleting chunks cascades to message_citations
-- (ON DELETE CASCADE), so existing conversations keep their message text
-- but lose their citation rows. That is acceptable for dev data.
DELETE FROM chunks;
DELETE FROM persona_interests;

-- DROP COLUMN also drops chunks_embedding_hnsw, which depends on it.
ALTER TABLE chunks DROP COLUMN embedding;
ALTER TABLE chunks ADD COLUMN embedding vector(1536) NOT NULL;

-- vector_cosine_ops, so queries must use <=> (cosine distance) to hit the
-- index. search.go now also reads that distance as the relevance score.
CREATE INDEX chunks_embedding_hnsw
    ON chunks USING hnsw (embedding vector_cosine_ops);

ALTER TABLE persona_interests DROP COLUMN embedding;
ALTER TABLE persona_interests ADD COLUMN embedding vector(1536) NOT NULL;
