-- 实例资源预算由超管调整，默认保持现有三应用和 768 MiB 配置。
CREATE TABLE app_limits (
    id smallint PRIMARY KEY CHECK (id = 1),
    max_apps integer NOT NULL CHECK (max_apps BETWEEN 1 AND 100),
    total_memory_mib integer NOT NULL CHECK (total_memory_mib BETWEEN 64 AND 131072),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);
INSERT INTO app_limits (id,max_apps,total_memory_mib) VALUES (1,3,768);
