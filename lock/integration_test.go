package lock

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFairLock(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.Fair = true
	config.WaitTimeout = 3 * time.Second
	config.AutoRenewal = false

	lockName := "test_fair_lock"
	ctx := context.Background()

	t.Run("Fair Lock Queue Order", func(t *testing.T) {
		const numGoroutines = 5
		order := make([]int, 0, numGoroutines)
		var orderMu sync.Mutex
		var wg sync.WaitGroup

		// 创建第一个锁并持有
		firstLock := NewFairLock(client, lockName, config)
		defer firstLock.Delete(ctx)

		err := firstLock.Lock(ctx)
		require.NoError(t, err)

		// 启动多个协程按顺序尝试获取锁
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				lock := NewFairLock(client, lockName, config)

				// 稍微错开启动时间，确保顺序
				time.Sleep(time.Duration(id) * 10 * time.Millisecond)

				err := lock.Lock(ctx)
				if err != nil {
					return
				}

				orderMu.Lock()
				order = append(order, id)
				orderMu.Unlock()

				// 持有锁一小段时间
				time.Sleep(50 * time.Millisecond)

				lock.Unlock(ctx)
			}(i)
		}

		// 短暂等待所有协程开始等待
		time.Sleep(200 * time.Millisecond)

		// 检查队列长度
		queueLength, err := firstLock.GetQueueLength(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, queueLength, int64(3)) // 至少有几个在排队

		hasQueued, err := firstLock.HasQueuedThreads(ctx)
		require.NoError(t, err)
		assert.True(t, hasQueued)

		// 释放第一个锁，让其他协程按顺序获取
		err = firstLock.Unlock(ctx)
		require.NoError(t, err)

		wg.Wait()

		// 验证顺序（应该大致按 FIFO 顺序）
		require.Greater(t, len(order), 0)

		// 由于公平锁的特性，顺序应该是递增的（允许少量乱序）
		disorder := 0
		for i := 1; i < len(order); i++ {
			if order[i] < order[i-1] {
				disorder++
			}
		}

		// 允许少量乱序（< 20%）
		assert.Less(t, disorder, len(order)/5, "Fair lock should maintain mostly FIFO order")
	})

	t.Run("Fair Lock Async", func(t *testing.T) {
		fairLock := NewFairLock(client, "test_async_fair_lock", config)
		defer fairLock.Delete(ctx)

		// 异步获取锁
		lockChan := fairLock.LockAsync(ctx)

		// 等待锁获取完成
		select {
		case err := <-lockChan:
			require.NoError(t, err)
		case <-time.After(1 * time.Second):
			t.Fatal("Lock acquisition timeout")
		}

		// 释放锁
		err := fairLock.Unlock(ctx)
		require.NoError(t, err)
	})
}

func TestReadWriteLock(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.AutoRenewal = false

	rwLock := NewReadWriteLock(client, "test_rwlock", config)
	ctx := context.Background()

	t.Run("Multiple Readers", func(t *testing.T) {
		const numReaders = 5
		var wg sync.WaitGroup
		successCount := make([]int, numReaders)

		// 启动多个读者
		for i := 0; i < numReaders; i++ {
			wg.Add(1)
			go func(readerID int) {
				defer wg.Done()

				readLock := rwLock.ReadLock()

				err := readLock.Lock(ctx)
				if err != nil {
					return
				}

				successCount[readerID] = 1

				// 持有读锁一段时间
				time.Sleep(100 * time.Millisecond)

				err = readLock.Unlock(ctx)
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()

		// 验证所有读者都成功获取了锁
		totalSuccess := 0
		for _, success := range successCount {
			totalSuccess += success
		}
		assert.Equal(t, numReaders, totalSuccess, "All readers should acquire the lock concurrently")
	})

	t.Run("Writer Exclusivity", func(t *testing.T) {
		writeLock := rwLock.WriteLock()

		// 获取写锁
		err := writeLock.Lock(ctx)
		require.NoError(t, err)

		// 尝试获取读锁应该失败
		readLock := rwLock.ReadLock()
		acquired, err := readLock.TryLock(ctx)
		require.NoError(t, err)
		assert.False(t, acquired, "Reader should not acquire lock when writer holds it")

		// 尝试获取另一个写锁应该失败
		writeLock2 := NewReadWriteLock(client, "test_rwlock", config).WriteLock()
		acquired2, err := writeLock2.TryLock(ctx)
		require.NoError(t, err)
		assert.False(t, acquired2, "Second writer should not acquire lock")

		// 释放写锁
		err = writeLock.Unlock(ctx)
		require.NoError(t, err)

		// 现在读锁应该能获取
		acquired3, err := readLock.TryLock(ctx)
		require.NoError(t, err)
		assert.True(t, acquired3, "Reader should acquire lock after writer releases")

		err = readLock.Unlock(ctx)
		require.NoError(t, err)
	})

	t.Run("Reader-Writer Priority", func(t *testing.T) {
		var readerSuccess, writerSuccess int
		var wg sync.WaitGroup

		// 先启动读者
		wg.Add(1)
		go func() {
			defer wg.Done()
			readLock := rwLock.ReadLock()

			err := readLock.Lock(ctx)
			if err == nil {
				readerSuccess = 1
				time.Sleep(200 * time.Millisecond)
				readLock.Unlock(ctx)
			}
		}()

		// 稍后启动写者
		time.Sleep(50 * time.Millisecond)
		wg.Add(1)
		go func() {
			defer wg.Done()
			writeLock := rwLock.WriteLock()

			acquired, err := writeLock.TryLockWithTimeout(ctx, 300*time.Millisecond)
			if err == nil && acquired {
				writerSuccess = 1
				writeLock.Unlock(ctx)
			}
		}()

		wg.Wait()

		assert.Equal(t, 1, readerSuccess, "Reader should succeed")
		assert.Equal(t, 1, writerSuccess, "Writer should succeed after reader releases")
	})

	t.Run("Read Lock Reentrant", func(t *testing.T) {
		readLock := rwLock.ReadLock()

		// 第一次获取读锁
		err := readLock.Lock(ctx)
		require.NoError(t, err)

		holdCount, err := readLock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, holdCount)

		// 再次获取读锁（可重入）
		err = readLock.Lock(ctx)
		require.NoError(t, err)

		holdCount, err = readLock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, holdCount)

		// 第一次释放
		err = readLock.Unlock(ctx)
		require.NoError(t, err)

		holdCount, err = readLock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, holdCount)

		// 第二次释放
		err = readLock.Unlock(ctx)
		require.NoError(t, err)

		holdCount, err = readLock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, holdCount)
	})

	// 清理
	readLock := rwLock.ReadLock()
	writeLock := rwLock.WriteLock()
	readLock.Delete(ctx)
	writeLock.Delete(ctx)
}

func TestLockPerformance(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.AutoRenewal = false

	ctx := context.Background()

	t.Run("Lock Performance", func(t *testing.T) {
		const numOperations = 100
		lockName := "test_performance_lock"

		start := time.Now()

		for i := 0; i < numOperations; i++ {
			lock := NewLock(client, lockName, config)

			err := lock.Lock(ctx)
			require.NoError(t, err)

			// 模拟一些工作
			time.Sleep(1 * time.Millisecond)

			err = lock.Unlock(ctx)
			require.NoError(t, err)

			if i == numOperations-1 {
				lock.Delete(ctx)
			}
		}

		elapsed := time.Since(start)
		opsPerSecond := float64(numOperations) / elapsed.Seconds()

		t.Logf("Performed %d lock/unlock operations in %v", numOperations, elapsed)
		t.Logf("Operations per second: %.2f", opsPerSecond)

		// 基本性能要求（可根据实际环境调整）
		assert.Greater(t, opsPerSecond, 50.0, "Lock performance should be reasonable")
	})
}

func TestLockStressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	client := setupRedisClient()
	config := DefaultLockConfig()
	config.WaitTimeout = 2 * time.Second
	config.AutoRenewal = false

	ctx := context.Background()
	lockName := "test_stress_lock"

	t.Run("Concurrent Stress Test", func(t *testing.T) {
		const numGoroutines = 20
		const operationsPerGoroutine = 50

		var totalOperations int64
		var mu sync.Mutex
		var wg sync.WaitGroup

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()

				localOps := 0
				for j := 0; j < operationsPerGoroutine; j++ {
					lock := NewLock(client, lockName, config)

					acquired, err := lock.TryLockWithTimeout(ctx, 500*time.Millisecond)
					if err == nil && acquired {
						// 模拟工作
						time.Sleep(time.Duration(j%5) * time.Millisecond)

						err = lock.Unlock(ctx)
						if err == nil {
							localOps++
						}
					}

					if j == operationsPerGoroutine-1 {
						lock.Delete(ctx)
					}
				}

				mu.Lock()
				totalOperations += int64(localOps)
				mu.Unlock()
			}(i)
		}

		start := time.Now()
		wg.Wait()
		elapsed := time.Since(start)

		t.Logf("Stress test completed: %d operations in %v", totalOperations, elapsed)
		t.Logf("Operations per second: %.2f", float64(totalOperations)/elapsed.Seconds())

		// 验证至少有一定数量的操作成功
		assert.Greater(t, totalOperations, int64(numGoroutines*operationsPerGoroutine/2),
			"At least 50% of operations should succeed under stress")
	})
}

// Benchmark 测试
func BenchmarkLockUnlock(b *testing.B) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.AutoRenewal = false

	lock := NewLock(client, "benchmark_lock", config)
	defer func() {
		ctx := context.Background()
		lock.Delete(ctx)
	}()

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			err := lock.Lock(ctx)
			if err != nil {
				b.Fatal(err)
			}

			err = lock.Unlock(ctx)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkTryLock(b *testing.B) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.AutoRenewal = false

	lockName := "benchmark_trylock"
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lock := NewLock(client, lockName, config)

			acquired, err := lock.TryLock(ctx)
			if err != nil {
				b.Fatal(err)
			}

			if acquired {
				err = lock.Unlock(ctx)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}

func BenchmarkReadWriteLock(b *testing.B) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.AutoRenewal = false

	rwLock := NewReadWriteLock(client, "benchmark_rwlock", config)
	ctx := context.Background()

	b.ResetTimer()
	b.Run("ReadLock", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				readLock := rwLock.ReadLock()

				err := readLock.Lock(ctx)
				if err != nil {
					b.Fatal(err)
				}

				err = readLock.Unlock(ctx)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	})

	b.Run("WriteLock", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			writeLock := rwLock.WriteLock()

			err := writeLock.Lock(ctx)
			if err != nil {
				b.Fatal(err)
			}

			err = writeLock.Unlock(ctx)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
