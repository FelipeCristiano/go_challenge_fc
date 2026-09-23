-- reversão da migration 000001_initial_schema
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP RULE IF EXISTS wallet_ledger_no_delete ON wallet_ledger_entries;
DROP RULE IF EXISTS wallet_ledger_no_update ON wallet_ledger_entries;
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
DROP TYPE IF EXISTS outbox_event_status;
DROP TYPE IF EXISTS ledger_direction;
DROP TYPE IF EXISTS transaction_status;
DROP TYPE IF EXISTS transaction_kind;
DROP TYPE IF EXISTS currency_code;
