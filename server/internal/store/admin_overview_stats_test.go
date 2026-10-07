package store

import (
	"fmt"
	"testing"
)

func TestAdminOverviewTrendDailyBoundariesAndDistinctUsers(t *testing.T) {
	db, ctx := openUsageStatsTestDB(t)
	const start = int64(1775001600)
	const day = int64(86400)
	end := start + 2*day + 3600
	for index, timestamp := range []int64{start, start + 2*day, start - 1, end} {
		id := fmt.Sprintf("overview-user-%d", index)
		seedUsageStatsUser(t, ctx, db, id, id+"@example.test")
		if _, err := db.ExecContext(ctx, `UPDATE users SET created_at=? WHERE id=?`, timestamp, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		user, message string
		timestamp     int64
	}{
		{"overview-user-0", "turn-1", start},
		{"overview-user-0", "turn-1", start + 1},
		{"overview-user-0", "turn-2", start + 2*day},
		{"overview-user-1", "turn-3", end - 1},
		{"overview-user-0", "outside-start", start - 1},
		{"overview-user-0", "outside-end", end},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO usage_stats
			(user_id,message_id,model_id,purpose,input_tokens,output_tokens,cost,credits,created_at)
			VALUES(?,?,'overview-model','chat',100,20,0.125,2,?)`, row.user, row.message, row.timestamp); err != nil {
			t.Fatal(err)
		}
	}
	points, err := AdminOverviewTrendBetween(ctx, db, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 {
		t.Fatalf("got %d points, want 3", len(points))
	}
	if first := points[0]; first.BucketStart != start || first.Registrations != 1 || first.Users != 1 || first.Turns != 1 || first.InputTokens != 200 || first.OutputTokens != 40 || first.Cost != 0.25 || first.Credits != 4 {
		t.Fatalf("unexpected first day: %+v", first)
	}
	if middle := points[1]; middle.BucketStart != start+day || middle.Users != 0 || middle.Calls != 0 || middle.Registrations != 0 {
		t.Fatalf("zero-activity day missing: %+v", middle)
	}
	if last := points[2]; last.Users != 2 || last.Turns != 2 || last.Registrations != 1 {
		t.Fatalf("unexpected partial final day: %+v", last)
	}
	totals, err := AdminUsageTotalsBetween(ctx, db, start, end)
	if err != nil || totals.Users != 2 {
		t.Fatalf("period users must be deduplicated: %+v, %v", totals, err)
	}
}

func TestAdminOverviewTrendWithoutActivity(t *testing.T) {
	db, ctx := openUsageStatsTestDB(t)
	points, err := AdminOverviewTrendBetween(ctx, db, 86400, 4*86400)
	if err != nil || len(points) != 3 {
		t.Fatalf("empty timeline: %+v, %v", points, err)
	}
	for _, point := range points {
		if point.Registrations != 0 || point.Calls != 0 {
			t.Fatalf("expected empty bucket: %+v", point)
		}
	}
}
