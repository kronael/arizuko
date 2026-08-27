CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
-- Burn ~100ms inside the exclusive transaction so every other opener has
-- certainly read MAX(version)=0 before this one commits. Without a hold this
-- long the goroutines serialise and the recheck is never exercised.
CREATE TABLE burn AS
  WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 400000)
  SELECT count(*) AS n FROM c;
INSERT INTO widgets (id, name) VALUES (1, 'first');
