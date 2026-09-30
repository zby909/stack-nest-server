package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AuthUser 定义允许登录管理端的邮箱。
type AuthUser struct{ ent.Schema }

// Fields 定义登录用户字段。
func (AuthUser) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Immutable(),
		field.String("email").NotEmpty().Unique(),
		field.String("role").NotEmpty(),
		field.Time("created_at").Immutable(),
	}
}

// Edges 定义用户的验证码与会话。
func (AuthUser) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("challenges", AuthChallenge.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("sessions", AuthSession.Type).Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
