-- Fix @ and # prefix route seq values so they evaluate before the default
-- route (seq 0). Old values were 9998/9999 (after default), should be -2/-1.
UPDATE routes SET seq = -2 WHERE type = 'prefix' AND match = '@' AND seq = 9998;
UPDATE routes SET seq = -1 WHERE type = 'prefix' AND match = '#' AND seq = 9999;
