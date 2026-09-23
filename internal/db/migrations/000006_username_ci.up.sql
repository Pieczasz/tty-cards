-- Usernames are unique case-insensitively (decision D-4): "Alice" and "alice" read as
-- the same player on a leaderboard, so letting both register is an impersonation
-- surface. The display case is kept; only the comparison folds.
--
-- Existing rows that already collide are not guessed at. Renaming one of them is an
-- operator's decision - which account keeps the name - so the migration refuses and
-- names every clash instead of creating an index that would fail with a bare 23505.
DO $$
DECLARE
    clashes TEXT;
BEGIN
    SELECT string_agg(names, '; ' ORDER BY names) INTO clashes
    FROM (
        SELECT string_agg(username, ', ' ORDER BY username) AS names
        FROM users
        GROUP BY lower(username)
        HAVING count(*) > 1
    ) collisions;

    IF clashes IS NOT NULL THEN
        RAISE EXCEPTION 'usernames that differ only by case must be resolved before 000006: %', clashes;
    END IF;
END $$;

CREATE UNIQUE INDEX idx_users_username_lower ON users (lower(username));
