-- 部署凭据只保存摘要，项目或创建者删除后自动失效。
CREATE TABLE deploy_tokens (
  id varchar PRIMARY KEY,
  project_id varchar NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  owner_user_id varchar NOT NULL,
  creator_user_id varchar NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
  name varchar(240) NOT NULL,
  token_hash char(64) NOT NULL UNIQUE,
  can_publish boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  last_used_at timestamptz
);
CREATE INDEX deploy_tokens_project_id ON deploy_tokens(project_id);
