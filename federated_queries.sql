-- Federated / Warehouse Queries
-- These queries target the federated PostgreSQL database.
-- Use fully qualified table names (schema_name.table_name)
-- since no default search_path is set on this connection.

--name: legacy-user-exists-by-blockchain-address
SELECT EXISTS (
    SELECT 1
    FROM sarafu_network.users
    WHERE LOWER(blockchain_address) = LOWER($1)
);
