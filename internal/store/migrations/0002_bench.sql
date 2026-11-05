CREATE TABLE bench_runs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at  INTEGER NOT NULL, -- Unix microseconds
	finished_at INTEGER,          -- NULL while running
	state       TEXT    NOT NULL,
	build_id    TEXT    NOT NULL,
	model       TEXT    NOT NULL,
	setup       TEXT    NOT NULL CHECK (json_valid(setup)),
	results     TEXT    NOT NULL CHECK (json_valid(results)),
	error       TEXT    NOT NULL
);
CREATE INDEX bench_runs_model ON bench_runs (model, started_at DESC);
