-- The anti-farm pair count reads every ranked match of the last 24 hours on each ranked
-- finalize. Without this it drove from match_participants by user and joined out to
-- matches, so a veteran's whole history was read to find the handful inside the window.
-- The predicate matches repeatedPairCountLast24h's (ranked, not soft-deleted), which is
-- what lets the planner use a partial index.
CREATE INDEX idx_matches_ranked_created ON matches (created_at) WHERE ranked AND deleted_at IS NULL;
