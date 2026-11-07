package main

import "time"

// worstCaseWall is what the selected cases can cost end to end: each case's own
// wall cap plus the bounded wait for its turn to settle before the next one is
// injected. Cases run strictly sequentially into one folder, so this sums.
func worstCaseWall(caseWallMs []int, settle time.Duration) time.Duration {
	total := time.Duration(0)
	for _, ms := range caseWallMs {
		total += time.Duration(ms)*time.Millisecond + settle
	}
	return total
}
