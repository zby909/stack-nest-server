package activity

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestQueryBeforePagination 验证历史范围、权限过滤、组合筛选和分页边界。
func TestQueryBeforePagination(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 145; i++ {
		owner := "mine"
		if i >= 125 {
			owner = "other"
		}
		if err := store.Append(Event{ID: fmt.Sprintf("event-%03d", i), CreatedAt: start.Add(time.Duration(i) * time.Minute), ActorID: owner, Actor: owner + "@example.com", Slug: "all", Action: "pause", Status: "succeeded", Message: "保留的记录"}); err != nil {
			t.Fatal(err)
		}
	}
	query := Query{UserID: "mine", Page: 3, PageSize: 50}
	page, err := store.Query(context.Background(), query)
	if err != nil || page.Total != 125 || len(page.Events) != 25 || page.Events[0].ID != "event-024" {
		t.Fatalf("visibility/page: %+v %v", page, err)
	}
	query.Page, query.Slug, query.Actor, query.Action, query.Status, query.Keyword = 1, "all", "MINE@example.com", "pause", "succeeded", "保留"
	query.From, query.To = start.Add(5*time.Minute), start.Add(8*time.Minute)
	page, err = store.Query(context.Background(), query)
	if err != nil || page.Total != 3 || page.Events[0].ID != "event-007" || page.Events[2].ID != "event-005" {
		t.Fatalf("combined range: %+v %v", page, err)
	}
	query.Actor = "other@example.com"
	page, _ = store.Query(context.Background(), query)
	if page.Total != 0 {
		t.Fatal("foreign logs leaked")
	}
	for _, q := range []Query{{Page: 0, PageSize: 50}, {Page: 1, PageSize: 101}, {Page: 1, PageSize: 50, Action: "invalid"}, {Page: 1, PageSize: 50, From: start, To: start}} {
		if _, err := store.Query(context.Background(), q); err == nil {
			t.Fatal("accepted invalid query")
		}
	}
}
