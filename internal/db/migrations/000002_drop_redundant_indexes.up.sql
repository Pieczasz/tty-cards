-- Indexes no query can use still cost every insert (register, finalize).

-- Implied by idx_users_username_lower: names equal byte for byte are equal folded,
-- so that index is the uniqueness rule (decision D-4) and registerUser's 23505 still
-- fires on it. No query compares the raw username.
ALTER TABLE users DROP CONSTRAINT users_username_key;

-- A strict prefix of idx_match_participants_user_match (user_id, match_id DESC),
-- which serves every user_id lookup this one did, the FK cascade included.
DROP INDEX idx_match_participants_user_id;
