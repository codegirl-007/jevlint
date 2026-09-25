package good

// Retry delays avoid synchronizing clients after a shared outage.
func RetryDelay(attempt int) int {
	return attempt * attempt
}
