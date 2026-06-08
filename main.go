// Package main реализует простой in-memory кэш с TTL (time-to-live).
//
// ТЕСТОВАЯ ЗАДАЧА ДЛЯ КАНДИДАТА
// ----------------------------
// Перед вами реализация потокобезопасного кэша с истечением срока жизни
// записей. Код компилируется и на первый взгляд работает, но содержит
// несколько дефектов: где-то ломается конкурентный доступ, где-то записи
// никогда не протухают, где-то утекают ресурсы.
//
// Что нужно сделать:
//  1. Запустить программу (`go run main.go`) и тесты (`go test -race ./...`).
//  2. Найти и описать все дефекты.
//  3. Исправить их, сохранив публичный API (New / Set / Get / Delete / Len).
//  4. Объяснить, как каждый баг проявился бы в проде.
//
// Подсказка: запускайте с детектором гонок (`-race`) и подумайте о единицах
// измерения времени, о том, под каким локом выполняется запись в map, и о
// судьбе фоновой горутины.
package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Ошибки, которые возвращает кэш.
var (
	// ErrNotFound — ключа нет в кэше.
	ErrNotFound = errors.New("cache: key not found")
	// ErrExpired — ключ был, но его TTL истёк.
	ErrExpired = errors.New("cache: key expired")
)

// item — одна запись кэша.
type item struct {
	value      any
	expiration int64 // момент истечения, наносекунды от epoch (0 — без срока)
}

// Cache — потокобезопасный in-memory кэш с TTL и фоновой очисткой.
type Cache struct {
	mu      sync.RWMutex
	items   map[string]item
	cleanup time.Duration
}

// New создаёт кэш и запускает фоновую очистку протухших записей
// с интервалом cleanup.
func New(cleanup time.Duration) *Cache {
	c := &Cache{
		items:   make(map[string]item),
		cleanup: cleanup,
	}
	go c.janitor()
	return c
}

// Set кладёт значение в кэш с временем жизни ttl.
// ttl == 0 означает «хранить без срока истечения».
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var exp int64
	if ttl > 0 {
		// БАГ #1: смешение единиц измерения времени.
		// item.expiration сравнивается с UnixNano() (см. Get), но здесь
		// берётся Unix() (СЕКУНДЫ) и к нему прибавляется ttl в наносекундах.
		// Итог: expiration на ~9 порядков меньше, чем текущий UnixNano,
		// поэтому ЛЮБАЯ запись считается протухшей сразу же.
		exp = time.Now().Unix() + int64(ttl)
	}

	c.items[key] = item{
		value:      value,
		expiration: exp,
	}
}

// Get возвращает значение по ключу.
// Возвращает ErrNotFound, если ключа нет, и ErrExpired, если TTL истёк.
func (c *Cache) Get(key string) (any, error) {
	// БАГ #2: метод берёт ТОЛЬКО read-lock, но ниже выполняет запись в map
	// (delete) при ленивой инвалидации. Запись в map под RLock — это гонка
	// данных и потенциальный "concurrent map writes" → паника.
	c.mu.RLock()
	defer c.mu.RUnlock()

	it, ok := c.items[key]
	if !ok {
		return nil, ErrNotFound
	}

	// БАГ #3: строгое '>' (граничный момент считается живым, off-by-one).
	// Само сравнение наносекунд с наносекундами корректно, но из-за БАГА #1
	// expiration записан в неверной единице — поэтому здесь всё «протухло».
	if it.expiration > 0 && time.Now().UnixNano() > it.expiration {
		delete(c.items, key) // запись в map под read-lock — см. БАГ #2
		return nil, ErrExpired
	}

	return it.value, nil
}

// Delete удаляет ключ из кэша.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Len возвращает текущее количество записей в кэше.
func (c *Cache) Len() int {
	// БАГ #4: чтение map без какого-либо лока — гонка с Set/Delete/janitor.
	return len(c.items)
}

// janitor периодически вычищает протухшие записи.
func (c *Cache) janitor() {
	// БАГ #5: горутина живёт вечно. Нет канала остановки и нет метода Close —
	// при пересоздании кэшей в долгоживущем процессе горутины утекают.
	ticker := time.NewTicker(c.cleanup)
	for range ticker.C {
		now := time.Now().UnixNano()
		// БАГ #6: проход и удаление по map БЕЗ лока — гонка данных с Get/Set
		// и риск "concurrent map iteration and map write".
		for key, it := range c.items {
			if it.expiration > 0 && now > it.expiration {
				delete(c.items, key)
			}
		}
	}
}

func main() {
	c := New(100 * time.Millisecond)

	c.Set("a", 1, 200*time.Millisecond)
	c.Set("b", "hello", 0) // без срока

	if v, err := c.Get("a"); err == nil {
		fmt.Printf("a = %v\n", v)
	}

	// Ждём дольше, чем TTL ключа "a": ожидаем ErrExpired.
	time.Sleep(400 * time.Millisecond)

	if _, err := c.Get("a"); err != nil {
		fmt.Printf("a после TTL: %v\n", err)
	} else {
		fmt.Println("a после TTL: всё ещё живо (это баг!)")
	}

	if v, err := c.Get("b"); err == nil {
		fmt.Printf("b = %v\n", v)
	}

	if _, err := c.Get("missing"); err != nil {
		fmt.Printf("missing: %v\n", err)
	}

	// Небольшая конкурентная нагрузка — под `-race` всплывут гонки.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", n%5)
			c.Set(key, n, 50*time.Millisecond)
			_, _ = c.Get(key)
		}(i)
	}
	wg.Wait()

	fmt.Printf("размер кэша: %d\n", c.Len())
}