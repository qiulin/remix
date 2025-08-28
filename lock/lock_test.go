package lock

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 测试用的 Redis 客户端
func setupRedisClient() redis.Cmdable {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})
	return rdb
}

// 清理测试数据
func cleanupLock(t *testing.T, lock Lock) {
	ctx := context.Background()
	err := lock.Delete(ctx)
	assert.NoError(t, err)
}

func TestBasicLockOperations(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.LeaseDuration = 2 * time.Second
	config.AutoRenewal = false // 测试时禁用自动续期

	lock := NewLock(client, "test_lock", config)
	defer cleanupLock(t, lock)

	ctx := context.Background()

	t.Run("Lock and Unlock", func(t *testing.T) {
		// 测试获取锁
		err := lock.Lock(ctx)
		require.NoError(t, err)

		// 检查锁状态
		isLocked, err := lock.IsLocked(ctx)
		require.NoError(t, err)
		assert.True(t, isLocked)

		isHeld, err := lock.IsHeldByCurrentThread(ctx)
		require.NoError(t, err)
		assert.True(t, isHeld)

		// 测试释放锁
		err = lock.Unlock(ctx)
		require.NoError(t, err)

		// 检查锁状态
		isLocked, err = lock.IsLocked(ctx)
		require.NoError(t, err)
		assert.False(t, isLocked)
	})

	t.Run("TryLock", func(t *testing.T) {
		// 测试 TryLock 成功
		acquired, err := lock.TryLock(ctx)
		require.NoError(t, err)
		assert.True(t, acquired)

		// 测试 TryLock 失败（锁已被持有）
		lock2 := NewLock(client, "test_lock", config)
		acquired2, err := lock2.TryLock(ctx)
		require.NoError(t, err)
		assert.False(t, acquired2)

		// 释放锁
		err = lock.Unlock(ctx)
		require.NoError(t, err)

		// 再次尝试获取锁应该成功
		acquired3, err := lock2.TryLock(ctx)
		require.NoError(t, err)
		assert.True(t, acquired3)

		err = lock2.Unlock(ctx)
		require.NoError(t, err)
	})

	t.Run("TryLockWithTimeout", func(t *testing.T) {
		// 先获取锁
		err := lock.Lock(ctx)
		require.NoError(t, err)

		lock2 := NewLock(client, "test_lock", config)

		// 测试超时
		start := time.Now()
		acquired, err := lock2.TryLockWithTimeout(ctx, 500*time.Millisecond)
		elapsed := time.Since(start)

		require.NoError(t, err)
		assert.False(t, acquired)
		assert.GreaterOrEqual(t, elapsed, 400*time.Millisecond)

		// 释放锁
		err = lock.Unlock(ctx)
		require.NoError(t, err)
	})
}

func TestReentrantLock(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.Reentrant = true
	config.AutoRenewal = false

	lock := NewLock(client, "test_reentrant_lock", config)
	defer cleanupLock(t, lock)

	ctx := context.Background()

	t.Run("Reentrant Lock", func(t *testing.T) {
		// 第一次获取锁
		err := lock.Lock(ctx)
		require.NoError(t, err)

		holdCount, err := lock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, holdCount)

		// 再次获取锁（可重入）
		err = lock.Lock(ctx)
		require.NoError(t, err)

		holdCount, err = lock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, holdCount)

		// 第一次释放锁
		err = lock.Unlock(ctx)
		require.NoError(t, err)

		holdCount, err = lock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, holdCount)

		// 锁仍然被持有
		isHeld, err := lock.IsHeldByCurrentThread(ctx)
		require.NoError(t, err)
		assert.True(t, isHeld)

		// 第二次释放锁
		err = lock.Unlock(ctx)
		require.NoError(t, err)

		holdCount, err = lock.GetHoldCount(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, holdCount)

		// 锁完全释放
		isHeld, err = lock.IsHeldByCurrentThread(ctx)
		require.NoError(t, err)
		assert.False(t, isHeld)
	})
}

func TestConcurrentLocks(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.WaitTimeout = 1 * time.Second
	config.AutoRenewal = false

	lockName := "test_concurrent_lock"
	ctx := context.Background()

	t.Run("Mutual Exclusion", func(t *testing.T) {
		const numGoroutines = 5
		const iterations = 10

		var counter int
		var wg sync.WaitGroup
		var mu sync.Mutex
		results := make([]int, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()

				lock := NewLock(client, lockName, config)
				defer lock.Delete(ctx)

				localCounter := 0
				for j := 0; j < iterations; j++ {
					// 获取分布式锁
					err := lock.Lock(ctx)
					if err != nil {
						continue
					}

					// 临界区
					oldValue := counter
					time.Sleep(1 * time.Millisecond) // 模拟工作
					counter = oldValue + 1
					localCounter++

					// 释放锁
					lock.Unlock(ctx)
				}

				mu.Lock()
				results[goroutineID] = localCounter
				mu.Unlock()
			}(i)
		}

		wg.Wait()

		// 验证结果
		totalProcessed := 0
		for _, result := range results {
			totalProcessed += result
		}

		assert.Equal(t, totalProcessed, counter, "Counter should equal total processed operations")
		assert.LessOrEqual(t, counter, numGoroutines*iterations, "Counter should not exceed maximum possible")
	})
}

func TestLockExpiration(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.LeaseDuration = 1 * time.Second
	config.AutoRenewal = false

	lock := NewLock(client, "test_expiration_lock", config)
	defer cleanupLock(t, lock)

	ctx := context.Background()

	t.Run("Lock Expiration", func(t *testing.T) {
		// 获取锁
		err := lock.Lock(ctx)
		require.NoError(t, err)

		// 检查锁状态
		isLocked, err := lock.IsLocked(ctx)
		require.NoError(t, err)
		assert.True(t, isLocked)

		// 等待锁过期
		time.Sleep(1500 * time.Millisecond)

		// 检查锁是否过期
		isLocked, err = lock.IsLocked(ctx)
		require.NoError(t, err)
		assert.False(t, isLocked)

		// 其他实例应该能获取锁
		lock2 := NewLock(client, "test_expiration_lock", config)
		acquired, err := lock2.TryLock(ctx)
		require.NoError(t, err)
		assert.True(t, acquired)

		err = lock2.Unlock(ctx)
		require.NoError(t, err)
	})
}

func TestForceUnlock(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.AutoRenewal = false

	lock1 := NewLock(client, "test_force_unlock", config)
	lock2 := NewLock(client, "test_force_unlock", config)
	defer cleanupLock(t, lock1)

	ctx := context.Background()

	t.Run("Force Unlock", func(t *testing.T) {
		// lock1 获取锁
		err := lock1.Lock(ctx)
		require.NoError(t, err)

		// lock2 无法获取锁
		acquired, err := lock2.TryLock(ctx)
		require.NoError(t, err)
		assert.False(t, acquired)

		// lock2 强制释放锁
		err = lock2.ForceUnlock(ctx)
		require.NoError(t, err)

		// lock2 现在应该能获取锁
		acquired, err = lock2.TryLock(ctx)
		require.NoError(t, err)
		assert.True(t, acquired)

		err = lock2.Unlock(ctx)
		require.NoError(t, err)
	})
}

func TestLockRenewal(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.LeaseDuration = 1 * time.Second
	config.RenewalInterval = 300 * time.Millisecond
	config.AutoRenewal = true

	lock := NewLock(client, "test_renewal_lock", config)
	defer cleanupLock(t, lock)

	ctx := context.Background()

	t.Run("Auto Renewal", func(t *testing.T) {
		// 获取锁
		err := lock.Lock(ctx)
		require.NoError(t, err)

		// 等待超过原始租约时间
		time.Sleep(1500 * time.Millisecond)

		// 锁应该仍然被持有（因为自动续期）
		isHeld, err := lock.IsHeldByCurrentThread(ctx)
		require.NoError(t, err)
		assert.True(t, isHeld)

		// 释放锁
		err = lock.Unlock(ctx)
		require.NoError(t, err)

		// 锁应该被释放
		isLocked, err := lock.IsLocked(ctx)
		require.NoError(t, err)
		assert.False(t, isLocked)
	})
}

func TestLockErrorHandling(t *testing.T) {
	client := setupRedisClient()
	config := DefaultLockConfig()
	config.AutoRenewal = false

	lock := NewLock(client, "test_error_lock", config)
	defer cleanupLock(t, lock)

	ctx := context.Background()

	t.Run("Unlock Without Lock", func(t *testing.T) {
		// 尝试释放未持有的锁
		err := lock.Unlock(ctx)
		assert.Error(t, err)
		assert.Equal(t, ErrLockNotHeld, err)
	})

	t.Run("Double Unlock", func(t *testing.T) {
		// 获取锁
		err := lock.Lock(ctx)
		require.NoError(t, err)

		// 第一次释放锁
		err = lock.Unlock(ctx)
		require.NoError(t, err)

		// 第二次释放锁应该失败
		err = lock.Unlock(ctx)
		assert.Error(t, err)
		assert.Equal(t, ErrLockNotHeld, err)
	})
}
