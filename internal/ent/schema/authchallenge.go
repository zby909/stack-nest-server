package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuthChallenge 定义一次性邮件验证码记录。
type AuthChallenge struct{ ent.Schema }

// Fields 定义验证码字段；数据库只保存服务端密钥计算的摘要。
func (AuthChallenge) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Immutable(),
		field.String("user_id").NotEmpty().Immutable(),
		field.String("code_hash").NotEmpty(),
		field.Int("attempts").Default(0),
		field.Time("sent_at"),
		field.Time("expires_at"),
	}
}

// Indexes 保证每个用户最多只有一个有效验证码记录。
func (AuthChallenge) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id").Unique()}
}

// Edges 定义验证码所属用户。
func (AuthChallenge) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", AuthUser.Type).Ref("challenges").Field("user_id").Unique().Required().Immutable(),
	}
}
