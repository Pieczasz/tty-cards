-- Fresh schema: one migration, no upgrade path. Recreate the volume to apply.

CREATE TABLE users (
    -- uuidv7() is Postgres 18; time-ordered, not gen_random_uuid().
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ,
    -- NOT NULL: a NULL username would satisfy UNIQUE (many NULLs) and skip the CHECK.
    -- 40 chars is deleted_ + 32 hex of the UUID; chosen names stay 1-16 via the CHECK
    -- and ValidateUsername, so a raw insert cannot squat a 40-char player name.
    username VARCHAR(40) NOT NULL UNIQUE,
    -- Anonymised form is pinned to this row's id so another account cannot squat
    -- deleted_<victim_hex> and make GDPR erase fail on the unique username index.
    CONSTRAINT username_valid CHECK (
        username ~ '^[A-Za-z0-9_]+$'
        AND (
            (char_length(username) <= 16 AND username !~* '^deleted_')
            OR username = 'deleted_' || replace(id::text, '-', '')
        )
    )
);
CREATE INDEX idx_users_deleted_at ON users(deleted_at);
-- Case-insensitive uniqueness (decision D-4): display case is kept; comparison folds.
CREATE UNIQUE INDEX idx_users_username_lower ON users (lower(username));

CREATE TABLE public_keys (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    -- NOT NULL: a NULL fingerprint would also satisfy UNIQUE (many NULLs).
    fingerprint TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    last_used_at TIMESTAMPTZ,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_public_keys_deleted_at ON public_keys(deleted_at);
CREATE INDEX idx_public_keys_user_id ON public_keys(user_id);

CREATE TABLE games (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    -- slug is the catalog's stable key; name is the display string refreshed on finalize.
    slug TEXT NOT NULL,
    name TEXT NOT NULL
);
CREATE INDEX idx_games_deleted_at ON games(deleted_at);
CREATE UNIQUE INDEX idx_games_slug ON games(slug);

CREATE TABLE rankings (
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    game_id BIGINT REFERENCES games(id) ON DELETE CASCADE,
    elo BIGINT NOT NULL DEFAULT 1500,
    -- Provisional accounts: matches_played decides whether beating this row pays
    -- anything (see internal/repository).
    matches_played BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, game_id),
    -- elo >= 0 vs Go elo.MinRating = 100 is deliberate: the constraint is the wider
    -- bound so application policy can move without a migration.
    CONSTRAINT elo_valid CHECK (elo >= 0 AND elo <= 4000)
);
CREATE INDEX idx_rankings_deleted_at ON rankings(deleted_at);
CREATE INDEX idx_rankings_elo ON rankings(elo DESC);
-- Leaderboard hot path: filter by game, sort by Elo, skip soft-deleted.
CREATE INDEX idx_rankings_game_elo ON rankings (game_id, elo DESC) WHERE deleted_at IS NULL;
-- ON DELETE CASCADE seq-scans without an index on the FK; the partial above is not enough.
CREATE INDEX idx_rankings_game_id ON rankings (game_id);

CREATE TABLE matches (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    game_id BIGINT NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    -- Casual matches are recorded for history but never move Elo, and a ranked
    -- match can legitimately swing zero, so the two cannot be told apart by delta.
    ranked BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX idx_matches_deleted_at ON matches(deleted_at);
CREATE INDEX idx_matches_game_id ON matches (game_id);
-- Anti-farm pair count: ranked matches in the last 24 hours.
CREATE INDEX idx_matches_ranked_created ON matches (created_at) WHERE ranked AND deleted_at IS NULL;

CREATE TABLE match_participants (
    match_id BIGINT REFERENCES matches(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    placement BIGINT NOT NULL,
    elo_delta BIGINT NOT NULL,
    PRIMARY KEY (match_id, user_id)
);
CREATE INDEX idx_match_participants_user_id ON match_participants(user_id);
-- UserMatchHistory filters on user_id and sorts by match_id desc.
CREATE INDEX idx_match_participants_user_match ON match_participants (user_id, match_id DESC);
