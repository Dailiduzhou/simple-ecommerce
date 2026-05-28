CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    nickname VARCHAR(50) NOT NULL DEFAULT '',
    real_name VARCHAR(50) NOT NULL DEFAULT '',
    
    phone_hash VARCHAR(128) NOT NULL,       -- 用于登录查询的手机号摘要 (HMAC-SHA256)
    phone_encrypt VARCHAR(255) NOT NULL,    -- 用于业务展示的手机号密文 (AES-GCM)
    
    password_hash VARCHAR(255) NOT NULL,    -- 用户密码的单向哈希 (Bcrypt)
    
    -- 收货地址通常是一对多关系，但在单表设计中推荐用 JSONB 以保持灵活性
    shipping_address JSONB DEFAULT '[]'::jsonb, 
    
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 为登录查询创建唯一索引
CREATE UNIQUE INDEX idx_users_phone_hash ON users(phone_hash);

-- 表备注
COMMENT ON TABLE users IS '电商系统用户表';
COMMENT ON COLUMN users.id IS '用户全局唯一ID';
COMMENT ON COLUMN users.phone_hash IS '手机号HMAC摘要，用于等值匹配登录';
COMMENT ON COLUMN users.phone_encrypt IS '手机号AES对称加密密文，用于解密展示';
COMMENT ON COLUMN users.password_hash IS 'Bcrypt加密后的密码';
COMMENT ON COLUMN users.shipping_address IS '收货地址列表(JSON格式)';
