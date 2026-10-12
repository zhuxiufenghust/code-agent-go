package ltm

import (
	"context"
	"testing"
)

func TestStoreAndSearch(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	it := Item{Kind: "convention", Content: "本项目禁止直接 push main，必须走 PR", Tags: []string{"git", "流程"}}
	if err := store.Save(ctx, &it); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if it.ID == 0 {
		t.Fatal("保存后应回填自增 ID")
	}

	// 命中多个关键词的条目排前面（这一条只命中 "流程" 一个词）
	if err := store.Save(ctx, &Item{Kind: "pitfall", Content: "走流程：跑测试前要先 make build"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Search(ctx, "PR main 流程", 10)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望命中 2 条, 实际 %d 条", len(got))
	}
	if got[0].ID != it.ID {
		t.Fatalf("命中词更多的条目应排第一, 实际首位 id=%d", got[0].ID)
	}
	if got[0].Kind != "convention" || len(got[0].Tags) != 2 {
		t.Fatalf("回读的条目内容不符: %+v", got[0])
	}
}

func TestSearchRespectsLimitAndEmptyQuery(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := store.Save(ctx, &Item{Content: "同一条 记忆内容"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Search(ctx, "记忆", 2)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit 未生效: %d 条", len(got))
	}

	// 空查询不该变成"返回全部"，否则模型一次 recall 会把整个库塞进上下文。
	none, err := store.Search(ctx, "   ", 10)
	if err != nil {
		t.Fatalf("空查询不该报错: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("空查询应返回空结果, 实际 %d 条", len(none))
	}
}

func TestDelete(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	it := Item{Content: "待删除的记忆"}
	if err := store.Save(ctx, &it); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, it.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	got, err := store.Search(ctx, "删除", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("删除后仍能检索到: %+v", got)
	}
}

func TestSaveRejectsEmptyContent(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()
	if err := store.Save(context.Background(), &Item{Content: "   "}); err == nil {
		t.Fatal("空内容应当被拒绝")
	}
}

func newTestStore(t *testing.T) (*SQLiteStore, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatalf("创建 LTM 库失败: %v", err)
	}
	return store, func() { store.Close() }
}
