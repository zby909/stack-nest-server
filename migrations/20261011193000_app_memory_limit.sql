-- 扩大单应用内存范围；实例总预算继续由 app_limits 单独控制。
ALTER TABLE container_apps
    DROP CONSTRAINT container_apps_memory_mib_check,
    ADD CONSTRAINT container_apps_memory_mib_check CHECK (memory_mib BETWEEN 64 AND 1024);
