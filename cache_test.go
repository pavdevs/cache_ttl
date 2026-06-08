package main

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// На исходном (багованном) коде часть этих тестов падает — это нормально:
// они и есть спецификация ожидаемого поведения. Запускайте так:
//
//	go test -race ./...
//
// После исправлений все тесты должны проходить, включая под -race.

func TestSetGet(t *testing.T) {
	c := New(time.Second)
	c.Set("k", 42, time.Minute)

	v, err := c.Get("k")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 42 {
		t.Fatalf("got %v, want 42", v)
	}
}

func TestNotFound(t *testing.T) {
	c := New(time.Second)
	if _, err := c.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestExpiration(t *testing.T) {
	c := New(time.Hour) // janitor не вмешивается — проверяем ленивую инвалидацию
	c.Set("k", "v", 50*time.Millisecond)

	time.Sleep(120 * time.Millisecond)

	if _, err := c.Get("k"); !errors.Is(err, ErrExpired) {
		t.Fatalf("got %v, want ErrExpired", err)
	}
}

func TestNoExpiry(t *testing.T) {
	c := New(time.Hour)
	c.Set("k", "v", 0) // 0 == хранить вечно

	time.Sleep(50 * time.Millisecond)

	if _, err := c.Get("k"); err != nil {
		t.Fatalf("key with ttl=0 must not expire, got %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New(5 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "key"
			c.Set(key, n, 10*time.Millisecond)
			_, _ = c.Get(key)
			_ = c.Len()
		}(i)
	}
	wg.Wait()
}