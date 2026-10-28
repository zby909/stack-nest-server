package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/deploytoken"
	"stacknest/server/internal/project"
)

// TestDeploymentTokenIntegration 验证真实外键、撤销、过期、角色变化及归属变更，不接触生产库。
func TestDeploymentTokenIntegration(t *testing.T) {
	url := os.Getenv("STACKNEST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STACKNEST_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := store.AddUser(ctx, uuid.NewString()+"@example.com", auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.AddUser(ctx, uuid.NewString()+"@example.com", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := store.AddUser(ctx, uuid.NewString()+"@example.com", auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	p := project.Project{ID: uuid.NewString(), Slug: "tokens-" + uuid.NewString(), OwnerID: owner.ID, Name: "Tokens", Status: "draft", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	defer func() {
		store.Delete(ctx, p.Slug, "")
		store.db.Exec("DELETE FROM auth_users WHERE id IN ($1,$2,$3)", owner.ID, admin.ID, stranger.ID)
	}()
	token, plain, err := deploytoken.New(p.ID, "CI", 30, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateDeployToken(ctx, token, stranger); !errors.Is(err, project.ErrNotFound) {
		t.Fatal("other user created token", err)
	}
	if err = store.CreateDeployToken(ctx, token, owner); err != nil {
		t.Fatal(err)
	}
	grant, err := store.AuthenticateDeployToken(ctx, token.Hash)
	if err != nil || grant.User.ID != owner.ID || grant.ProjectID != p.ID || grant.OwnerID != owner.ID || grant.CanPublish {
		t.Fatal("invalid grant", err)
	}
	var stored string
	if err = store.db.QueryRow("SELECT token_hash FROM deploy_tokens WHERE id=$1", token.ID).Scan(&stored); err != nil || stored == plain || stored != token.Hash {
		t.Fatal("plaintext persisted", err)
	}
	if _, err = store.ListDeployTokens(ctx, p.ID, stranger); !errors.Is(err, project.ErrNotFound) {
		t.Fatal("other user listed", err)
	}
	if err = store.DeleteDeployToken(ctx, p.ID, token.ID, stranger); !errors.Is(err, project.ErrNotFound) {
		t.Fatal("other user revoked", err)
	}
	items, err := store.ListDeployTokens(ctx, p.ID, admin)
	if err != nil || len(items) != 1 || items[0].Hash != "" || items[0].LastUsedAt == nil {
		t.Fatal("unsafe or stale list", err)
	}
	if err = store.DeleteDeployToken(ctx, p.ID, token.ID, admin); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateDeployToken(ctx, token.Hash); !errors.Is(err, deploytoken.ErrInvalid) {
		t.Fatal("revoked grant valid", err)
	}
	// 管理员签发的凭据在降权后不能继续操作他人项目。
	token, _, _ = deploytoken.New(p.ID, "admin CI", 30, true)
	if err = store.CreateDeployToken(ctx, token, admin); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE auth_users SET role='user' WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateDeployToken(ctx, token.Hash); !errors.Is(err, deploytoken.ErrInvalid) {
		t.Fatal("downgrade ignored", err)
	}
	if _, err = store.db.Exec("UPDATE auth_users SET role='admin' WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE projects SET owner_user_id=$1 WHERE id=$2", stranger.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateDeployToken(ctx, token.Hash); !errors.Is(err, deploytoken.ErrInvalid) {
		t.Fatal("transfer ignored", err)
	}
	if _, err = store.db.Exec("UPDATE projects SET owner_user_id=$1 WHERE id=$2", owner.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE deploy_tokens SET expires_at=now()-interval '1 second' WHERE id=$1", token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateDeployToken(ctx, token.Hash); !errors.Is(err, deploytoken.ErrInvalid) {
		t.Fatal("expiry ignored", err)
	}
	if err = store.DeleteDeployToken(ctx, p.ID, token.ID, owner); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= deploytoken.MaxTokens; i++ {
		token, _, _ = deploytoken.New(p.ID, "quota", 30, true)
		err = store.CreateDeployToken(ctx, token, owner)
		if i < deploytoken.MaxTokens && err != nil || i == deploytoken.MaxTokens && !errors.Is(err, deploytoken.ErrLimit) {
			t.Fatal("token quota", i, err)
		}
	}
	items, err = store.ListDeployTokens(ctx, p.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(ctx, p.Slug, ""); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = store.db.QueryRow("SELECT count(*) FROM deploy_tokens WHERE project_id=$1", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("project cascade failed", err)
	}
	if len(items) != deploytoken.MaxTokens {
		t.Fatal("wrong stored count")
	}
}
