package main

import (
	"log"
	"sync"
)

// logMu is acquired before log.Printf creates its timestamp. Besides keeping a
// line intact, this prevents concurrent controllers from printing timestamps
// out of order.
var logMu sync.Mutex

func logf(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	log.Printf(format, args...)
}
