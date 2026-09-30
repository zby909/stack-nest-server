package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Deployment 定义一次项目部署记录。
type Deployment struct {
	ent.Schema
}

// Fields 定义部署记录字段。
func (Deployment) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			NotEmpty().
			Immutable(),
		field.String("project_id").
			NotEmpty().
			Immutable(),
		field.String("status").
			NotEmpty(),
		field.String("file_name").
			NotEmpty(),
		field.Time("created_at").
			Immutable(),
		field.Time("finished_at").
			Optional().
			Nillable(),
		field.String("error").
			Default(""),
	}
}

// Edges 定义部署记录所属项目。
func (Deployment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("deployments").
			Field("project_id").
			Unique().
			Required().
			Immutable(),
	}
}

// Indexes 优化项目部署记录按时间查询。
func (Deployment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("project_id", "created_at"),
	}
}
