package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/qiulin/remix/lock"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 创建 Redis 客户端
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	ctx := context.Background()

	// 示例 1: 基本分布式锁
	fmt.Println("=== 基本分布式锁示例 ===")
	basicLockExample(ctx, rdb)

	// 示例 2: 可重入锁
	fmt.Println("\n=== 可重入锁示例 ===")
	reentrantLockExample(ctx, rdb)

	// 示例 3: 公平锁
	fmt.Println("\n=== 公平锁示例 ===")
	fairLockExample(ctx, rdb)

	// 示例 4: 读写锁
	fmt.Println("\n=== 读写锁示例 ===")
	readWriteLockExample(ctx, rdb)

	// 示例 5: 锁的自动续期
	fmt.Println("\n=== 锁自动续期示例 ===")
	lockRenewalExample(ctx, rdb)

	// 示例 6: 并发控制示例
	fmt.Println("\n=== 并发控制示例 ===")
	concurrencyControlExample(ctx, rdb)
}

func basicLockExample(ctx context.Context, rdb redis.Cmdable) {
	config := lock.DefaultLockConfig()
	distributedLock := lock.NewLock(rdb, "basic_lock", config)
	defer distributedLock.Delete(ctx)

	// 获取锁
	fmt.Println("尝试获取分布式锁...")
	err := distributedLock.Lock(ctx)
	if err != nil {
		log.Printf("获取锁失败: %v", err)
		return
	}
	fmt.Println("✅ 成功获取分布式锁")

	// 检查锁状态
	isLocked, _ := distributedLock.IsLocked(ctx)
	isHeld, _ := distributedLock.IsHeldByCurrentThread(ctx)
	fmt.Printf("锁状态: 被锁定=%v, 被当前线程持有=%v\n", isLocked, isHeld)

	// 模拟业务处理
	fmt.Println("正在处理业务逻辑...")
	time.Sleep(1 * time.Second)

	// 释放锁
	err = distributedLock.Unlock(ctx)
	if err != nil {
		log.Printf("释放锁失败: %v", err)
		return
	}
	fmt.Println("✅ 成功释放分布式锁")
}

func reentrantLockExample(ctx context.Context, rdb redis.Cmdable) {
	config := lock.DefaultLockConfig()
	config.Reentrant = true

	reentrantLock := lock.NewLock(rdb, "reentrant_lock", config)
	defer reentrantLock.Delete(ctx)

	// 第一次获取锁
	fmt.Println("第一次获取可重入锁...")
	err := reentrantLock.Lock(ctx)
	if err != nil {
		log.Printf("获取锁失败: %v", err)
		return
	}

	holdCount, _ := reentrantLock.GetHoldCount(ctx)
	fmt.Printf("✅ 第一次获取成功，持有次数: %d\n", holdCount)

	// 第二次获取锁（可重入）
	fmt.Println("第二次获取可重入锁...")
	err = reentrantLock.Lock(ctx)
	if err != nil {
		log.Printf("获取锁失败: %v", err)
		return
	}

	holdCount, _ = reentrantLock.GetHoldCount(ctx)
	fmt.Printf("✅ 第二次获取成功，持有次数: %d\n", holdCount)

	// 第一次释放锁
	err = reentrantLock.Unlock(ctx)
	if err != nil {
		log.Printf("释放锁失败: %v", err)
		return
	}

	holdCount, _ = reentrantLock.GetHoldCount(ctx)
	fmt.Printf("✅ 第一次释放成功，持有次数: %d\n", holdCount)

	// 第二次释放锁
	err = reentrantLock.Unlock(ctx)
	if err != nil {
		log.Printf("释放锁失败: %v", err)
		return
	}

	holdCount, _ = reentrantLock.GetHoldCount(ctx)
	fmt.Printf("✅ 第二次释放成功，持有次数: %d\n", holdCount)
}

func fairLockExample(ctx context.Context, rdb redis.Cmdable) {
	config := lock.DefaultLockConfig()
	config.Fair = true
	config.WaitTimeout = 5 * time.Second

	var wg sync.WaitGroup
	const numWorkers = 3

	// 启动多个工作者，演示公平锁的 FIFO 特性
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			fairLock := lock.NewFairLock(rdb, "fair_lock", config)
			if workerID == 0 {
				defer fairLock.Delete(ctx)
			}

			fmt.Printf("工作者 %d 开始排队等待公平锁...\n", workerID)

			start := time.Now()
			err := fairLock.Lock(ctx)
			if err != nil {
				fmt.Printf("工作者 %d 获取锁失败: %v\n", workerID, err)
				return
			}

			elapsed := time.Since(start)
			fmt.Printf("✅ 工作者 %d 获取到公平锁，等待时间: %v\n", workerID, elapsed)

			// 模拟工作
			time.Sleep(500 * time.Millisecond)

			err = fairLock.Unlock(ctx)
			if err != nil {
				fmt.Printf("工作者 %d 释放锁失败: %v\n", workerID, err)
				return
			}

			fmt.Printf("✅ 工作者 %d 释放公平锁\n", workerID)
		}(i)

		// 错开启动时间，确保顺序
		time.Sleep(100 * time.Millisecond)
	}

	wg.Wait()
}

func readWriteLockExample(ctx context.Context, rdb redis.Cmdable) {
	config := lock.DefaultLockConfig()
	rwLock := lock.NewReadWriteLock(rdb, "rw_lock", config)

	var wg sync.WaitGroup

	// 启动多个读者
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			readLock := rwLock.ReadLock()

			fmt.Printf("读者 %d 尝试获取读锁...\n", readerID)
			err := readLock.Lock(ctx)
			if err != nil {
				fmt.Printf("读者 %d 获取读锁失败: %v\n", readerID, err)
				return
			}

			fmt.Printf("✅ 读者 %d 获取到读锁\n", readerID)
			time.Sleep(1 * time.Second) // 模拟读操作

			err = readLock.Unlock(ctx)
			if err != nil {
				fmt.Printf("读者 %d 释放读锁失败: %v\n", readerID, err)
				return
			}
			fmt.Printf("✅ 读者 %d 释放读锁\n", readerID)
		}(i)
	}

	// 启动一个写者
	wg.Add(1)
	go func() {
		defer wg.Done()

		// 等待读者先启动
		time.Sleep(500 * time.Millisecond)

		writeLock := rwLock.WriteLock()
		defer writeLock.Delete(ctx)

		fmt.Println("写者尝试获取写锁...")
		err := writeLock.Lock(ctx)
		if err != nil {
			fmt.Printf("写者获取写锁失败: %v\n", err)
			return
		}

		fmt.Println("✅ 写者获取到写锁")
		time.Sleep(800 * time.Millisecond) // 模拟写操作

		err = writeLock.Unlock(ctx)
		if err != nil {
			fmt.Printf("写者释放写锁失败: %v\n", err)
			return
		}
		fmt.Println("✅ 写者释放写锁")
	}()

	wg.Wait()
}

func lockRenewalExample(ctx context.Context, rdb redis.Cmdable) {
	config := lock.DefaultLockConfig()
	config.LeaseDuration = 2 * time.Second          // 短租约时间
	config.RenewalInterval = 600 * time.Millisecond // 续期间隔
	config.AutoRenewal = true                       // 启用自动续期

	renewalLock := lock.NewLock(rdb, "renewal_lock", config)
	defer renewalLock.Delete(ctx)

	fmt.Println("获取带自动续期的锁...")
	err := renewalLock.Lock(ctx)
	if err != nil {
		log.Printf("获取锁失败: %v", err)
		return
	}
	fmt.Println("✅ 成功获取锁，自动续期已启动")

	// 持有锁超过原始租约时间
	fmt.Println("持有锁 5 秒（超过原始租约时间 2 秒）...")
	for i := 0; i < 5; i++ {
		time.Sleep(1 * time.Second)

		// 检查锁状态
		isHeld, _ := renewalLock.IsHeldByCurrentThread(ctx)
		remainingTTL, _ := renewalLock.GetRemainingTimeToLive(ctx)

		fmt.Printf("第 %d 秒: 锁被持有=%v, 剩余TTL=%v\n", i+1, isHeld, remainingTTL)
	}

	err = renewalLock.Unlock(ctx)
	if err != nil {
		log.Printf("释放锁失败: %v", err)
		return
	}
	fmt.Println("✅ 成功释放锁，自动续期已停止")
}

func concurrencyControlExample(ctx context.Context, rdb redis.Cmdable) {
	config := lock.DefaultLockConfig()
	config.WaitTimeout = 3 * time.Second

	const numWorkers = 5
	const numTasks = 20
	var counter int
	var wg sync.WaitGroup

	fmt.Printf("启动 %d 个工作者处理 %d 个任务...\n", numWorkers, numTasks)

	// 任务通道
	tasks := make(chan int, numTasks)
	for i := 1; i <= numTasks; i++ {
		tasks <- i
	}
	close(tasks)

	// 启动工作者
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			distributedLock := lock.NewLock(rdb, "counter_lock", config)
			if workerID == 0 {
				defer distributedLock.Delete(ctx)
			}

			for taskID := range tasks {
				// 使用分布式锁保护计数器
				acquired, err := distributedLock.TryLockWithTimeout(ctx, 1*time.Second)
				if err != nil || !acquired {
					fmt.Printf("工作者 %d 获取锁失败，跳过任务 %d\n", workerID, taskID)
					continue
				}

				// 临界区：更新计数器
				oldValue := counter
				time.Sleep(10 * time.Millisecond) // 模拟计算
				counter = oldValue + 1

				fmt.Printf("工作者 %d 处理任务 %d，计数器: %d -> %d\n",
					workerID, taskID, oldValue, counter)

				distributedLock.Unlock(ctx)
			}
		}(i)
	}

	wg.Wait()
	fmt.Printf("✅ 所有任务完成，最终计数器值: %d\n", counter)
}
