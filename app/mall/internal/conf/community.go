package conf

import "fmt"

// Zero means the documented default, not disabled/unlimited. Reject overflow
// and accidentally unbounded retention, queues, or rate-limit windows at boot.
func ValidateCommunity(c *Community) error {
	fields := []struct {
		name       string
		value, max int32
	}{
		{"history_retention_days", c.GetHistoryRetentionDays(), 3650},
		{"cleanup_batch_size", c.GetCleanupBatchSize(), 1000},
		{"maintenance_workers", c.GetMaintenanceWorkers(), 32},
		{"media_workers", c.GetMediaWorkers(), 32},
		{"posts_per_minute", c.GetPostsPerMinute(), 10000},
		{"comments_per_minute", c.GetCommentsPerMinute(), 10000},
		{"uploads_per_minute", c.GetUploadsPerMinute(), 10000},
		{"interactions_per_minute", c.GetInteractionsPerMinute(), 10000},
	}
	for _, f := range fields {
		if f.value < 0 || f.value > f.max {
			return fmt.Errorf("community.%s must be between 0 (default) and %d", f.name, f.max)
		}
	}
	return nil
}
