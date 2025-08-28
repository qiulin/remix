package reliablequeue

import (
	"context"
	"time"
)

// ReliableQueue 可靠队列接口，提供类似 Redisson RReliableQueue 的功能
type ReliableQueue interface {
	// Offer 将消息添加到队列末尾
	Offer(ctx context.Context, message string) error

	// OfferWithDelay 将消息添加到队列末尾，指定延迟时间
	OfferWithDelay(ctx context.Context, message string, delay time.Duration) error

	// Poll 从队列头部获取一个消息，如果队列为空则返回 nil
	Poll(ctx context.Context) (*Message, error)

	// PollWithTimeout 从队列头部获取一个消息，如果在指定时间内没有消息则返回 nil
	PollWithTimeout(ctx context.Context, timeout time.Duration) (*Message, error)

	// Ack 确认消息已处理完成，将从 processing 队列中移除
	Ack(ctx context.Context, messageID string) error

	// Nack 拒绝消息，将重新放回队列或进入死信队列
	Nack(ctx context.Context, messageID string) error

	// Size 返回队列中待处理消息数量
	Size(ctx context.Context) (int64, error)

	// ProcessingSize 返回正在处理中的消息数量
	ProcessingSize(ctx context.Context) (int64, error)

	// DeadLetterSize 返回死信队列中的消息数量
	DeadLetterSize(ctx context.Context) (int64, error)

	// Clear 清空队列
	Clear(ctx context.Context) error

	// Delete 删除队列
	Delete(ctx context.Context) error

	// GetDeadLetters 获取死信队列中的消息
	GetDeadLetters(ctx context.Context, limit int64) ([]*Message, error)

	// Destroy 销毁队列，清理所有相关数据
	Destroy(ctx context.Context) error

	// RequeueDeadLetter 将死信队列中的消息重新放回正常队列
	RequeueDeadLetter(ctx context.Context, messageID string) error

	// PurgeDeadLetters 清空死信队列
	PurgeDeadLetters(ctx context.Context) error

	// GetStats 获取队列统计信息
	GetStats(ctx context.Context) (*QueueStats, error)
}

// Message 表示队列中的消息
type Message struct {
	// ID 消息的唯一标识符
	ID string `json:"id"`

	// Body 消息内容
	Body string `json:"body"`

	// EnqueueTime 消息入队时间
	EnqueueTime time.Time `json:"enqueue_time"`

	// VisibleTime 消息变为可见的时间（用于延迟消息）
	VisibleTime time.Time `json:"visible_time"`

	// DequeueTime 消息出队时间
	DequeueTime time.Time `json:"dequeue_time"`

	// RetryCount 重试次数
	RetryCount int `json:"retry_count"`

	// MaxRetries 最大重试次数
	MaxRetries int `json:"max_retries"`
}

// Config 队列配置
type Config struct {
	// VisibilityTimeout 消息可见性超时时间，默认 30 秒
	VisibilityTimeout time.Duration

	// MaxRetries 最大重试次数，默认 3 次
	MaxRetries int

	// RetryDelay 重试延迟时间，默认 1 秒
	RetryDelay time.Duration

	// CleanupInterval 清理任务执行间隔，默认 60 秒
	CleanupInterval time.Duration

	// EnableDeadLetter 是否启用死信队列，默认 true
	EnableDeadLetter bool
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		VisibilityTimeout: 30 * time.Second,
		MaxRetries:        3,
		RetryDelay:        1 * time.Second,
		CleanupInterval:   60 * time.Second,
		EnableDeadLetter:  true,
	}
}

// QueueStats 队列统计信息
type QueueStats struct {
	// PendingMessages 待处理消息数量
	PendingMessages int64 `json:"pending_messages"`

	// ProcessingMessages 正在处理的消息数量
	ProcessingMessages int64 `json:"processing_messages"`

	// DeadLetterMessages 死信消息数量
	DeadLetterMessages int64 `json:"dead_letter_messages"`

	// TotalEnqueued 总入队消息数量
	TotalEnqueued int64 `json:"total_enqueued"`

	// TotalProcessed 总处理成功消息数量
	TotalProcessed int64 `json:"total_processed"`

	// TotalFailed 总失败消息数量
	TotalFailed int64 `json:"total_failed"`
}
