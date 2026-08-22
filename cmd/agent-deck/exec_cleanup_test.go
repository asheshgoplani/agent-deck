package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStopExecWorkersHoistsWebShutdownAndWaitsForMaintenance(t *testing.T) {
	maintenanceDone := make(chan struct{})
	maintenanceCanceled := make(chan struct{})
	webStopped := false
	done := make(chan error, 1)
	go func() {
		done <- stopExecWorkers(execCleanupTasks{
			maintenanceCancel: func() { close(maintenanceCanceled) },
			maintenanceDone:   maintenanceDone,
			webShutdown:       func() error { webStopped = true; return nil },
		})
	}()
	<-maintenanceCanceled
	time.Sleep(10 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("cleanup returned before maintenance worker completed")
	default:
	}
	close(maintenanceDone)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !webStopped {
		t.Fatal("live web server shutdown was not hoisted")
	}
}

func TestStopExecWorkersWaitsForEverySlowWorker(t *testing.T) {
	tests := []struct {
		name string
		set  func(*execCleanupTasks, func(), <-chan struct{})
	}{
		{"maintenance", func(tasks *execCleanupTasks, cancel func(), done <-chan struct{}) {
			tasks.maintenanceCancel, tasks.maintenanceDone = cancel, done
		}},
		{"pricing fetcher", func(tasks *execCleanupTasks, cancel func(), done <-chan struct{}) {
			tasks.fetchCancel, tasks.fetchDone = cancel, done
		}},
		{"cost watcher", func(tasks *execCleanupTasks, cancel func(), done <-chan struct{}) {
			tasks.costWatcherStop, tasks.costWatcherDone = cancel, done
		}},
		{"cost consumer", func(tasks *execCleanupTasks, cancel func(), done <-chan struct{}) {
			tasks.costWatcherStop, tasks.costConsumerDone = cancel, done
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canceled := make(chan struct{})
			exited := make(chan struct{})
			tasks := execCleanupTasks{workerWaitTimeout: time.Second}
			tt.set(&tasks, func() { close(canceled) }, exited)
			done := make(chan error, 1)
			go func() { done <- stopExecWorkers(tasks) }()
			<-canceled
			select {
			case err := <-done:
				t.Fatalf("cleanup returned before slow worker exited: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(exited)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStopExecWorkersReportsHungCostWorker(t *testing.T) {
	err := stopExecWorkers(execCleanupTasks{
		fetchCancel:       func() {},
		fetchDone:         make(chan struct{}),
		workerWaitTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "pricing fetcher") {
		t.Fatalf("error = %v, want loud pricing-fetcher timeout", err)
	}
}

func TestStopExecWorkersReturnsWebShutdownError(t *testing.T) {
	want := errors.New("stuck")
	err := stopExecWorkers(execCleanupTasks{webShutdown: func() error { return want }})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}
