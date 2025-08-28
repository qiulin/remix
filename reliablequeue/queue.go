package reliablequeue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// redisReliableQueue Redis 实现的可靠队列
type redisReliableQueue struct {
	client      redis.Cmdable
	name        string
	config      *Config
	cleanupStop chan struct{}
	cleanupDone chan struct{}
}

// NewReliableQueue 创建一个新的可靠队列
func NewReliableQueue(client redis.Cmdable, name string, config *Config) ReliableQueue {
	if config == nil {
		config = DefaultConfig()
	}

	rq := &redisReliableQueue{
		client:      client,
		name:        name,
		config:      config,
		cleanupStop: make(chan struct{}),
		cleanupDone: make(chan struct{}),
	}

	// 启动清理任务
	go rq.cleanupTask()

	return rq
}

// 获取各种队列的 Redis key
func (rq *redisReliableQueue) getQueueKey() string {
	return fmt.Sprintf("reliable_queue:%s:pending", rq.name)
}

func (rq *redisReliableQueue) getProcessingKey() string {
	return fmt.Sprintf("reliable_queue:%s:processing", rq.name)
}

func (rq *redisReliableQueue) getDeadLetterKey() string {
	return fmt.Sprintf("reliable_queue:%s:dead_letter", rq.name)
}

func (rq *redisReliableQueue) getDelayedKey() string {
	return fmt.Sprintf("reliable_queue:%s:delayed", rq.name)
}

func (rq *redisReliableQueue) getStatsKey() string {
	return fmt.Sprintf("reliable_queue:%s:stats", rq.name)
}

func (rq *redisReliableQueue) getMessageKey(messageID string) string {
	return fmt.Sprintf("reliable_queue:%s:message:%s", rq.name, messageID)
}

// Offer 添加消息到队列
func (rq *redisReliableQueue) Offer(ctx context.Context, message string) error {
	return rq.OfferWithDelay(ctx, message, 0)
}

// OfferWithDelay 添加延迟消息到队列
func (rq *redisReliableQueue) OfferWithDelay(ctx context.Context, message string, delay time.Duration) error {
	messageID := uuid.New().String()
	now := time.Now()
	visibleTime := now.Add(delay)

	msg := &Message{
		ID:          messageID,
		Body:        message,
		EnqueueTime: now,
		VisibleTime: visibleTime,
		RetryCount:  0,
		MaxRetries:  rq.config.MaxRetries,
	}

	// 序列化消息
	msgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// 使用 Lua 脚本确保原子性
	script := `
		local message_key = KEYS[1]
		local stats_key = KEYS[2]
		local queue_key = KEYS[3]
		local delayed_key = KEYS[4]
		
		local message_data = ARGV[1]
		local visible_time = tonumber(ARGV[2])
		local current_time = tonumber(ARGV[3])
		
		-- 保存消息数据
		redis.call('SET', message_key, message_data)
		
		-- 更新统计信息
		redis.call('HINCRBY', stats_key, 'total_enqueued', 1)
		
		-- 如果是立即可见，加入待处理队列；否则加入延迟队列
		if visible_time <= current_time then
			redis.call('LPUSH', queue_key, KEYS[1])
		else
			redis.call('ZADD', delayed_key, visible_time, KEYS[1])
		end
		
		return 'OK'
	`

	keys := []string{
		rq.getMessageKey(messageID),
		rq.getStatsKey(),
		rq.getQueueKey(),
		rq.getDelayedKey(),
	}

	args := []interface{}{
		string(msgData),
		visibleTime.Unix(),
		now.Unix(),
	}

	_, err = rq.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return fmt.Errorf("failed to offer message: %w", err)
	}

	return nil
}

// Poll 从队列获取消息
func (rq *redisReliableQueue) Poll(ctx context.Context) (*Message, error) {
	return rq.PollWithTimeout(ctx, 0)
}

// PollWithTimeout 从队列获取消息，支持超时
func (rq *redisReliableQueue) PollWithTimeout(ctx context.Context, timeout time.Duration) (*Message, error) {
	// 首先移动延迟消息到待处理队列
	if err := rq.moveDelayedMessages(ctx); err != nil {
		return nil, fmt.Errorf("failed to move delayed messages: %w", err)
	}

	var result []string
	var err error

	if timeout > 0 {
		// 阻塞式获取
		result, err = rq.client.BRPop(ctx, timeout, rq.getQueueKey()).Result()
	} else {
		// 非阻塞式获取
		messageKey, err := rq.client.RPop(ctx, rq.getQueueKey()).Result()
		if err != nil {
			if err == redis.Nil {
				return nil, nil
			}
			return nil, err
		}
		result = []string{rq.getQueueKey(), messageKey}
	}

	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to poll message: %w", err)
	}

	if len(result) < 2 {
		return nil, nil
	}

	messageKey := result[1]

	// 获取消息数据
	msgData, err := rq.client.Get(ctx, messageKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get message data: %w", err)
	}

	var msg Message
	if err := json.Unmarshal([]byte(msgData), &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}

	// 移动消息到处理队列
	now := time.Now()
	msg.DequeueTime = now

	// 更新消息数据
	updatedMsgData, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated message: %w", err)
	}

	// 使用 Lua 脚本确保原子性
	script := `
		local message_key = KEYS[1]
		local processing_key = KEYS[2]
		
		local message_data = ARGV[1]
		local timeout_score = tonumber(ARGV[2])
		
		-- 更新消息数据
		redis.call('SET', message_key, message_data)
		
		-- 添加到处理队列，使用超时时间作为分数
		redis.call('ZADD', processing_key, timeout_score, message_key)
		
		return 'OK'
	`

	keys := []string{messageKey, rq.getProcessingKey()}
	timeoutScore := now.Add(rq.config.VisibilityTimeout).Unix()
	args := []interface{}{string(updatedMsgData), timeoutScore}

	_, err = rq.client.Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to move message to processing: %w", err)
	}

	return &msg, nil
}

// Ack 确认消息处理完成
func (rq *redisReliableQueue) Ack(ctx context.Context, messageID string) error {
	messageKey := rq.getMessageKey(messageID)

	script := `
		local message_key = KEYS[1]
		local processing_key = KEYS[2]
		local stats_key = KEYS[3]
		
		-- 从处理队列中移除
		local removed = redis.call('ZREM', processing_key, message_key)
		
		if removed == 1 then
			-- 删除消息数据
			redis.call('DEL', message_key)
			
			-- 更新统计信息
			redis.call('HINCRBY', stats_key, 'total_processed', 1)
			
			return 'OK'
		else
			return 'NOT_FOUND'
		end
	`

	keys := []string{messageKey, rq.getProcessingKey(), rq.getStatsKey()}

	result, err := rq.client.Eval(ctx, script, keys).Result()
	if err != nil {
		return fmt.Errorf("failed to ack message: %w", err)
	}

	if result.(string) == "NOT_FOUND" {
		return fmt.Errorf("message not found in processing queue")
	}

	return nil
}

// Nack 拒绝消息，重新排队或移到死信队列
func (rq *redisReliableQueue) Nack(ctx context.Context, messageID string) error {
	messageKey := rq.getMessageKey(messageID)

	// 由于 Redis Lua 脚本中处理 JSON 比较复杂，我们采用分步操作
	// 首先获取消息并检查是否在处理队列中
	exists, err := rq.client.ZScore(ctx, rq.getProcessingKey(), messageKey).Result()
	if err != nil {
		if err == redis.Nil {
			return fmt.Errorf("message not found in processing queue")
		}
		return fmt.Errorf("failed to check message in processing queue: %w", err)
	}

	// 获取消息数据
	msgData, err := rq.client.Get(ctx, messageKey).Result()
	if err != nil {
		return fmt.Errorf("failed to get message data: %w", err)
	}

	var msg Message
	if err := json.Unmarshal([]byte(msgData), &msg); err != nil {
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}

	// 增加重试次数
	msg.RetryCount++

	// 使用事务处理
	pipe := rq.client.TxPipeline()

	// 从处理队列移除
	pipe.ZRem(ctx, rq.getProcessingKey(), messageKey)

	if msg.RetryCount <= msg.MaxRetries {
		// 重新排队
		msg.VisibleTime = time.Now().Add(rq.config.RetryDelay)
		updatedMsgData, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal updated message: %w", err)
		}
		pipe.Set(ctx, messageKey, string(updatedMsgData), 0)
		pipe.LPush(ctx, rq.getQueueKey(), messageKey)
	} else if rq.config.EnableDeadLetter {
		// 移到死信队列
		pipe.LPush(ctx, rq.getDeadLetterKey(), messageKey)
		pipe.HIncrBy(ctx, rq.getStatsKey(), "total_failed", 1)
	} else {
		// 删除消息
		pipe.Del(ctx, messageKey)
		pipe.HIncrBy(ctx, rq.getStatsKey(), "total_failed", 1)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to nack message: %w", err)
	}

	_ = exists // 消除未使用变量警告

	return nil
}

// Size 返回待处理消息数量
func (rq *redisReliableQueue) Size(ctx context.Context) (int64, error) {
	return rq.client.LLen(ctx, rq.getQueueKey()).Result()
}

// ProcessingSize 返回正在处理的消息数量
func (rq *redisReliableQueue) ProcessingSize(ctx context.Context) (int64, error) {
	return rq.client.ZCard(ctx, rq.getProcessingKey()).Result()
}

// DeadLetterSize 返回死信队列消息数量
func (rq *redisReliableQueue) DeadLetterSize(ctx context.Context) (int64, error) {
	return rq.client.LLen(ctx, rq.getDeadLetterKey()).Result()
}

// Clear 清空队列
func (rq *redisReliableQueue) Clear(ctx context.Context) error {
	keys := []string{
		rq.getQueueKey(),
		rq.getProcessingKey(),
		rq.getDeadLetterKey(),
		rq.getDelayedKey(),
	}

	pipe := rq.client.TxPipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// Delete 删除队列
func (rq *redisReliableQueue) Delete(ctx context.Context) error {
	return rq.Clear(ctx)
}

// GetDeadLetters 获取死信队列中的消息
func (rq *redisReliableQueue) GetDeadLetters(ctx context.Context, limit int64) ([]*Message, error) {
	messageKeys, err := rq.client.LRange(ctx, rq.getDeadLetterKey(), 0, limit-1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get dead letter message keys: %w", err)
	}

	var messages []*Message
	for _, messageKey := range messageKeys {
		msgData, err := rq.client.Get(ctx, messageKey).Result()
		if err != nil {
			if err == redis.Nil {
				continue
			}
			return nil, fmt.Errorf("failed to get message data: %w", err)
		}

		var msg Message
		if err := json.Unmarshal([]byte(msgData), &msg); err != nil {
			continue
		}

		messages = append(messages, &msg)
	}

	return messages, nil
}

// Destroy 销毁队列，清理所有数据
func (rq *redisReliableQueue) Destroy(ctx context.Context) error {
	// 停止清理任务
	close(rq.cleanupStop)
	<-rq.cleanupDone

	// 获取所有相关的 key
	pattern := fmt.Sprintf("reliable_queue:%s:*", rq.name)
	keys, err := rq.client.Keys(ctx, pattern).Result()
	if err != nil {
		return fmt.Errorf("failed to get queue keys: %w", err)
	}

	if len(keys) > 0 {
		_, err = rq.client.Del(ctx, keys...).Result()
		if err != nil {
			return fmt.Errorf("failed to delete queue keys: %w", err)
		}
	}

	return nil
}

// moveDelayedMessages 移动到期的延迟消息到待处理队列
func (rq *redisReliableQueue) moveDelayedMessages(ctx context.Context) error {
	script := `
		local delayed_key = KEYS[1]
		local queue_key = KEYS[2]
		local current_time = tonumber(ARGV[1])
		
		-- 获取所有到期的消息
		local messages = redis.call('ZRANGEBYSCORE', delayed_key, 0, current_time)
		
		for i = 1, #messages do
			-- 移动到待处理队列
			redis.call('LPUSH', queue_key, messages[i])
			-- 从延迟队列移除
			redis.call('ZREM', delayed_key, messages[i])
		end
		
		return #messages
	`

	keys := []string{rq.getDelayedKey(), rq.getQueueKey()}
	args := []interface{}{time.Now().Unix()}

	_, err := rq.client.Eval(ctx, script, keys, args...).Result()
	return err
}

// cleanupTask 后台清理任务，处理超时的消息
func (rq *redisReliableQueue) cleanupTask() {
	defer close(rq.cleanupDone)

	ticker := time.NewTicker(rq.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rq.cleanupStop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			rq.processTimeoutMessages(ctx)
			rq.moveDelayedMessages(ctx)
			cancel()
		}
	}
}

// processTimeoutMessages 处理超时的消息
func (rq *redisReliableQueue) processTimeoutMessages(ctx context.Context) error {
	currentTime := time.Now().Unix()

	// 获取所有超时的消息
	timeoutMessages, err := rq.client.ZRangeByScore(ctx, rq.getProcessingKey(), &redis.ZRangeBy{
		Min: "0",
		Max: strconv.FormatInt(currentTime, 10),
	}).Result()

	if err != nil {
		return fmt.Errorf("failed to get timeout messages: %w", err)
	}

	// 处理每个超时的消息
	for _, messageKey := range timeoutMessages {
		if err := rq.handleTimeoutMessage(ctx, messageKey); err != nil {
			// 记录错误但继续处理其他消息
			fmt.Printf("Error handling timeout message %s: %v\n", messageKey, err)
		}
	}

	return nil
}

// handleTimeoutMessage 处理单个超时消息
func (rq *redisReliableQueue) handleTimeoutMessage(ctx context.Context, messageKey string) error {
	// 获取消息数据
	msgData, err := rq.client.Get(ctx, messageKey).Result()
	if err != nil {
		if err == redis.Nil {
			// 消息已被删除，从处理队列移除
			rq.client.ZRem(ctx, rq.getProcessingKey(), messageKey)
			return nil
		}
		return fmt.Errorf("failed to get message data: %w", err)
	}

	var msg Message
	if err := json.Unmarshal([]byte(msgData), &msg); err != nil {
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}

	// 增加重试次数
	msg.RetryCount++

	// 使用事务处理
	pipe := rq.client.TxPipeline()

	// 从处理队列移除
	pipe.ZRem(ctx, rq.getProcessingKey(), messageKey)

	if msg.RetryCount <= msg.MaxRetries {
		// 重新排队
		msg.VisibleTime = time.Now().Add(rq.config.RetryDelay)
		updatedMsgData, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal updated message: %w", err)
		}
		pipe.Set(ctx, messageKey, string(updatedMsgData), 0)
		pipe.LPush(ctx, rq.getQueueKey(), messageKey)
	} else if rq.config.EnableDeadLetter {
		// 移到死信队列
		pipe.LPush(ctx, rq.getDeadLetterKey(), messageKey)
		pipe.HIncrBy(ctx, rq.getStatsKey(), "total_failed", 1)
	} else {
		// 删除消息
		pipe.Del(ctx, messageKey)
		pipe.HIncrBy(ctx, rq.getStatsKey(), "total_failed", 1)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to handle timeout message: %w", err)
	}

	return nil
}

// RequeueDeadLetter 将死信队列中的消息重新放回正常队列
func (rq *redisReliableQueue) RequeueDeadLetter(ctx context.Context, messageID string) error {
	messageKey := rq.getMessageKey(messageID)

	script := `
		local message_key = KEYS[1]
		local dead_letter_key = KEYS[2]
		local queue_key = KEYS[3]
		
		-- 检查消息是否在死信队列中
		local pos = redis.call('LPOS', dead_letter_key, message_key)
		if pos then
			-- 从死信队列移除
			redis.call('LREM', dead_letter_key, 1, message_key)
			-- 重置重试次数并加入正常队列
			local message_data = redis.call('GET', message_key)
			if message_data then
				redis.call('LPUSH', queue_key, message_key)
				return 'OK'
			end
		end
		
		return 'NOT_FOUND'
	`

	keys := []string{messageKey, rq.getDeadLetterKey(), rq.getQueueKey()}

	result, err := rq.client.Eval(ctx, script, keys).Result()
	if err != nil {
		return fmt.Errorf("failed to requeue dead letter: %w", err)
	}

	if result.(string) == "NOT_FOUND" {
		return fmt.Errorf("message not found in dead letter queue")
	}

	// 重置消息的重试次数
	msgData, err := rq.client.Get(ctx, messageKey).Result()
	if err != nil {
		return fmt.Errorf("failed to get message data: %w", err)
	}

	var msg Message
	if err := json.Unmarshal([]byte(msgData), &msg); err != nil {
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}

	// 重置重试次数
	msg.RetryCount = 0
	msg.VisibleTime = time.Now()

	updatedMsgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal updated message: %w", err)
	}

	return rq.client.Set(ctx, messageKey, string(updatedMsgData), 0).Err()
}

// PurgeDeadLetters 清空死信队列
func (rq *redisReliableQueue) PurgeDeadLetters(ctx context.Context) error {
	// 获取所有死信消息的 key
	messageKeys, err := rq.client.LRange(ctx, rq.getDeadLetterKey(), 0, -1).Result()
	if err != nil {
		return fmt.Errorf("failed to get dead letter keys: %w", err)
	}

	// 删除所有死信消息数据
	pipe := rq.client.TxPipeline()
	for _, messageKey := range messageKeys {
		pipe.Del(ctx, messageKey)
	}
	// 清空死信队列
	pipe.Del(ctx, rq.getDeadLetterKey())

	_, err = pipe.Exec(ctx)
	return err
}

// GetStats 获取队列统计信息
func (rq *redisReliableQueue) GetStats(ctx context.Context) (*QueueStats, error) {
	pipe := rq.client.Pipeline()

	// 获取各队列大小
	pendingCmd := pipe.LLen(ctx, rq.getQueueKey())
	processingCmd := pipe.ZCard(ctx, rq.getProcessingKey())
	deadLetterCmd := pipe.LLen(ctx, rq.getDeadLetterKey())

	// 获取统计信息
	statsCmd := pipe.HMGet(ctx, rq.getStatsKey(),
		"total_enqueued", "total_processed", "total_failed")

	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get queue stats: %w", err)
	}

	stats := &QueueStats{
		PendingMessages:    pendingCmd.Val(),
		ProcessingMessages: processingCmd.Val(),
		DeadLetterMessages: deadLetterCmd.Val(),
	}

	// 解析统计信息
	statsValues := statsCmd.Val()
	if len(statsValues) >= 3 {
		if val, ok := statsValues[0].(string); ok {
			if totalEnqueued, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.TotalEnqueued = totalEnqueued
			}
		}
		if val, ok := statsValues[1].(string); ok {
			if totalProcessed, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.TotalProcessed = totalProcessed
			}
		}
		if val, ok := statsValues[2].(string); ok {
			if totalFailed, err := strconv.ParseInt(val, 10, 64); err == nil {
				stats.TotalFailed = totalFailed
			}
		}
	}

	return stats, nil
}
