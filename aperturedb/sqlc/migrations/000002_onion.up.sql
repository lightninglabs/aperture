CREATE TABLE IF NOT EXISTS onion (
    private_key BLOB UNIQUE NOT NULL,
    created_at TIMESTAMP NOT NULL 
);
