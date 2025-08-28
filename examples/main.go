package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/qiulin/remix/reliablequeue"
	"github.com/redis/go-redis/v9"
)

func main() {
	// 创建 Redis 客户端
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	// 配置队列
	config := reliablequeue.DefaultConfig()
	config.VisibilityTimeout = 30 * time.Second
	config.MaxRetries = 3
	config.RetryDelay = 2 * time.Second
	config.CleanupInterval = 60 * time.Second

	// 创建可靠队列
	queue := reliablequeue.NewReliableQueue(rdb, "my_queue", config)
	defer func() {
		ctx := context.Background()
		queue.Destroy(ctx)
	}()

	ctx := context.Background()

	// 示例 1: 基本的生产者和消费者
	fmt.Println("=== 基本的生产者和消费者示例 ===")
	basicProducerConsumer(ctx, queue)

	// 示例 2: 延迟消息
	fmt.Println("\n=== 延迟消息示例 ===")
	delayedMessageExample(ctx, queue)

	// 示例 3: 消息重试和死信队列
	fmt.Println("\n=== 消息重试和死信队列示例 ===")
	retryAndDeadLetterExample(ctx, queue)

	// 示例 4: 获取统计信息
	fmt.Println("\n=== 队列统计信息示例 ===")
	statsExample(ctx, queue)
}

func basicProducerConsumer(ctx context.Context, queue reliablequeue.ReliableQueue) {
	// 生产者：添加消息到队列
	messages := []string{
		"Hello, World!",
		"这是一条中文消息",
		"Task: Process user data",
		"Task: Send email notification",
	}

	for _, msg := range messages {
		err := queue.Offer(ctx, msg)
		if err != nil {
			log.Printf("Failed to offer message: %v", err)
			continue
		}
		fmt.Printf("生产者：已发送消息 '%s'\n", msg)
	}

	// 消费者：处理消息
	for {
		msg, err := queue.PollWithTimeout(ctx, 2*time.Second)
		if err != nil {
			log.Printf("Failed to poll message: %v", err)
			break
		}
		if msg == nil {
			fmt.Println("消费者：没有更多消息")
			break
		}

		fmt.Printf("消费者：收到消息 '%s' (ID: %s)\n", msg.Body, msg.ID)

		// 模拟消息处理
		time.Sleep(500 * time.Millisecond)

		// 模拟 90% 的成功率
		if len(msg.Body)%10 != 0 {
			// 成功处理，确认消息
			err = queue.Ack(ctx, msg.ID)
			if err != nil {
				log.Printf("Failed to ack message: %v", err)
			} else {
				fmt.Printf("消费者：成功处理消息 '%s'\n", msg.Body)
			}
		} else {
			// 处理失败，拒绝消息
			err = queue.Nack(ctx, msg.ID)
			if err != nil {
				log.Printf("Failed to nack message: %v", err)
			} else {
				fmt.Printf("消费者：处理失败，拒绝消息 '%s'\n", msg.Body)
			}
		}
	}
}

func delayedMessageExample(ctx context.Context, queue reliablequeue.ReliableQueue) {
	// 发送延迟消息
	delay := 3 * time.Second
	fmt.Printf("发送延迟消息，延迟时间：%v\n", delay)

	start := time.Now()
	err := queue.OfferWithDelay(ctx, "这是一条延迟消息", delay)
	if err != nil {
		log.Printf("Failed to offer delayed message: %v", err)
		return
	}

	// 立即尝试获取消息（应该为空）
	msg, err := queue.Poll(ctx)
	if err != nil {
		log.Printf("Failed to poll message: %v", err)
		return
	}
	if msg == nil {
		fmt.Println("立即获取消息：无消息（符合预期）")
	}

	// 等待延迟时间
	fmt.Printf("等待 %v...\n", delay)
	time.Sleep(delay + 500*time.Millisecond)

	// 再次尝试获取消息
	msg, err = queue.Poll(ctx)
	if err != nil {
		log.Printf("Failed to poll delayed message: %v", err)
		return
	}
	if msg != nil {
		elapsed := time.Since(start)
		fmt.Printf("延迟后获取消息：'%s'，实际延迟：%v\n", msg.Body, elapsed)
		err = queue.Ack(ctx, msg.ID)
		if err != nil {
			log.Printf("Failed to ack delayed message: %v", err)
		}
	}
}

func retryAndDeadLetterExample(ctx context.Context, queue reliablequeue.ReliableQueue) {
	// 发送一条会失败的消息
	err := queue.Offer(ctx, "失败的任务消息")
	if err != nil {
		log.Printf("Failed to offer message: %v", err)
		return
	}

	fmt.Println("模拟消息处理失败，触发重试机制...")

	// 模拟多次处理失败
	for i := 0; i < 5; i++ {
		msg, err := queue.PollWithTimeout(ctx, 3*time.Second)
		if err != nil {
			log.Printf("Failed to poll message: %v", err)
			continue
		}
		if msg == nil {
			fmt.Println("没有更多消息")
			break
		}

		fmt.Printf("第 %d 次处理消息 '%s' (重试次数: %d/%d)\n",
			i+1, msg.Body, msg.RetryCount, msg.MaxRetries)

		// 模拟处理失败
		err = queue.Nack(ctx, msg.ID)
		if err != nil {
			log.Printf("Failed to nack message: %v", err)
		} else {
			fmt.Printf("处理失败，已拒绝消息\n")
		}

		// 如果超过最大重试次数，消息会进入死信队列
		if msg.RetryCount >= msg.MaxRetries {
			fmt.Println("消息已超过最大重试次数，将进入死信队列")
			break
		}

		// 等待重试延迟
		time.Sleep(2 * time.Second)
	}

	// 检查死信队列
	time.Sleep(1 * time.Second)
	deadLetterSize, err := queue.DeadLetterSize(ctx)
	if err != nil {
		log.Printf("Failed to get dead letter size: %v", err)
		return
	}

	fmt.Printf("死信队列大小：%d\n", deadLetterSize)

	if deadLetterSize > 0 {
		// 获取死信消息
		deadLetters, err := queue.GetDeadLetters(ctx, 5)
		if err != nil {
			log.Printf("Failed to get dead letters: %v", err)
			return
		}

		for _, deadMsg := range deadLetters {
			fmt.Printf("死信消息：'%s' (ID: %s, 重试次数: %d)\n",
				deadMsg.Body, deadMsg.ID, deadMsg.RetryCount)
		}

		// 演示重新排队死信消息
		if len(deadLetters) > 0 {
			fmt.Printf("重新排队死信消息: %s\n", deadLetters[0].ID)
			err = queue.RequeueDeadLetter(ctx, deadLetters[0].ID)
			if err != nil {
				log.Printf("Failed to requeue dead letter: %v", err)
			} else {
				fmt.Println("死信消息已重新排队")

				// 重新处理消息
				msg, err := queue.Poll(ctx)
				if err == nil && msg != nil {
					fmt.Printf("重新处理消息：'%s'\n", msg.Body)
					err = queue.Ack(ctx, msg.ID)
					if err != nil {
						log.Printf("Failed to ack requeued message: %v", err)
					} else {
						fmt.Println("重新处理成功")
					}
				}
			}
		}
	}
}

func statsExample(ctx context.Context, queue reliablequeue.ReliableQueue) {
	// 添加一些测试消息
	for i := 0; i < 5; i++ {
		err := queue.Offer(ctx, fmt.Sprintf("统计测试消息 %d", i+1))
		if err != nil {
			log.Printf("Failed to offer message: %v", err)
		}
	}

	// 处理一些消息
	for i := 0; i < 3; i++ {
		msg, err := queue.Poll(ctx)
		if err != nil {
			log.Printf("Failed to poll message: %v", err)
			continue
		}
		if msg == nil {
			break
		}

		if i == 2 {
			// 最后一个消息不确认，保持在处理状态
			fmt.Printf("消息 '%s' 保持在处理状态\n", msg.Body)
		} else {
			err = queue.Ack(ctx, msg.ID)
			if err != nil {
				log.Printf("Failed to ack message: %v", err)
			}
		}
	}

	// 获取统计信息
	stats, err := queue.GetStats(ctx)
	if err != nil {
		log.Printf("Failed to get stats: %v", err)
		return
	}

	fmt.Printf("队列统计信息：\n")
	fmt.Printf("  待处理消息：%d\n", stats.PendingMessages)
	fmt.Printf("  处理中消息：%d\n", stats.ProcessingMessages)
	fmt.Printf("  死信消息：%d\n", stats.DeadLetterMessages)
	fmt.Printf("  总入队消息：%d\n", stats.TotalEnqueued)
	fmt.Printf("  总处理成功：%d\n", stats.TotalProcessed)
	fmt.Printf("  总失败消息：%d\n", stats.TotalFailed)
}
