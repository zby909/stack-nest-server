package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Project 定义 StackNest 项目表。
type Project struct {
	ent.Schema
}

// Fields 定义项目字段。
func (Project) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			NotEmpty().
			Immutable(),
		field.String("owner_user_id").NotEmpty(),
		field.String("name").
			NotEmpty(),
		field.String("slug").
			NotEmpty().
			MaxLen(63).
			SchemaType(map[string]string{dialect.Postgres: "varchar(63)"}).
			Unique().
			Immutable(),
		field.String("description").
			Default(""),
		field.String("status").
			NotEmpty(),
		field.Time("created_at").
			Immutable(),
		field.Time("updated_at"),
	}
}

// Edges 定义项目与部署记录的关系。
func (Project) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("deployments", Deployment.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

// Indexes 优化项目列表按更新时间排序。
func (Project) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("updated_at"),
		index.Fields("owner_user_id"),
	}
}
