package web

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestServerOnListeningRunsOnceBound(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	srv := NewServer(Config{ListenAddr: addr})
	called := make(chan struct{}, 2)
	srv.SetOnListening(func() { called <- struct{}{} })
	done := make(chan error, 1)
	go func() { done <- srv.Start() }()

	select {
	case <-called:
	case err := <-done:
		t.Fatalf("Start returned before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("onListening not called")
	}
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("onListening ran but %s is not accepting: %v", addr, err)
	}
	c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	if err := <-done; err != nil {
		t.Fatalf("Start after Shutdown: %v", err)
	}
	if len(called) != 0 {
		t.Fatal("onListening called more than once")
	}
}

func TestServerOnListeningNotCalledWhenBindFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := NewServer(Config{ListenAddr: ln.Addr().String()})
	called := false
	srv.SetOnListening(func() { called = true })
	if err := srv.Start(); err == nil {
		t.Fatal("Start succeeded on an occupied address")
	}
	if called {
		t.Fatal("onListening called although binding failed")
	}
}
