package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// ILogger is a basic logging interface for ContextualMain.
type ILogger interface {
	Debug(...interface{})
	Error(...interface{})
	Info(...interface{})
	Warn(...interface{})
	Fatal(...interface{})
}

// ContextualMain calls a main entry point function with a cancellable
// context via SIGTERM. This should be called once per process so as
// to not clobber the signals from Notify.
func ContextualMain[L ILogger](main func(ctx context.Context, args []string, logger L) error, logger L) {
	// This will only run on a successful exit due to the fatal error
	// logic in contextualMain.
	defer func() {
		if err := FindGoroutineLeaks(); err != nil {
			fmt.Fprintf(os.Stderr, "goroutine leak(s) detected: %v\n", err)
		}
	}()
	contextualMain(main, false, logger)
}

// ContextualMainQuit is the same as ContextualMain but catches quit signals into the provided
// context accessed via ContextMainQuitSignal.
func ContextualMainQuit[L ILogger](main func(ctx context.Context, args []string, logger L) error, logger L) {
	contextualMain(main, true, logger)
}

// contextualMain is the core implementation for context-aware main functions
func contextualMain[L ILogger](main func(ctx context.Context, args []string, logger L) error, quitSignal bool, logger L) {
	// Set up basic cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up interrupt signal handling
	interruptC := make(chan os.Signal, 1)
	signal.Notify(interruptC, os.Interrupt, syscall.SIGTERM)

	// Monitor for interrupt signals and cancel context when received
	go func() {
		select {
		case <-interruptC:
			cancel()
		case <-ctx.Done():
			// Context was cancelled elsewhere
		}
	}()

	// Set up quit signal handling if requested
	var quitC chan os.Signal
	if quitSignal {
		quitC = make(chan os.Signal, 1)
		ctx = ContextWithQuitSignal(ctx, quitC)
	}

	// Set up USR1 signal for stack traces
	usr1C := make(chan os.Signal, 1)

	var signalWatcher sync.WaitGroup
	signalWatcher.Add(1)
	defer signalWatcher.Wait()

	// Set up stack trace dumping goroutine
	ManagedGo(func() {
		for {
			if !SelectContextOrWaitChan(ctx, usr1C) {
				return
			}
			buf := make([]byte, 1024)
			for {
				n := runtime.Stack(buf, true)
				if n < len(buf) {
					buf = buf[:n]
					break
				}
				buf = make([]byte, 2*len(buf))
			}
			logger.Warn(string(buf))
		}
	}, signalWatcher.Done)

	// Set up ready channel and context
	readyC := make(chan struct{})
	readyCtx := ContextWithReadyFunc(ctx, readyC)

	// Run main function and handle errors
	if err := FilterOutError(main(readyCtx, os.Args, logger), context.Canceled); err != nil {
		fatal(logger, err)
	}
}

// SelectContextOrWaitChan returns true if the channel received before the context was done
func SelectContextOrWaitChan(ctx context.Context, ch <-chan os.Signal) bool {
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	}
}

// ContextWithQuitSignal returns a context that includes a quit signal channel
func ContextWithQuitSignal(ctx context.Context, quitC <-chan os.Signal) context.Context {
	return context.WithValue(ctx, quitSignalKey{}, quitC)
}

// quitSignalKey is a type used as a key for storing quit signal channel in context
type quitSignalKey struct{}

// ContextMainQuitSignal extracts the quit signal channel from a context
func ContextMainQuitSignal(ctx context.Context) <-chan os.Signal {
	v := ctx.Value(quitSignalKey{})
	if v == nil {
		return nil
	}
	return v.(<-chan os.Signal)
}

// ContextWithReadyFunc returns a context with a ready function embedded
func ContextWithReadyFunc(ctx context.Context, readyC chan struct{}) context.Context {
	return context.WithValue(ctx, readyFuncKey{}, readyC)
}

// readyFuncKey is a type used as a key for storing ready channel in context
type readyFuncKey struct{}

// ContextMainReady signals that the main function is ready
func ContextMainReady(ctx context.Context) {
	v := ctx.Value(readyFuncKey{})
	if v == nil {
		return
	}
	close(v.(chan struct{}))
}

// FilterOutError filters out a specific error from being returned
func FilterOutError(err, filterErr error) error {
	if err == filterErr {
		return nil
	}
	fmt.Println("ERRORRR? ", err)
	return err
}

// ManagedGo runs a function in a goroutine with panic recovery and done callback
func ManagedGo(f func(), done func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Log the panic if needed
				fmt.Fprintf(os.Stderr, "Panic in managed goroutine: %v\n%s", r, debug.Stack())
			}
			if done != nil {
				done()
			}
		}()
		f()
	}()
}

// fatal logs a fatal error and exits
func fatal[L ILogger](logger L, err error) {
	// Assuming the logger has a method like Error or Fatal
	// This might need adjustment based on your actual ILogger interface
	logger.Error(err)
	os.Exit(1)
}

// FindGoroutineLeaks checks for goroutines that may be leaking by
// examining the current goroutine stack trace for suspicious patterns.
func FindGoroutineLeaks() error {
	// Get all goroutine stacks
	buf := make([]byte, 2<<20) // 2MB buffer
	n := runtime.Stack(buf, true)
	if n == len(buf) {
		// The buffer might have been too small
		return fmt.Errorf("stack buffer size of %d may be too small", len(buf))
	}

	stacks := string(buf[:n])

	// Count goroutines
	grCount := countGoroutines(stacks)

	// Look for suspicious patterns
	var leaks []string

	// Check for commonly leaked goroutines
	if blocked := findBlockedOnChannels(stacks); len(blocked) > 0 {
		leaks = append(leaks, fmt.Sprintf("%d goroutines blocked on channels", blocked))
	}

	if waiting := findWaitingOnWaitGroups(stacks); len(waiting) > 0 {
		leaks = append(leaks, fmt.Sprintf("%d goroutines waiting on sync.WaitGroup", waiting))
	}

	// Check for high number of goroutines (potential leak)
	if grCount > 100 { // Threshold may need adjustment based on your application
		leaks = append(leaks, fmt.Sprintf("high number of goroutines: %d", grCount))
	}

	// If any leaks detected, return error
	if len(leaks) > 0 {
		return fmt.Errorf("potential goroutine leaks detected: %s", strings.Join(leaks, ", "))
	}

	return nil
}

// countGoroutines counts the number of goroutines in a stack trace
func countGoroutines(stacks string) int {
	return strings.Count(stacks, "goroutine ")
}

// findBlockedOnChannels looks for goroutines blocked on channel operations
func findBlockedOnChannels(stacks string) []int {
	var blocked []int
	lines := strings.Split(stacks, "\n")

	for i, line := range lines {
		if strings.Contains(line, "goroutine ") {
			// Extract goroutine ID
			id := extractGoroutineID(line)

			// Check if any of the next lines indicate channel blocking
			for j := i + 1; j < len(lines) && j < i+20; j++ {
				if strings.Contains(lines[j], "chan send") ||
					strings.Contains(lines[j], "chan receive") {
					blocked = append(blocked, id)
					break
				}
			}
		}
	}

	return blocked
}

// findWaitingOnWaitGroups looks for goroutines waiting on sync.WaitGroup
func findWaitingOnWaitGroups(stacks string) []int {
	var waiting []int
	lines := strings.Split(stacks, "\n")

	for i, line := range lines {
		if strings.Contains(line, "goroutine ") {
			// Extract goroutine ID
			id := extractGoroutineID(line)

			// Check if any of the next lines indicate WaitGroup waiting
			for j := i + 1; j < len(lines) && j < i+20; j++ {
				if strings.Contains(lines[j], "sync.WaitGroup.Wait") {
					waiting = append(waiting, id)
					break
				}
			}
		}
	}

	return waiting
}

// extractGoroutineID extracts the goroutine ID from a goroutine header line
func extractGoroutineID(line string) int {
	// Format is typically "goroutine 1 [running]:"
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		id, err := strconv.Atoi(parts[1])
		if err == nil {
			return id
		}
	}
	return 0
}
