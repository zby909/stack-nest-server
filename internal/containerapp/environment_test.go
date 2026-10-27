package containerapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"stacknest/server/internal/storage"
)

// envValue 构造补丁中的字符串值，区别于删除标记 nil。
func envValue(value string) *string { return &value }

// TestSaveEnvironmentPatch 验证合并后加密保存、保留未改值及公开响应不回显。
func TestSaveEnvironmentPatch(t *testing.T) {
	secret, id := "environment-patch-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
	spec := testSpec()
	spec.Env = map[string]string{"KEEP": "keep-secret", "CHANGE": "old-secret", "DELETE": "delete-secret", "EMPTY": "not-empty"}
	sealed, err := seal(secret, id, spec)
	if err != nil {
		t.Fatal(err)
	}
	store := &testStore{value: App{ID: id, Slug: "env-test", Generation: 7, Sealed: sealed}}
	service := New(store, &testRuntime{}, secret, storage.New(t.TempDir()), nil)
	input := Input{Name: "Env test", Spec: testSpec(), EnvPatch: map[string]*string{
		"CHANGE": envValue("new-secret"), "ADD": envValue("added-secret"), "EMPTY": envValue(""), "DELETE": nil, "MISSING": nil,
	}}
	input.Env = nil
	view, err := service.Save(context.Background(), id, Actor{}, input, 7)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := open(secret, id, store.value.Sealed)
	want := map[string]string{"KEEP": "keep-secret", "CHANGE": "new-secret", "ADD": "added-secret", "EMPTY": ""}
	if err != nil || !reflect.DeepEqual(saved.Env, want) {
		t.Fatal("incorrect saved environment", err)
	}
	if !reflect.DeepEqual(view.EnvKeys, []string{"ADD", "CHANGE", "EMPTY", "KEEP"}) {
		t.Fatal("incorrect public keys", view.EnvKeys)
	}
	raw, _ := json.Marshal(view)
	for _, value := range []string{"keep-secret", "new-secret", "added-secret", "old-secret", sealed, store.value.Sealed} {
		if strings.Contains(string(raw), value) {
			t.Fatal("public view leaked a secret")
		}
	}
}

// TestSaveRejectsInvalidEnvironmentPatch 保证歧义、过量、无效和过期补丁不触碰已存配置。
func TestSaveRejectsInvalidEnvironmentPatch(t *testing.T) {
	cases := []struct {
		name          string
		change        func(*Input)
		create, stale bool
	}{
		{"replace-conflict", func(i *Input) { i.ReplaceEnv = true }, false, false},
		{"env-conflict", func(i *Input) { i.Env = map[string]string{"OTHER": "value"} }, false, false},
		{"invalid-delete-key", func(i *Input) { i.EnvPatch["BAD=KEY"] = nil }, false, false},
		{"oversized-value", func(i *Input) { i.EnvPatch["KEY"] = envValue(strings.Repeat("x", 4097)) }, false, false},
		{"nul-value", func(i *Input) { i.EnvPatch["KEY"] = envValue("x\x00") }, false, false},
		{"merged-count", func(i *Input) {
			for n := range 64 {
				i.EnvPatch[fmt.Sprintf("KEY%d", n)] = envValue("x")
			}
		}, false, false},
		{"patch-count", func(i *Input) {
			for n := range 65 {
				i.EnvPatch[fmt.Sprintf("KEY%d", n)] = nil
			}
		}, false, false},
		{"merged-size", func(i *Input) {
			for n := range 4 {
				i.EnvPatch[fmt.Sprintf("KEY%d", n)] = envValue(strings.Repeat("x", 4096))
			}
		}, false, false},
		{"create", func(*Input) {}, true, false},
		{"stale", func(*Input) {}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secret, id := "environment-patch-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
			sealed, _ := seal(secret, id, testSpec())
			store := &testStore{value: App{ID: id, Slug: "env-test", Generation: 7, Sealed: sealed}}
			service := New(store, &testRuntime{}, secret, storage.New(t.TempDir()), nil)
			input := Input{Name: "Env test", Slug: "env-test", Spec: testSpec(), EnvPatch: map[string]*string{}}
			input.Env = nil
			tc.change(&input)
			generation := int64(7)
			if tc.create {
				id = ""
			}
			if tc.stale {
				generation--
			}
			_, err := service.Save(context.Background(), id, Actor{}, input, generation)
			want := ErrInvalid
			if tc.stale {
				want = ErrBusy
			}
			if !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
			if store.value.Sealed != sealed {
				t.Fatal("rejected input changed saved configuration")
			}
		})
	}
}

// TestPatchEnvironmentDoesNotMutateSource 保证删除与赋值不会改动旧版本配置。
func TestPatchEnvironmentDoesNotMutateSource(t *testing.T) {
	current := map[string]string{"KEEP": "old"}
	merged, err := patchEnvironment(current, map[string]*string{"KEEP": nil, "ADD": envValue("")})
	if err != nil || current["KEEP"] != "old" || len(current) != 1 || len(merged) != 1 {
		t.Fatal("source changed", err)
	}
}
