package lock

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisReadWriteLock Redis 实现的读写锁
type redisReadWriteLock struct {
	client    redis.Cmdable
	name      string
	config    *LockConfig
	readLock  Lock
	writeLock Lock
}

// redisReadLock 读锁实现
type redisReadLock struct {
	rwLock   *redisReadWriteLock
	owner    string
	threadID string
}

// redisWriteLock 写锁实现
type redisWriteLock struct {
	rwLock   *redisReadWriteLock
	owner    string
	threadID string
}

// NewReadWriteLock 创建一个新的读写锁
func NewReadWriteLock(client redis.Cmdable, name string, config *LockConfig) ReadWriteLock {
	if config == nil {
		config = DefaultLockConfig()
	}

	rwLock := &redisReadWriteLock{
		client: client,
		name:   name,
		config: config,
	}

	rwLock.readLock = &redisReadLock{
		rwLock:   rwLock,
		owner:    generateOwnerID(),
		threadID: getThreadID(),
	}

	rwLock.writeLock = &redisWriteLock{
		rwLock:   rwLock,
		owner:    generateOwnerID(),
		threadID: getThreadID(),
	}

	return rwLock
}

// ReadLock 获取读锁
func (rwl *redisReadWriteLock) ReadLock() Lock {
	return rwl.readLock
}

// WriteLock 获取写锁
func (rwl *redisReadWriteLock) WriteLock() Lock {
	return rwl.writeLock
}

// 获取读写锁相关的 Redis key
func (rwl *redisReadWriteLock) getReadLockKey() string {
	return fmt.Sprintf("rwlock:%s:read", rwl.name)
}

func (rwl *redisReadWriteLock) getWriteLockKey() string {
	return fmt.Sprintf("rwlock:%s:write", rwl.name)
}

func (rwl *redisReadWriteLock) getReadCountKey() string {
	return fmt.Sprintf("rwlock:%s:read_count", rwl.name)
}

func (rwl *redisReadWriteLock) getStatsKey() string {
	return fmt.Sprintf("rwlock:%s:stats", rwl.name)
}

// 读锁实现

// Lock 获取读锁
func (rl *redisReadLock) Lock(ctx context.Context) error {
	return rl.lockInternal(ctx, rl.rwLock.config.WaitTimeout, true)
}

// TryLock 尝试获取读锁
func (rl *redisReadLock) TryLock(ctx context.Context) (bool, error) {
	err := rl.lockInternal(ctx, 0, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// TryLockWithTimeout 在指定时间内尝试获取读锁
func (rl *redisReadLock) TryLockWithTimeout(ctx context.Context, timeout time.Duration) (bool, error) {
	err := rl.lockInternal(ctx, timeout, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// lockInternal 读锁的内部加锁逻辑
func (rl *redisReadLock) lockInternal(ctx context.Context, waitTimeout time.Duration, block bool) error {
	startTime := time.Now()

	for {
		// 尝试获取读锁
		acquired, err := rl.tryAcquireReadLock(ctx)
		if err != nil {
			return err
		}

		if acquired {
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
		case <-time.After(rl.rwLock.config.RetryInterval):
			continue
		}
	}
}

// tryAcquireReadLock 尝试获取读锁
func (rl *redisReadLock) tryAcquireReadLock(ctx context.Context) (bool, error) {
	script := `
		local read_lock_key = KEYS[1]
		local write_lock_key = KEYS[2]
		local read_count_key = KEYS[3]
		local stats_key = KEYS[4]
		
		local owner = ARGV[1]
		local thread_id = ARGV[2]
		local expire_seconds = tonumber(ARGV[3])
		local current_time = tonumber(ARGV[4])
		
		-- 检查是否有写锁
		local write_lock = redis.call('GET', write_lock_key)
		if write_lock then
			local write_info = cjson.decode(write_lock)
			local expire_time = tonumber(write_info.expire_time)
			
			-- 如果写锁未过期且不是同一个线程，不能获取读锁
			if expire_time > current_time then
				local write_owner = write_info.owner .. ':' .. write_info.thread_id
				local current_owner = owner .. ':' .. thread_id
				
				if write_owner ~= current_owner then
					return 0
				end
			end
		end
		
		-- 创建读锁信息
		local read_info = {
			name = KEYS[1],
			owner = owner,
			thread_id = thread_id,
			acquire_time = current_time,
			expire_time = current_time + expire_seconds
		}
		
		local read_data = cjson.encode(read_info)
		local reader_key = owner .. ':' .. thread_id
		
		-- 设置读锁
		redis.call('HSET', read_lock_key, reader_key, read_data)
		redis.call('EXPIRE', read_lock_key, expire_seconds)
		
		-- 增加读者计数
		redis.call('HINCRBY', read_count_key, reader_key, 1)
		redis.call('EXPIRE', read_count_key, expire_seconds)
		
		-- 更新统计信息
		redis.call('HINCRBY', stats_key, 'total_read_acquired', 1)
		
		return 1
	`

	keys := []string{
		rl.rwLock.getReadLockKey(),
		rl.rwLock.getWriteLockKey(),
		rl.rwLock.getReadCountKey(),
		rl.rwLock.getStatsKey(),
	}

	args := []interface{}{
		rl.owner,
		rl.threadID,
		int(rl.rwLock.config.LeaseDuration.Seconds()),
		time.Now().Unix(),
	}

	result, err := rl.rwLock.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return false, fmt.Errorf("failed to acquire read lock: %w", err)
	}

	return result.(int64) == 1, nil
}

// Unlock 释放读锁
func (rl *redisReadLock) Unlock(ctx context.Context) error {
	script := `
		local read_lock_key = KEYS[1]
		local read_count_key = KEYS[2]
		local stats_key = KEYS[3]
		
		local owner = ARGV[1]
		local thread_id = ARGV[2]
		local reader_key = owner .. ':' .. thread_id
		
		-- 检查读锁是否存在
		local read_data = redis.call('HGET', read_lock_key, reader_key)
		if not read_data then
			return 0  -- 读锁不存在
		end
		
		-- 减少读者计数
		local count = redis.call('HINCRBY', read_count_key, reader_key, -1)
		
		-- 如果计数为 0，移除读锁
		if count <= 0 then
			redis.call('HDEL', read_lock_key, reader_key)
			redis.call('HDEL', read_count_key, reader_key)
		end
		
		-- 更新统计信息
		redis.call('HINCRBY', stats_key, 'total_read_released', 1)
		
		return 1
	`

	keys := []string{
		rl.rwLock.getReadLockKey(),
		rl.rwLock.getReadCountKey(),
		rl.rwLock.getStatsKey(),
	}

	args := []interface{}{rl.owner, rl.threadID}

	result, err := rl.rwLock.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return fmt.Errorf("failed to release read lock: %w", err)
	}

	if result.(int64) == 0 {
		return ErrLockNotHeld
	}

	return nil
}

// IsLocked 检查读锁是否被持有
func (rl *redisReadLock) IsLocked(ctx context.Context) (bool, error) {
	count, err := rl.rwLock.client.HLen(ctx, rl.rwLock.getReadLockKey()).Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// IsHeldByCurrentThread 检查读锁是否被当前线程持有
func (rl *redisReadLock) IsHeldByCurrentThread(ctx context.Context) (bool, error) {
	readerKey := fmt.Sprintf("%s:%s", rl.owner, rl.threadID)
	exists, err := rl.rwLock.client.HExists(ctx, rl.rwLock.getReadLockKey(), readerKey).Result()
	if err != nil {
		return false, err
	}
	return exists, nil
}

// GetHoldCount 获取当前线程持有读锁的次数
func (rl *redisReadLock) GetHoldCount(ctx context.Context) (int, error) {
	readerKey := fmt.Sprintf("%s:%s", rl.owner, rl.threadID)
	countStr, err := rl.rwLock.client.HGet(ctx, rl.rwLock.getReadCountKey(), readerKey).Result()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, err
	}

	count, err := strconv.Atoi(countStr)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// GetRemainingTimeToLive 获取读锁的剩余生存时间
func (rl *redisReadLock) GetRemainingTimeToLive(ctx context.Context) (time.Duration, error) {
	ttl, err := rl.rwLock.client.TTL(ctx, rl.rwLock.getReadLockKey()).Result()
	if err != nil {
		return 0, err
	}

	if ttl < 0 {
		return 0, nil
	}

	return ttl, nil
}

// ForceUnlock 强制释放读锁
func (rl *redisReadLock) ForceUnlock(ctx context.Context) error {
	readerKey := fmt.Sprintf("%s:%s", rl.owner, rl.threadID)

	pipe := rl.rwLock.client.TxPipeline()
	pipe.HDel(ctx, rl.rwLock.getReadLockKey(), readerKey)
	pipe.HDel(ctx, rl.rwLock.getReadCountKey(), readerKey)

	_, err := pipe.Exec(ctx)
	return err
}

// Delete 删除读锁
func (rl *redisReadLock) Delete(ctx context.Context) error {
	return rl.ForceUnlock(ctx)
}

// 写锁实现

// Lock 获取写锁
func (wl *redisWriteLock) Lock(ctx context.Context) error {
	return wl.lockInternal(ctx, wl.rwLock.config.WaitTimeout, true)
}

// TryLock 尝试获取写锁
func (wl *redisWriteLock) TryLock(ctx context.Context) (bool, error) {
	err := wl.lockInternal(ctx, 0, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// TryLockWithTimeout 在指定时间内尝试获取写锁
func (wl *redisWriteLock) TryLockWithTimeout(ctx context.Context, timeout time.Duration) (bool, error) {
	err := wl.lockInternal(ctx, timeout, false)
	if err != nil {
		if err == ErrLockTimeout {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// lockInternal 写锁的内部加锁逻辑
func (wl *redisWriteLock) lockInternal(ctx context.Context, waitTimeout time.Duration, block bool) error {
	startTime := time.Now()

	for {
		// 尝试获取写锁
		acquired, err := wl.tryAcquireWriteLock(ctx)
		if err != nil {
			return err
		}

		if acquired {
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
		case <-time.After(wl.rwLock.config.RetryInterval):
			continue
		}
	}
}

// tryAcquireWriteLock 尝试获取写锁
func (wl *redisWriteLock) tryAcquireWriteLock(ctx context.Context) (bool, error) {
	script := `
		local read_lock_key = KEYS[1]
		local write_lock_key = KEYS[2]
		local stats_key = KEYS[3]
		
		local owner = ARGV[1]
		local thread_id = ARGV[2]
		local expire_seconds = tonumber(ARGV[3])
		local current_time = tonumber(ARGV[4])
		
		-- 检查是否有读锁
		local read_count = redis.call('HLEN', read_lock_key)
		if read_count > 0 then
			-- 检查是否是同一个线程的读锁
			local reader_key = owner .. ':' .. thread_id
			local own_read_lock = redis.call('HGET', read_lock_key, reader_key)
			
			-- 如果有其他线程的读锁，不能获取写锁
			if read_count > 1 or (read_count == 1 and not own_read_lock) then
				return 0
			end
		end
		
		-- 检查是否有其他线程的写锁
		local write_lock = redis.call('GET', write_lock_key)
		if write_lock then
			local write_info = cjson.decode(write_lock)
			local expire_time = tonumber(write_info.expire_time)
			
			if expire_time > current_time then
				local write_owner = write_info.owner .. ':' .. write_info.thread_id
				local current_owner = owner .. ':' .. thread_id
				
				-- 如果不是同一个线程，不能获取写锁
				if write_owner ~= current_owner then
					return 0
				end
			end
		end
		
		-- 创建写锁信息
		local write_info = {
			name = write_lock_key,
			owner = owner,
			thread_id = thread_id,
			acquire_time = current_time,
			expire_time = current_time + expire_seconds
		}
		
		local write_data = cjson.encode(write_info)
		
		-- 设置写锁
		redis.call('SET', write_lock_key, write_data, 'EX', expire_seconds)
		
		-- 更新统计信息
		redis.call('HINCRBY', stats_key, 'total_write_acquired', 1)
		
		return 1
	`

	keys := []string{
		wl.rwLock.getReadLockKey(),
		wl.rwLock.getWriteLockKey(),
		wl.rwLock.getStatsKey(),
	}

	args := []interface{}{
		wl.owner,
		wl.threadID,
		int(wl.rwLock.config.LeaseDuration.Seconds()),
		time.Now().Unix(),
	}

	result, err := wl.rwLock.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return false, fmt.Errorf("failed to acquire write lock: %w", err)
	}

	return result.(int64) == 1, nil
}

// Unlock 释放写锁
func (wl *redisWriteLock) Unlock(ctx context.Context) error {
	script := `
		local write_lock_key = KEYS[1]
		local stats_key = KEYS[2]
		
		local owner = ARGV[1]
		local thread_id = ARGV[2]
		
		-- 检查写锁是否存在且属于当前线程
		local write_lock = redis.call('GET', write_lock_key)
		if not write_lock then
			return 0  -- 写锁不存在
		end
		
		local write_info = cjson.decode(write_lock)
		local write_owner = write_info.owner .. ':' .. write_info.thread_id
		local current_owner = owner .. ':' .. thread_id
		
		if write_owner ~= current_owner then
			return -1  -- 不是锁的持有者
		end
		
		-- 删除写锁
		redis.call('DEL', write_lock_key)
		
		-- 更新统计信息
		redis.call('HINCRBY', stats_key, 'total_write_released', 1)
		
		return 1
	`

	keys := []string{wl.rwLock.getWriteLockKey(), wl.rwLock.getStatsKey()}
	args := []interface{}{wl.owner, wl.threadID}

	result, err := wl.rwLock.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return fmt.Errorf("failed to release write lock: %w", err)
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

// IsLocked 检查写锁是否被持有
func (wl *redisWriteLock) IsLocked(ctx context.Context) (bool, error) {
	exists, err := wl.rwLock.client.Exists(ctx, wl.rwLock.getWriteLockKey()).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

// IsHeldByCurrentThread 检查写锁是否被当前线程持有
func (wl *redisWriteLock) IsHeldByCurrentThread(ctx context.Context) (bool, error) {
	lockData, err := wl.rwLock.client.Get(ctx, wl.rwLock.getWriteLockKey()).Result()
	if err != nil {
		if err == redis.Nil {
			return false, nil
		}
		return false, err
	}

	var lockInfo LockInfo
	if err := json.Unmarshal([]byte(lockData), &lockInfo); err != nil {
		return false, err
	}

	currentOwner := fmt.Sprintf("%s:%s", wl.owner, wl.threadID)
	existingOwner := fmt.Sprintf("%s:%s", lockInfo.Owner, lockInfo.ThreadID)

	return currentOwner == existingOwner, nil
}

// GetHoldCount 获取当前线程持有写锁的次数（写锁不支持重入）
func (wl *redisWriteLock) GetHoldCount(ctx context.Context) (int, error) {
	isHeld, err := wl.IsHeldByCurrentThread(ctx)
	if err != nil {
		return 0, err
	}

	if isHeld {
		return 1, nil
	}

	return 0, nil
}

// GetRemainingTimeToLive 获取写锁的剩余生存时间
func (wl *redisWriteLock) GetRemainingTimeToLive(ctx context.Context) (time.Duration, error) {
	ttl, err := wl.rwLock.client.TTL(ctx, wl.rwLock.getWriteLockKey()).Result()
	if err != nil {
		return 0, err
	}

	if ttl < 0 {
		return 0, nil
	}

	return ttl, nil
}

// ForceUnlock 强制释放写锁
func (wl *redisWriteLock) ForceUnlock(ctx context.Context) error {
	return wl.rwLock.client.Del(ctx, wl.rwLock.getWriteLockKey()).Err()
}

// Delete 删除写锁
func (wl *redisWriteLock) Delete(ctx context.Context) error {
	return wl.ForceUnlock(ctx)
}
