package store

import (
	"context"
	"database/sql"
)

type OverviewTrendPoint struct {
	UsageBucket
	Registrations int `json:"registrations"`
}

// AdminOverviewTrendBetween combines usage and registrations on the same daily
// timeline, including zero-activity days. The caller supplies UTC day boundaries.
func AdminOverviewTrendBetween(ctx context.Context, db *sql.DB, since, before int64) ([]OverviewTrendPoint, error) {
	const day = int64(86400)
	usage, err := AdminUsageTrendBetween(ctx, db, since, before, day)
	if err != nil {
		return nil, err
	}
	byDay := make(map[int64]OverviewTrendPoint, len(usage))
	for _, point := range usage {
		byDay[point.BucketStart] = OverviewTrendPoint{UsageBucket: point}
	}
	rows, err := db.QueryContext(ctx, `
		SELECT ((created_at - ?) / ?) * ? + ?, COUNT(*)
		FROM users WHERE created_at >= ? AND created_at < ?
		GROUP BY 1`, since, day, day, since, since, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var timestamp int64
		var count int
		if err := rows.Scan(&timestamp, &count); err != nil {
			return nil, err
		}
		point := byDay[timestamp]
		point.Registrations = count
		byDay[timestamp] = point
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	points := make([]OverviewTrendPoint, 0)
	for timestamp := since; timestamp < before; timestamp += day {
		point := byDay[timestamp]
		point.BucketStart = timestamp
		points = append(points, point)
	}
	return points, nil
}
