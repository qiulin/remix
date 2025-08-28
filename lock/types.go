package lock

import (
	"context"
	"time"
)

// Lock 分布式锁接口，提供类似 Redisson RLock 的功能
type Lock interface {
	// Lock 获取锁，阻塞直到成功获取锁
	Lock(ctx context.Context) error

	// TryLock 尝试获取锁，立即返回结果
	TryLock(ctx context.Context) (bool, error)

	// TryLockWithTimeout 在指定时间内尝试获取锁
	TryLockWithTimeout(ctx context.Context, timeout time.Duration) (bool, error)

	// Unlock 释放锁
	Unlock(ctx context.Context) error

	// IsLocked 检查锁是否被持有
	IsLocked(ctx context.Context) (bool, error)

	// IsHeldByCurrentThread 检查锁是否被当前线程/协程持有
	IsHeldByCurrentThread(ctx context.Context) (bool, error)

	// GetHoldCount 获取当前线程/协程持有锁的次数（可重入锁）
	GetHoldCount(ctx context.Context) (int, error)

	// GetRemainingTimeToLive 获取锁的剩余生存时间
	GetRemainingTimeToLive(ctx context.Context) (time.Duration, error)

	// ForceUnlock 强制释放锁（危险操作，谨慎使用）
	ForceUnlock(ctx context.Context) error

	// Delete 删除锁
	Delete(ctx context.Context) error
}

// FairLock 公平锁接口，提供 FIFO 顺序的锁获取
type FairLock interface {
	Lock

	// LockAsync 异步获取锁
	LockAsync(ctx context.Context) <-chan error

	// GetQueueLength 获取等待队列长度
	GetQueueLength(ctx context.Context) (int64, error)

	// HasQueuedThreads 检查是否有线程在等待队列中
	HasQueuedThreads(ctx context.Context) (bool, error)
}

// ReadWriteLock 读写锁接口
type ReadWriteLock interface {
	// ReadLock 获取读锁
	ReadLock() Lock

	// WriteLock 获取写锁
	WriteLock() Lock
}

// LockConfig 锁配置
type LockConfig struct {
	// LeaseDuration 锁的租约时间，默认 30 秒
	LeaseDuration time.Duration

	// WaitTimeout 等待锁的超时时间，默认 10 秒
	WaitTimeout time.Duration

	// RetryInterval 重试间隔时间，默认 100 毫秒
	RetryInterval time.Duration

	// Reentrant 是否支持可重入，默认 true
	Reentrant bool

	// Fair 是否为公平锁，默认 false
	Fair bool

	// AutoRenewal 是否自动续期，默认 true
	AutoRenewal bool

	// RenewalInterval 续期检查间隔，默认为租约时间的 1/3
	RenewalInterval time.Duration
}

// DefaultLockConfig 返回默认锁配置
func DefaultLockConfig() *LockConfig {
	leaseDuration := 30 * time.Second
	return &LockConfig{
		LeaseDuration:   leaseDuration,
		WaitTimeout:     10 * time.Second,
		RetryInterval:   100 * time.Millisecond,
		Reentrant:       true,
		Fair:            false,
		AutoRenewal:     true,
		RenewalInterval: leaseDuration / 3,
	}
}

// LockInfo 锁信息
type LockInfo struct {
	// Name 锁名称
	Name string `json:"name"`

	// Owner 锁持有者标识
	Owner string `json:"owner"`

	// ThreadID 线程/协程 ID
	ThreadID string `json:"thread_id"`

	// HoldCount 持有次数（可重入锁）
	HoldCount int `json:"hold_count"`

	// AcquireTime 获取锁的时间
	AcquireTime time.Time `json:"acquire_time"`

	// ExpireTime 锁过期时间
	ExpireTime time.Time `json:"expire_time"`

	// LastRenewalTime 最后续期时间
	LastRenewalTime time.Time `json:"last_renewal_time"`
}

// LockStats 锁统计信息
type LockStats struct {
	// Name 锁名称
	Name string `json:"name"`

	// IsLocked 是否被锁定
	IsLocked bool `json:"is_locked"`

	// Owner 当前持有者
	Owner string `json:"owner"`

	// HoldCount 持有次数
	HoldCount int `json:"hold_count"`

	// RemainingTTL 剩余生存时间
	RemainingTTL time.Duration `json:"remaining_ttl"`

	// WaitingThreads 等待的线程数
	WaitingThreads int64 `json:"waiting_threads"`

	// TotalAcquired 总获取次数
	TotalAcquired int64 `json:"total_acquired"`

	// TotalReleased 总释放次数
	TotalReleased int64 `json:"total_released"`

	// TotalTimeout 总超时次数
	TotalTimeout int64 `json:"total_timeout"`
}

// LockError 锁相关错误
type LockError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *LockError) Error() string {
	return e.Message
}

// 预定义错误类型
var (
	ErrLockTimeout      = &LockError{Type: "TIMEOUT", Message: "lock acquisition timeout"}
	ErrLockNotHeld      = &LockError{Type: "NOT_HELD", Message: "lock not held by current thread"}
	ErrLockAlreadyHeld  = &LockError{Type: "ALREADY_HELD", Message: "lock already held"}
	ErrLockNotFound     = &LockError{Type: "NOT_FOUND", Message: "lock not found"}
	ErrInvalidOperation = &LockError{Type: "INVALID_OPERATION", Message: "invalid lock operation"}
)
