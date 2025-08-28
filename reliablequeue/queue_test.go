package reliablequeue

import (
	"context"
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
func cleanupQueue(t *testing.T, rq ReliableQueue) {
	ctx := context.Background()
	err := rq.Destroy(ctx)
	assert.NoError(t, err)
}

func TestBasicQueueOperations(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	config.CleanupInterval = 1 * time.Second // 缩短清理间隔便于测试

	rq := NewReliableQueue(client, "test_queue", config)
	defer cleanupQueue(t, rq)

	ctx := context.Background()

	t.Run("Offer and Poll", func(t *testing.T) {
		// 测试入队
		err := rq.Offer(ctx, "test message 1")
		require.NoError(t, err)

		err = rq.Offer(ctx, "test message 2")
		require.NoError(t, err)

		// 测试队列大小
		size, err := rq.Size(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(2), size)

		// 测试出队
		msg1, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, msg1)
		assert.Equal(t, "test message 1", msg1.Body)

		msg2, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, msg2)
		assert.Equal(t, "test message 2", msg2.Body)

		// 测试空队列
		msg3, err := rq.Poll(ctx)
		require.NoError(t, err)
		assert.Nil(t, msg3)
	})

	t.Run("Message Acknowledgment", func(t *testing.T) {
		// 入队消息
		err := rq.Offer(ctx, "ack test message")
		require.NoError(t, err)

		// 出队消息
		msg, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, msg)

		// 检查处理队列大小
		processingSize, err := rq.ProcessingSize(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(1), processingSize)

		// 确认消息
		err = rq.Ack(ctx, msg.ID)
		require.NoError(t, err)

		// 检查处理队列大小应该为 0
		processingSize, err = rq.ProcessingSize(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), processingSize)
	})

	t.Run("Message Nack and Retry", func(t *testing.T) {
		// 入队消息
		err := rq.Offer(ctx, "nack test message")
		require.NoError(t, err)

		// 出队消息
		msg, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, msg)
		originalRetryCount := msg.RetryCount

		// 拒绝消息
		err = rq.Nack(ctx, msg.ID)
		require.NoError(t, err)

		// 消息应该重新回到队列
		time.Sleep(100 * time.Millisecond) // 等待重试延迟
		retryMsg, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, retryMsg)
		assert.Equal(t, msg.ID, retryMsg.ID)
		assert.Equal(t, originalRetryCount+1, retryMsg.RetryCount)

		// 确认消息以清理
		err = rq.Ack(ctx, retryMsg.ID)
		require.NoError(t, err)
	})
}

func TestDelayedMessages(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	rq := NewReliableQueue(client, "test_delayed_queue", config)
	defer cleanupQueue(t, rq)

	ctx := context.Background()

	t.Run("Delayed Message", func(t *testing.T) {
		delay := 1 * time.Second
		start := time.Now()

		// 添加延迟消息
		err := rq.OfferWithDelay(ctx, "delayed message", delay)
		require.NoError(t, err)

		// 立即尝试获取消息应该为空
		msg, err := rq.Poll(ctx)
		require.NoError(t, err)
		assert.Nil(t, msg)

		// 等待延迟时间后再次尝试
		time.Sleep(delay + 100*time.Millisecond)
		msg, err = rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, msg)
		assert.Equal(t, "delayed message", msg.Body)

		elapsed := time.Since(start)
		assert.GreaterOrEqual(t, elapsed, delay)

		// 确认消息
		err = rq.Ack(ctx, msg.ID)
		require.NoError(t, err)
	})
}

func TestDeadLetterQueue(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	config.MaxRetries = 2 // 设置较小的重试次数便于测试
	config.RetryDelay = 100 * time.Millisecond

	rq := NewReliableQueue(client, "test_dlq_queue", config)
	defer cleanupQueue(t, rq)

	ctx := context.Background()

	t.Run("Messages Go to Dead Letter Queue After Max Retries", func(t *testing.T) {
		// 入队消息
		err := rq.Offer(ctx, "dlq test message")
		require.NoError(t, err)

		var msg *Message
		// 循环拒绝消息直到超过最大重试次数
		for i := 0; i <= config.MaxRetries; i++ {
			msg, err = rq.Poll(ctx)
			require.NoError(t, err)
			require.NotNil(t, msg)

			err = rq.Nack(ctx, msg.ID)
			require.NoError(t, err)

			time.Sleep(config.RetryDelay + 50*time.Millisecond)
		}

		// 消息应该不再出现在正常队列中
		normalMsg, err := rq.Poll(ctx)
		require.NoError(t, err)
		assert.Nil(t, normalMsg)

		// 检查死信队列
		dlqSize, err := rq.DeadLetterSize(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(1), dlqSize)

		// 获取死信消息
		deadLetters, err := rq.GetDeadLetters(ctx, 10)
		require.NoError(t, err)
		assert.Len(t, deadLetters, 1)
		assert.Equal(t, msg.ID, deadLetters[0].ID)
	})

	t.Run("Requeue Dead Letter Message", func(t *testing.T) {
		// 获取死信队列中的消息
		deadLetters, err := rq.GetDeadLetters(ctx, 1)
		require.NoError(t, err)
		require.Len(t, deadLetters, 1)

		deadMsg := deadLetters[0]

		// 重新排队死信消息
		err = rq.RequeueDeadLetter(ctx, deadMsg.ID)
		require.NoError(t, err)

		// 死信队列应该为空
		dlqSize, err := rq.DeadLetterSize(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), dlqSize)

		// 正常队列应该有消息
		size, err := rq.Size(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(1), size)

		// 获取并确认消息
		msg, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, msg)
		assert.Equal(t, deadMsg.ID, msg.ID)

		err = rq.Ack(ctx, msg.ID)
		require.NoError(t, err)
	})

	t.Run("Purge Dead Letters", func(t *testing.T) {
		// 添加一些消息到死信队列
		for i := 0; i < 3; i++ {
			err := rq.Offer(ctx, "purge test message")
			require.NoError(t, err)

			msg, err := rq.Poll(ctx)
			require.NoError(t, err)
			require.NotNil(t, msg)

			// 拒绝消息直到进入死信队列
			for j := 0; j <= config.MaxRetries; j++ {
				err = rq.Nack(ctx, msg.ID)
				require.NoError(t, err)
				time.Sleep(config.RetryDelay + 50*time.Millisecond)

				if j < config.MaxRetries {
					msg, err = rq.Poll(ctx)
					require.NoError(t, err)
					require.NotNil(t, msg)
				}
			}
		}

		// 验证死信队列有消息
		dlqSize, err := rq.DeadLetterSize(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(3), dlqSize)

		// 清空死信队列
		err = rq.PurgeDeadLetters(ctx)
		require.NoError(t, err)

		// 验证死信队列为空
		dlqSize, err = rq.DeadLetterSize(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), dlqSize)
	})
}

func TestQueueStats(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	rq := NewReliableQueue(client, "test_stats_queue", config)
	defer cleanupQueue(t, rq)

	ctx := context.Background()

	// 添加一些消息
	for i := 0; i < 5; i++ {
		err := rq.Offer(ctx, "stats test message")
		require.NoError(t, err)
	}

	// 处理一些消息
	msg1, err := rq.Poll(ctx)
	require.NoError(t, err)
	err = rq.Ack(ctx, msg1.ID)
	require.NoError(t, err)

	msg2, err := rq.Poll(ctx)
	require.NoError(t, err)
	err = rq.Nack(ctx, msg2.ID)
	require.NoError(t, err)

	// 获取统计信息
	stats, err := rq.GetStats(ctx)
	require.NoError(t, err)

	assert.Equal(t, int64(5), stats.TotalEnqueued)
	assert.Equal(t, int64(1), stats.TotalProcessed)
	assert.GreaterOrEqual(t, stats.PendingMessages, int64(3)) // 至少 3 条待处理消息
}
