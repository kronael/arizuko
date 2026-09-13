//go:build race

package msgsearch

// raceDetector is what the measurement bodies read to know that a number taken
// here would be the instrumentation's and not the index's: the same walk is
// 283 ms without -race and 1.5 s with it.
const raceDetector = true
