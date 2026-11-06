CREATE TABLE app_deployments (
  app_id varchar NOT NULL REFERENCES container_apps(id) ON DELETE CASCADE,
  generation bigint NOT NULL CHECK (generation > 0),
  image varchar(256) NOT NULL,
  image_id varchar(128) NOT NULL DEFAULT '',
  actor_email varchar(254) NOT NULL,
  source varchar(16) NOT NULL CHECK (source IN ('web', 'automation')),
  status varchar(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'deploying', 'succeeded', 'failed', 'canceled')),
  message text NOT NULL DEFAULT '',
  queued_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  PRIMARY KEY (app_id, generation)
);
