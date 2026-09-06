CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
-- Hold the exclusive transaction long enough that every other opener has read
-- MAX(version)=0 before this one commits. Without the hold the goroutines
-- serialise and the recheck is never exercised.
--
-- The row count is bounded ABOVE by busy_timeout(5000): a losing opener waits
-- that long and then reports SQLITE_BUSY, so a hold near the timeout turns a
-- correctness test into a timing test. 40,000 rows measures ~106ms here and
-- stays under the timeout with the race detector's slowdown on top, which is
-- the configuration that finds what this test is for.
CREATE TABLE burn AS
  WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 40000)
  SELECT count(*) AS n FROM c;
INSERT INTO widgets (id, name) VALUES (1, 'first');
