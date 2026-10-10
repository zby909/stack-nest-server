env "local" {
  url = getenv("STACKNEST_DATABASE_URL")
  dev = "docker://postgres/17/dev?search_path=public"

  schema {
    src = "ent://internal/ent/schema"
  }

  migration {
    dir = "file://migrations"
    # 应用任务表由手写 SQL 管理，Ent 差异生成不能删除它。
    exclude = ["container_apps"]
  }
}
