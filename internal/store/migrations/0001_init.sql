CREATE TABLE kv (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE profiles (
	id  TEXT PRIMARY KEY,
	doc TEXT NOT NULL CHECK (json_valid(doc))
) WITHOUT ROWID;

CREATE TABLE requests (
	id                   INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at           INTEGER NOT NULL, -- Unix microseconds, like every timestamp here
	model                TEXT    NOT NULL,
	endpoint             TEXT    NOT NULL,
	method               TEXT    NOT NULL,
	status               INTEGER NOT NULL,
	status_class         INTEGER NOT NULL, -- status / 100, so charts group without arithmetic
	slot                 INTEGER,          -- NULL when unknown; 0 is a real slot
	task_id              INTEGER,          -- NULL when unknown
	ttft_ms              REAL    NOT NULL,
	total_ms             REAL    NOT NULL,
	prompt_n             INTEGER NOT NULL,
	cache_n              INTEGER NOT NULL,
	prompt_ms            REAL    NOT NULL,
	predicted_n          INTEGER NOT NULL,
	predicted_ms         REAL    NOT NULL,
	predicted_per_second REAL    NOT NULL,
	stream               INTEGER NOT NULL,
	error                TEXT    NOT NULL,
	gaps                 TEXT CHECK (gaps IS NULL OR json_valid(gaps))
);
CREATE INDEX requests_started_at ON requests (started_at, id);
CREATE INDEX requests_model ON requests (model, started_at, id);
CREATE INDEX requests_endpoint ON requests (endpoint, started_at, id);

CREATE TABLE request_bodies (
	request_id INTEGER PRIMARY KEY REFERENCES requests (id) ON DELETE CASCADE,
	request    BLOB,
	response   BLOB
);

CREATE TABLE log_lines (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	ts      INTEGER NOT NULL,
	level   TEXT    NOT NULL,
	source  TEXT    NOT NULL,
	model   TEXT    NOT NULL,
	message TEXT    NOT NULL,
	raw     TEXT    NOT NULL
);
CREATE INDEX log_lines_ts ON log_lines (ts);

CREATE TABLE events (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       INTEGER NOT NULL,
	kind     TEXT    NOT NULL,
	severity TEXT    NOT NULL,
	title    TEXT    NOT NULL,
	meta     TEXT CHECK (meta IS NULL OR json_valid(meta))
);
CREATE INDEX events_ts ON events (ts);
