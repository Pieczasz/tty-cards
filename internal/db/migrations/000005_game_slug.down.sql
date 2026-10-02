-- LOSSY. This drops games.slug, and 000005 up re-derives it from name. A game renamed
-- since 000005 comes back under a slug derived from its new display name, so
-- its ratings no longer hang off the catalog's slug; and a duplicate display name
-- comes back suffixed with its id. Take a backup before rolling past this.
--
-- Duplicate display names (a leftover of a rename while slug was identity) would
-- make UNIQUE(name) fail. Keep the oldest row's name; suffix the rest.
UPDATE games g
SET name = name || '_' || id::text
WHERE id NOT IN (
    SELECT MIN(id) FROM games GROUP BY name
);

CREATE UNIQUE INDEX idx_games_name ON games(name);
DROP INDEX IF EXISTS idx_games_slug;
ALTER TABLE games DROP COLUMN slug;
