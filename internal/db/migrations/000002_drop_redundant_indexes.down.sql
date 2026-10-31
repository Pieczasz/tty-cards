-- Not lossy: idx_users_username_lower already keeps raw names unique.
CREATE INDEX idx_match_participants_user_id ON match_participants(user_id);
ALTER TABLE users ADD CONSTRAINT users_username_key UNIQUE (username);
