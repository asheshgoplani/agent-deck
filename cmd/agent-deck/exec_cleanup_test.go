package main

import (
	"errors"
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

func TestStopExecWorkersReturnsWebShutdownError(t *testing.T) {
	want := errors.New("stuck")
	err := stopExecWorkers(execCleanupTasks{webShutdown: func() error { return want }})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}
