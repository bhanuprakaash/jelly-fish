package worker

import "time"

// SetTitleTimeout bounds each Title call to d; zero or less turns Titles off,
// which keeps tests that count provider calls or events independent of them.
func SetTitleTimeout(w *Worker, d time.Duration) { w.titleTimeout = d }
