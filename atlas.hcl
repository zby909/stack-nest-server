env "local" {
  url = getenv("STACKNEST_DATABASE_URL")
  dev = "docker://postgres/17/dev?search_path=public"

  schema {
    src = "ent://internal/ent/schema"
  }

  migration {
    dir = "file://migrations"
  }
}
