package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestInsertMessageKeepsNewest(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)
	var last Message
	for i := range MaxMessages + 3 {
		m, err := db.InsertMessage(ctx, Message{Text: fmt.Sprintf("m%d", i), CreatedBy: "agent:public:c", CreatedAt: t0.Add(time.Duration(i) * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if m.ID == 0 || m.Text != fmt.Sprintf("m%d", i) || !m.CreatedAt.Equal(t0.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("stored %+v", m)
		}
		last = m
	}
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil || n != MaxMessages {
		t.Fatalf("count = %d err %v, want %d", n, err, MaxMessages)
	}
	got, err := db.RecentMessages(ctx, t0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0].ID != last.ID || got[0].Text != last.Text || got[4].Text != fmt.Sprintf("m%d", MaxMessages-2) {
		t.Fatalf("recent = %+v", got)
	}
	if _, err := db.InsertMessage(ctx, Message{ID: 7, Text: "x", CreatedBy: "agent:public:c", CreatedAt: t0}); err == nil {
		t.Fatal("non-zero id: want error")
	}
}

func TestRecentMessagesSince(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{t0.Add(-time.Second), t0, t0.Add(time.Hour)} {
		if _, err := db.InsertMessage(ctx, Message{Text: fmt.Sprint(i), CreatedBy: "agent:public:c", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.RecentMessages(ctx, t0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "2" || got[1].Text != "1" {
		t.Fatalf("recent = %+v", got)
	}
}
