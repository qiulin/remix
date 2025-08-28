package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisLock Redis 实现的分布式锁
type redisLock struct {
	client      redis.Cmdable
	name        string
	config      *LockConfig
	owner       string
	threadID    string
	holdCount   int
	renewalStop chan struct{}
	renewalDone chan struct{}
	mu          sync.RWMutex
}

// NewLock 创建一个新的分布式锁
func NewLock(client redis.Cmdable, name string, config *LockConfig) Lock {
	if config == nil {
		config = DefaultLockConfig()
	}

	owner := generateOwnerID()
	threadID := getThreadID()

	lock := &redisLock{
		client:      client,
		name:        name,
		config:      config,
		owner:       owner,
		threadID:    threadID,
		holdCount:   0,
		renewalStop: make(chan struct{}),
		renewalDone: make(chan struct{}),
	}

	return lock
}

// generateOwnerID 生成唯一的所有者 ID
func generateOwnerID() string {
	hostname, _ := os.Hostname()
	pid := os.Getpid()

	// 生成随机字符串
	bytes := make([]byte, 8)
	rand.Read(bytes)
	random := hex.EncodeToString(bytes)

	return fmt.Sprintf("%s:%d:%s", hostname, pid, random)
}

// getThreadID 获取当前协程 ID
func getThreadID() string {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	idField := strings.Fields(strings.TrimPrefix(string(buf[:n]), "goroutine "))[0]
	return idField
}

// getLockKey 获取锁的 Redis key
func (l *redisLock) getLockKey() string {
	return fmt.Sprintf("lock:%s", l.name)
}

// getStatsKey 获取统计信息的 Redis key
func (l *redisLock) getStatsKey() string {
	return fmt.Sprintf("lock:%s:stats", l.name)
}

// getQueueKey 获取等待队列的 Redis key（公平锁使用）
func (l *redisLock) getQueueKey() string {
	return fmt.Sprintf("lock:%s:queue", l.name)
}

// Lock 获取锁，阻塞直到成功获取锁
func (l *redisLock) Lock(ctx context.Context) error {
	return l.lockInternal(ctx, l.config.WaitTimeout, true)
}

// TryLock 尝试获取锁，立即返回结果
func (l *redisLock) TryLock(ctx context.Context) (bool, error) {
	err := l.lockInternal(ctx, 0, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// TryLockWithTimeout 在指定时间内尝试获取锁
func (l *redisLock) TryLockWithTimeout(ctx context.Context, timeout time.Duration) (bool, error) {
	err := l.lockInternal(ctx, timeout, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// lockInternal 内部加锁逻辑
func (l *redisLock) lockInternal(ctx context.Context, waitTimeout time.Duration, block bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 检查可重入锁
	if l.config.Reentrant && l.holdCount > 0 {
		currentOwner := fmt.Sprintf("%s:%s", l.owner, l.threadID)

		// 检查是否是同一个线程持有的锁
		lockInfo, err := l.getLockInfo(ctx)
		if err == nil && lockInfo != nil {
			existingOwner := fmt.Sprintf("%s:%s", lockInfo.Owner, lockInfo.ThreadID)
			if existingOwner == currentOwner {
				l.holdCount++
				// 更新锁信息
				return l.updateLockInfo(ctx, l.holdCount)
			}
		}
	}

	startTime := time.Now()
	for {
		// 尝试获取锁
		acquired, err := l.tryAcquireLock(ctx)
		if err != nil {
			return err
		}

		if acquired {
			l.holdCount = 1
			// 启动自动续期
			if l.config.AutoRenewal {
				go l.renewalTask()
			}
			return nil
		}

		// 如果不阻塞或超时，直接返回
		if !block || (waitTimeout > 0 && time.Since(startTime) >= waitTimeout) {
			return ErrLockTimeout
		}

		// 等待重试
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.config.RetryInterval):
			continue
		}
	}
}

// tryAcquireLock 尝试获取锁
func (l *redisLock) tryAcquireLock(ctx context.Context) (bool, error) {
	now := time.Now()
	expireTime := now.Add(l.config.LeaseDuration)

	lockInfo := &LockInfo{
		Name:            l.name,
		Owner:           l.owner,
		ThreadID:        l.threadID,
		HoldCount:       1,
		AcquireTime:     now,
		ExpireTime:      expireTime,
		LastRenewalTime: now,
	}

	lockData, err := json.Marshal(lockInfo)
	if err != nil {
		return false, fmt.Errorf("failed to marshal lock info: %w", err)
	}

	// 使用 Lua 脚本确保原子性
	script := `
		local lock_key = KEYS[1]
		local stats_key = KEYS[2]
		
		local lock_data = ARGV[1]
		local expire_seconds = tonumber(ARGV[2])
		
		-- 检查锁是否存在
		local current_lock = redis.call('GET', lock_key)
		if current_lock then
			-- 锁已存在，检查是否过期
			local lock_info = cjson.decode(current_lock)
			local expire_time = tonumber(lock_info.expire_time)
			local current_time = tonumber(ARGV[3])
			
			if expire_time > current_time then
				-- 锁未过期，获取失败
				return 0
			end
		end
		
		-- 设置锁
		redis.call('SET', lock_key, lock_data, 'EX', expire_seconds)
		
		-- 更新统计信息
		redis.call('HINCRBY', stats_key, 'total_acquired', 1)
		
		return 1
	`

	keys := []string{l.getLockKey(), l.getStatsKey()}
	args := []interface{}{
		string(lockData),
		int(l.config.LeaseDuration.Seconds()),
		now.Unix(),
	}

	result, err := l.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return false, fmt.Errorf("failed to acquire lock: %w", err)
	}

	return result.(int64) == 1, nil
}

// Unlock 释放锁
func (l *redisLock) Unlock(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.holdCount == 0 {
		return ErrLockNotHeld
	}

	// 可重入锁处理
	if l.config.Reentrant && l.holdCount > 1 {
		l.holdCount--
		return l.updateLockInfo(ctx, l.holdCount)
	}

	// 释放锁
	err := l.releaseLock(ctx)
	if err != nil {
		return err
	}

	l.holdCount = 0

	// 停止自动续期
	if l.config.AutoRenewal {
		close(l.renewalStop)
		<-l.renewalDone

		// 重新初始化通道以便复用
		l.renewalStop = make(chan struct{})
		l.renewalDone = make(chan struct{})
	}

	return nil
}

// releaseLock 释放锁的内部逻辑
func (l *redisLock) releaseLock(ctx context.Context) error {
	script := `
		local lock_key = KEYS[1]
		local stats_key = KEYS[2]
		
		local owner = ARGV[1]
		local thread_id = ARGV[2]
		
		-- 获取当前锁信息
		local current_lock = redis.call('GET', lock_key)
		if not current_lock then
			return 0  -- 锁不存在
		end
		
		local lock_info = cjson.decode(current_lock)
		
		-- 检查是否是锁的持有者
		if lock_info.owner ~= owner or lock_info.thread_id ~= thread_id then
			return -1  -- 不是锁的持有者
		end
		
		-- 删除锁
		redis.call('DEL', lock_key)
		
		-- 更新统计信息
		redis.call('HINCRBY', stats_key, 'total_released', 1)
		
		return 1
	`

	keys := []string{l.getLockKey(), l.getStatsKey()}
	args := []interface{}{l.owner, l.threadID}

	result, err := l.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}

	resultCode := result.(int64)
	switch resultCode {
	case 0:
		return ErrLockNotFound
	case -1:
		return ErrLockNotHeld
	case 1:
		return nil
	default:
		return ErrInvalidOperation
	}
}

// updateLockInfo 更新锁信息（用于可重入锁）
func (l *redisLock) updateLockInfo(ctx context.Context, holdCount int) error {
	lockInfo, err := l.getLockInfo(ctx)
	if err != nil {
		return err
	}

	if lockInfo == nil {
		return ErrLockNotFound
	}

	lockInfo.HoldCount = holdCount
	lockInfo.LastRenewalTime = time.Now()

	lockData, err := json.Marshal(lockInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal lock info: %w", err)
	}

	return l.client.Set(ctx, l.getLockKey(), string(lockData), l.config.LeaseDuration).Err()
}

// IsLocked 检查锁是否被持有
func (l *redisLock) IsLocked(ctx context.Context) (bool, error) {
	lockInfo, err := l.getLockInfo(ctx)
	if err != nil {
		return false, err
	}

	if lockInfo == nil {
		return false, nil
	}

	// 检查是否过期
	return time.Now().Before(lockInfo.ExpireTime), nil
}

// IsHeldByCurrentThread 检查锁是否被当前线程/协程持有
func (l *redisLock) IsHeldByCurrentThread(ctx context.Context) (bool, error) {
	lockInfo, err := l.getLockInfo(ctx)
	if err != nil {
		return false, err
	}

	if lockInfo == nil {
		return false, nil
	}

	currentOwner := fmt.Sprintf("%s:%s", l.owner, l.threadID)
	existingOwner := fmt.Sprintf("%s:%s", lockInfo.Owner, lockInfo.ThreadID)

	return currentOwner == existingOwner && time.Now().Before(lockInfo.ExpireTime), nil
}

// GetHoldCount 获取当前线程/协程持有锁的次数
func (l *redisLock) GetHoldCount(ctx context.Context) (int, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	isHeld, err := l.IsHeldByCurrentThread(ctx)
	if err != nil {
		return 0, err
	}

	if !isHeld {
		return 0, nil
	}

	return l.holdCount, nil
}

// GetRemainingTimeToLive 获取锁的剩余生存时间
func (l *redisLock) GetRemainingTimeToLive(ctx context.Context) (time.Duration, error) {
	ttl, err := l.client.TTL(ctx, l.getLockKey()).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get lock TTL: %w", err)
	}

	if ttl < 0 {
		return 0, nil // 锁不存在或已过期
	}

	return ttl, nil
}

// getLockInfo 获取锁信息
func (l *redisLock) getLockInfo(ctx context.Context) (*LockInfo, error) {
	lockData, err := l.client.Get(ctx, l.getLockKey()).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get lock info: %w", err)
	}

	var lockInfo LockInfo
	if err := json.Unmarshal([]byte(lockData), &lockInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal lock info: %w", err)
	}

	return &lockInfo, nil
}

// ForceUnlock 强制释放锁
func (l *redisLock) ForceUnlock(ctx context.Context) error {
	script := `
		local lock_key = KEYS[1]
		local stats_key = KEYS[2]
		
		-- 删除锁（不检查持有者）
		local deleted = redis.call('DEL', lock_key)
		
		if deleted == 1 then
			-- 更新统计信息
			redis.call('HINCRBY', stats_key, 'total_released', 1)
			return 1
		end
		
		return 0
	`

	keys := []string{l.getLockKey(), l.getStatsKey()}

	result, err := l.client.Eval(ctx, script, keys).Result()
	if err != nil {
		return fmt.Errorf("failed to force unlock: %w", err)
	}

	if result.(int64) == 0 {
		return ErrLockNotFound
	}

	return nil
}

// Delete 删除锁
func (l *redisLock) Delete(ctx context.Context) error {
	keys := []string{
		l.getLockKey(),
		l.getStatsKey(),
		l.getQueueKey(),
	}

	_, err := l.client.Del(ctx, keys...).Result()
	return err
}

// renewalTask 自动续期任务
func (l *redisLock) renewalTask() {
	defer close(l.renewalDone)

	ticker := time.NewTicker(l.config.RenewalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.renewalStop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			l.renewLock(ctx)
			cancel()
		}
	}
}

// renewLock 续期锁
func (l *redisLock) renewLock(ctx context.Context) error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.holdCount == 0 {
		return nil // 锁已释放
	}

	script := `
		local lock_key = KEYS[1]
		
		local owner = ARGV[1]
		local thread_id = ARGV[2]
		local expire_seconds = tonumber(ARGV[3])
		local current_time = tonumber(ARGV[4])
		
		-- 获取当前锁信息
		local current_lock = redis.call('GET', lock_key)
		if not current_lock then
			return 0  -- 锁不存在
		end
		
		local lock_info = cjson.decode(current_lock)
		
		-- 检查是否是锁的持有者
		if lock_info.owner ~= owner or lock_info.thread_id ~= thread_id then
			return -1  -- 不是锁的持有者
		end
		
		-- 更新续期时间
		lock_info.last_renewal_time = current_time
		lock_info.expire_time = current_time + expire_seconds
		
		local updated_data = cjson.encode(lock_info)
		redis.call('SET', lock_key, updated_data, 'EX', expire_seconds)
		
		return 1
	`

	keys := []string{l.getLockKey()}
	args := []interface{}{
		l.owner,
		l.threadID,
		int(l.config.LeaseDuration.Seconds()),
		time.Now().Unix(),
	}

	_, err := l.client.Eval(ctx, script, keys, args...).Result()
	return err
}
