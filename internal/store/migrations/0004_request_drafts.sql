-- Speculative decoding's tokens, 0 for a request without it.
ALTER TABLE requests ADD COLUMN draft_n INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN draft_accepted INTEGER NOT NULL DEFAULT 0;
