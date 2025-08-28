package lock

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisFairLock Redis 实现的公平锁
type redisFairLock struct {
	*redisLock
}

// NewFairLock 创建一个新的公平锁
func NewFairLock(client redis.Cmdable, name string, config *LockConfig) FairLock {
	if config == nil {
		config = DefaultLockConfig()
	}
	config.Fair = true

	baseLock := NewLock(client, name, config).(*redisLock)
	return &redisFairLock{
		redisLock: baseLock,
	}
}

// Lock 获取公平锁，按 FIFO 顺序
func (fl *redisFairLock) Lock(ctx context.Context) error {
	return fl.lockFairInternal(ctx, fl.config.WaitTimeout, true)
}

// TryLock 尝试获取公平锁
func (fl *redisFairLock) TryLock(ctx context.Context) (bool, error) {
	err := fl.lockFairInternal(ctx, 0, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// TryLockWithTimeout 在指定时间内尝试获取公平锁
func (fl *redisFairLock) TryLockWithTimeout(ctx context.Context, timeout time.Duration) (bool, error) {
	err := fl.lockFairInternal(ctx, timeout, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// lockFairInternal 公平锁的内部加锁逻辑
func (fl *redisFairLock) lockFairInternal(ctx context.Context, waitTimeout time.Duration, block bool) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	// 检查可重入锁
	if fl.config.Reentrant && fl.holdCount > 0 {
		currentOwner := fmt.Sprintf("%s:%s", fl.owner, fl.threadID)

		lockInfo, err := fl.getLockInfo(ctx)
		if err == nil && lockInfo != nil {
			existingOwner := fmt.Sprintf("%s:%s", lockInfo.Owner, lockInfo.ThreadID)
			if existingOwner == currentOwner {
				fl.holdCount++
				return fl.updateLockInfo(ctx, fl.holdCount)
			}
		}
	}

	// 加入等待队列
	queuePosition, err := fl.joinQueue(ctx)
	if err != nil {
		return err
	}

	defer fl.leaveQueue(ctx)

	startTime := time.Now()
	for {
		// 检查是否轮到自己
		canAcquire, err := fl.canAcquireFairLock(ctx, queuePosition)
		if err != nil {
			return err
		}

		if canAcquire {
			// 尝试获取锁
			acquired, err := fl.tryAcquireLock(ctx)
			if err != nil {
				return err
			}

			if acquired {
				fl.holdCount = 1
				// 启动自动续期
				if fl.config.AutoRenewal {
					go fl.renewalTask()
				}
				return nil
			}
		}

		// 如果不阻塞或超时，直接返回
		if !block || (waitTimeout > 0 && time.Since(startTime) >= waitTimeout) {
			return ErrLockTimeout
		}

		// 等待重试
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(fl.config.RetryInterval):
			continue
		}
	}
}

// joinQueue 加入等待队列
func (fl *redisFairLock) joinQueue(ctx context.Context) (int64, error) {
	script := `
		local queue_key = KEYS[1]
		local owner = ARGV[1]
		local thread_id = ARGV[2]
		local current_time = tonumber(ARGV[3])
		
		local member = owner .. ':' .. thread_id
		
		-- 检查是否已在队列中
		local existing_score = redis.call('ZSCORE', queue_key, member)
		if existing_score then
			return tonumber(existing_score)
		end
		
		-- 加入队列，使用时间戳作为分数确保 FIFO
		redis.call('ZADD', queue_key, current_time, member)
		
		-- 返回在队列中的位置
		return redis.call('ZRANK', queue_key, member)
	`

	keys := []string{fl.getQueueKey()}
	args := []interface{}{
		fl.owner,
		fl.threadID,
		time.Now().UnixNano(),
	}

	result, err := fl.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to join queue: %w", err)
	}

	return result.(int64), nil
}

// leaveQueue 离开等待队列
func (fl *redisFairLock) leaveQueue(ctx context.Context) error {
	member := fmt.Sprintf("%s:%s", fl.owner, fl.threadID)
	return fl.client.ZRem(ctx, fl.getQueueKey(), member).Err()
}

// canAcquireFairLock 检查是否可以获取公平锁
func (fl *redisFairLock) canAcquireFairLock(ctx context.Context, queuePosition int64) (bool, error) {
	script := `
		local lock_key = KEYS[1]
		local queue_key = KEYS[2]
		local queue_position = tonumber(ARGV[1])
		local current_time = tonumber(ARGV[2])
		
		-- 检查锁是否被持有
		local current_lock = redis.call('GET', lock_key)
		if current_lock then
			local lock_info = cjson.decode(current_lock)
			local expire_time = tonumber(lock_info.expire_time)
			
			-- 如果锁未过期，检查队列位置
			if expire_time > current_time then
				-- 只有队列中的第一个可以获取锁
				return queue_position == 0
			end
		end
		
		-- 锁不存在或已过期，队列中第一个可以获取
		return queue_position == 0
	`

	keys := []string{fl.getLockKey(), fl.getQueueKey()}
	args := []interface{}{
		queuePosition,
		time.Now().Unix(),
	}

	result, err := fl.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check fair lock acquisition: %w", err)
	}

	return result.(int64) == 1, nil
}

// LockAsync 异步获取锁
func (fl *redisFairLock) LockAsync(ctx context.Context) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		err := fl.Lock(ctx)
		resultChan <- err
	}()

	return resultChan
}

// GetQueueLength 获取等待队列长度
func (fl *redisFairLock) GetQueueLength(ctx context.Context) (int64, error) {
	return fl.client.ZCard(ctx, fl.getQueueKey()).Result()
}

// HasQueuedThreads 检查是否有线程在等待队列中
func (fl *redisFairLock) HasQueuedThreads(ctx context.Context) (bool, error) {
	length, err := fl.GetQueueLength(ctx)
	if err != nil {
		return false, err
	}
	return length > 0, nil
}

// Unlock 释放公平锁
func (fl *redisFairLock) Unlock(ctx context.Context) error {
	// 调用父类的 Unlock 方法
	err := fl.redisLock.Unlock(ctx)
	if err != nil {
		return err
	}

	// 清理队列中的过期条目
	go fl.cleanupQueue(ctx)

	return nil
}

// cleanupQueue 清理队列中的过期条目
func (fl *redisFairLock) cleanupQueue(ctx context.Context) {
	script := `
		local queue_key = KEYS[1]
		local current_time = tonumber(ARGV[1])
		local expire_duration = tonumber(ARGV[2])
		
		-- 移除超过过期时间的队列条目
		local expire_before = current_time - expire_duration
		return redis.call('ZREMRANGEBYSCORE', queue_key, 0, expire_before)
	`

	keys := []string{fl.getQueueKey()}
	args := []interface{}{
		time.Now().UnixNano(),
		fl.config.WaitTimeout.Nanoseconds(),
	}

	fl.client.Eval(ctx, script, keys, args...)
}

// GetStats 获取公平锁的统计信息
func (fl *redisFairLock) GetStats(ctx context.Context) (*LockStats, error) {
	// 获取基本锁信息
	lockInfo, err := fl.getLockInfo(ctx)
	if err != nil {
		return nil, err
	}

	// 获取等待队列长度
	queueLength, err := fl.GetQueueLength(ctx)
	if err != nil {
		return nil, err
	}

	// 获取统计信息
	statsData, err := fl.client.HMGet(ctx, fl.getStatsKey(),
		"total_acquired", "total_released", "total_timeout").Result()
	if err != nil {
		return nil, err
	}

	stats := &LockStats{
		Name:           fl.name,
		WaitingThreads: queueLength,
	}

	// 填充锁信息
	if lockInfo != nil {
		stats.IsLocked = time.Now().Before(lockInfo.ExpireTime)
		stats.Owner = lockInfo.Owner
		stats.HoldCount = lockInfo.HoldCount
		stats.RemainingTTL = time.Until(lockInfo.ExpireTime)
	}

	// 填充统计数据
	if len(statsData) >= 3 {
		if val, ok := statsData[0].(string); ok {
			if totalAcquired, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.TotalAcquired = totalAcquired
			}
		}
		if val, ok := statsData[1].(string); ok {
			if totalReleased, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.TotalReleased = totalReleased
			}
		}
		if val, ok := statsData[2].(string); ok {
			if totalTimeout, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.TotalTimeout = totalTimeout
			}
		}
	}

	return stats, nil
}
