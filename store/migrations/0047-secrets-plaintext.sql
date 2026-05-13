-- Spec 9/11: v1 stores plaintext per the broker model. Operator trusts disk
-- + FS permissions; encryption at rest deferred.
ALTER TABLE secrets RENAME COLUMN enc_value TO value;
