CREATE TABLE telemetry_samples (
	ts          INTEGER PRIMARY KEY, -- Unix microseconds, the start of the sample's period
	decode      REAL NOT NULL,       -- mean tokens per second
	decode_peak REAL NOT NULL,       -- highest per-second rate in the period
	prefill     REAL NOT NULL,
	ttft        REAL NOT NULL,       -- ms; 0 with no requests to measure
	rpm         REAL NOT NULL,
	kv          REAL NOT NULL        -- share of context cells in use, 0 to 1
);
