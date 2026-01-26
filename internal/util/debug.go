package util

import (
	"fmt"
	"log"
	"os"
	"sync"
)

var (
	debugLogger *log.Logger
	debugFile   *os.File
	mu          sync.Mutex
)

// InitDebugLogger opens the debug file and initializes the logger
func InitDebugLogger(filename string) error {
	mu.Lock()
	defer mu.Unlock()

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	debugFile = f
	debugLogger = log.New(f, "[DEBUG] ", log.LstdFlags|log.Lmicroseconds)
	return nil
}

// CloseDebugLogger closes the file handle
func CloseDebugLogger() {
	mu.Lock()
	defer mu.Unlock()
	if debugFile != nil {
		debugFile.Close()
		debugFile = nil
	}
}

// Debug logs a message if the logger is initialized
func Debug(format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	if debugLogger != nil {
		// Ensure a newline
		msg := fmt.Sprintf(format, args...)
		debugLogger.Output(2, msg)
	}
}
