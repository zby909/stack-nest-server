-- 凭据只能绑定一种资源，旧静态项目凭据的权限保持原样。
ALTER TABLE deploy_tokens ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE deploy_tokens ADD COLUMN app_id varchar REFERENCES container_apps(id) ON DELETE CASCADE;
ALTER TABLE deploy_tokens ADD CONSTRAINT deploy_tokens_resource CHECK (
  (project_id IS NOT NULL AND app_id IS NULL) OR
  (project_id IS NULL AND app_id IS NOT NULL AND can_publish=false)
);
CREATE INDEX deploy_tokens_app_id ON deploy_tokens(app_id);

-- 每应用只保留最近一次自动部署回执，有界保存，断线重试不会重复入队。
CREATE TABLE app_deploy_requests (
  app_id varchar PRIMARY KEY REFERENCES container_apps(id) ON DELETE CASCADE,
  token_id varchar NOT NULL REFERENCES deploy_tokens(id) ON DELETE CASCADE,
  request_id uuid NOT NULL,
  image varchar(256) NOT NULL,
  expected_generation bigint NOT NULL CHECK (expected_generation > 0),
  generation bigint NOT NULL CHECK (generation > expected_generation)
);
