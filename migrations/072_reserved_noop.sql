-- Version 072 is intentionally empty.
--
-- The production database was advanced to version 72 by a build that shipped
-- three other migrations here (recovery tables and triggers, never populated).
-- tern compares the stored version with the number of migration files, so the
-- sequence must reach 72 for this codebase to start against that database.
-- Staging and fresh databases run these as no-ops. The objects that build
-- created are removed by a later migration.
SELECT 1;

---- create above / drop below ----

SELECT 1;
