-- migration 0004: persist the rendered message body so the retry worker
-- can re-send without rebuilding from upstream context. Additive.

ALTER TABLE telegram_deliveries ADD COLUMN IF NOT EXISTS body text;
