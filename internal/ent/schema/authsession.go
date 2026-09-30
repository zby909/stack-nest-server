package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuthSession 定义可撤销的管理端会话。
type AuthSession struct{ ent.Schema }

// Fields 定义会话字段；ID 为随机令牌的 SHA-256 摘要。
func (AuthSession) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Immutable(),
		field.String("user_id").NotEmpty().Immutable(),
		field.Time("created_at").Immutable(),
		field.Time("expires_at"),
	}
}

// Edges 定义会话所属用户。
func (AuthSession) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", AuthUser.Type).Ref("sessions").Field("user_id").Unique().Required().Immutable(),
	}
}

// Indexes 优化过期会话清理。
func (AuthSession) Indexes() []ent.Index {
	return []ent.Index{index.Fields("expires_at")}
}
