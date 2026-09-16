-- Runs once, only when the postgres container's data directory is empty
-- (docker-entrypoint-initdb.d semantics) — see docker-compose.yml's mount
-- of this file and the GOTCHA in the plan about stale volumes.
--
-- CREATE EXTENSION and every table live in each service's own startup
-- migration (internal/db/migrations/<set>/0001_init.sql) — this script only
-- creates the four empty databases those migrations then apply to.
CREATE DATABASE kaigi_meeting;
CREATE DATABASE kaigi_registry;
CREATE DATABASE kaigi_persona;
CREATE DATABASE kaigi_knowledge;
