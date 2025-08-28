package reliablequeue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageTimeout(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	config.VisibilityTimeout = 1 * time.Second
	config.CleanupInterval = 500 * time.Millisecond

	rq := NewReliableQueue(client, "test_timeout_queue", config)
	defer cleanupQueue(t, rq)

	ctx := context.Background()

	t.Run("Message Timeout and Retry", func(t *testing.T) {
		// 入队消息
		err := rq.Offer(ctx, "timeout test message")
		require.NoError(t, err)

		// 出队消息但不确认
		msg, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, msg)
		originalRetryCount := msg.RetryCount

		// 等待超时
		time.Sleep(config.VisibilityTimeout + config.CleanupInterval + 200*time.Millisecond)

		// 消息应该重新出现在队列中
		retryMsg, err := rq.Poll(ctx)
		require.NoError(t, err)
		require.NotNil(t, retryMsg)
		assert.Equal(t, msg.ID, retryMsg.ID)
		assert.Equal(t, originalRetryCount+1, retryMsg.RetryCount)

		// 确认消息
		err = rq.Ack(ctx, retryMsg.ID)
		require.NoError(t, err)
	})
}

func TestConcurrentOperations(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	rq := NewReliableQueue(client, "test_concurrent_queue", config)
	defer cleanupQueue(t, rq)

	ctx := context.Background()

	t.Run("Concurrent Producers and Consumers", func(t *testing.T) {
		const numProducers = 3
		const numConsumers = 2
		const messagesPerProducer = 10
		const totalMessages = numProducers * messagesPerProducer

		var wg sync.WaitGroup
		processed := make(chan string, totalMessages)

		// 启动生产者
		for i := 0; i < numProducers; i++ {
			wg.Add(1)
			go func(producerID int) {
				defer wg.Done()
				for j := 0; j < messagesPerProducer; j++ {
					message := fmt.Sprintf("producer-%d-message-%d", producerID, j)
					err := rq.Offer(ctx, message)
					assert.NoError(t, err)
				}
			}(i)
		}

		// 启动消费者
		for i := 0; i < numConsumers; i++ {
			wg.Add(1)
			go func(consumerID int) {
				defer wg.Done()
				for {
					msg, err := rq.PollWithTimeout(ctx, 2*time.Second)
					if err != nil {
						assert.NoError(t, err)
						return
					}
					if msg == nil {
						// 没有更多消息，退出
						return
					}

					// 模拟处理时间
					time.Sleep(10 * time.Millisecond)

					err = rq.Ack(ctx, msg.ID)
					assert.NoError(t, err)

					processed <- msg.Body

					// 检查是否处理完所有消息
					select {
					case <-time.After(100 * time.Millisecond):
						// 继续处理
					default:
						if len(processed) >= totalMessages {
							return
						}
					}
				}
			}(i)
		}

		// 等待所有生产者完成
		wg.Wait()

		// 等待所有消息被处理
		timeout := time.After(10 * time.Second)
		processedCount := 0
		processedMessages := make(map[string]bool)

	waitLoop:
		for {
			select {
			case msg := <-processed:
				processedMessages[msg] = true
				processedCount++
				if processedCount >= totalMessages {
					break waitLoop
				}
			case <-timeout:
				t.Fatalf("Timeout waiting for messages to be processed. Processed: %d/%d", processedCount, totalMessages)
			}
		}

		assert.Equal(t, totalMessages, processedCount)
		assert.Len(t, processedMessages, totalMessages)

		// 验证队列为空
		size, err := rq.Size(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), size)

		processingSize, err := rq.ProcessingSize(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), processingSize)
	})
}

func TestQueuePersistence(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	queueName := "test_persistence_queue"

	// 创建队列并添加消息
	rq1 := NewReliableQueue(client, queueName, config)
	ctx := context.Background()

	err := rq1.Offer(ctx, "persistent message 1")
	require.NoError(t, err)

	err = rq1.Offer(ctx, "persistent message 2")
	require.NoError(t, err)

	// 模拟应用重启 - 销毁当前队列实例但不清理数据
	rq1.(*redisReliableQueue).cleanupStop <- struct{}{}
	<-rq1.(*redisReliableQueue).cleanupDone

	// 创建新的队列实例
	rq2 := NewReliableQueue(client, queueName, config)
	defer cleanupQueue(t, rq2)

	// 验证消息仍然存在
	size, err := rq2.Size(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), size)

	// 获取消息
	msg1, err := rq2.Poll(ctx)
	require.NoError(t, err)
	require.NotNil(t, msg1)
	assert.Equal(t, "persistent message 1", msg1.Body)

	msg2, err := rq2.Poll(ctx)
	require.NoError(t, err)
	require.NotNil(t, msg2)
	assert.Equal(t, "persistent message 2", msg2.Body)

	// 确认消息
	err = rq2.Ack(ctx, msg1.ID)
	require.NoError(t, err)

	err = rq2.Ack(ctx, msg2.ID)
	require.NoError(t, err)
}

func TestErrorHandling(t *testing.T) {
	client := setupRedisClient()
	config := DefaultConfig()
	rq := NewReliableQueue(client, "test_error_queue", config)
	defer cleanupQueue(t, rq)

	ctx := context.Background()

	t.Run("Ack Non-existent Message", func(t *testing.T) {
		err := rq.Ack(ctx, "non-existent-message-id")
		assert.Error(t, err)
	})

	t.Run("Nack Non-existent Message", func(t *testing.T) {
		err := rq.Nack(ctx, "non-existent-message-id")
		assert.Error(t, err)
	})

	t.Run("Requeue Non-existent Dead Letter", func(t *testing.T) {
		err := rq.RequeueDeadLetter(ctx, "non-existent-message-id")
		assert.Error(t, err)
	})
}

// Benchmark 测试
func BenchmarkOfferPoll(b *testing.B) {
	client := setupRedisClient()
	config := DefaultConfig()
	rq := NewReliableQueue(client, "benchmark_queue", config)
	defer func() {
		ctx := context.Background()
		rq.Destroy(ctx)
	}()

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			err := rq.Offer(ctx, "benchmark message")
			if err != nil {
				b.Fatal(err)
			}

			msg, err := rq.Poll(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if msg != nil {
				err = rq.Ack(ctx, msg.ID)
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
