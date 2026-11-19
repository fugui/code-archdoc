CREATE TABLE orders (
    order_id VARCHAR(64) PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL,
    sku_id VARCHAR(64) NOT NULL,
    amount_cents BIGINT NOT NULL,
    status INT NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL
);

CREATE INDEX idx_orders_user_id ON orders (user_id);

CREATE TABLE order_payments (
    tx_id VARCHAR(64) PRIMARY KEY,
    order_id VARCHAR(64) NOT NULL,
    payment_channel VARCHAR(32) NOT NULL,
    amount_cents BIGINT NOT NULL,
    paid_at TIMESTAMP NOT NULL
);

CREATE INDEX idx_order_payments_order_id ON order_payments (order_id);
