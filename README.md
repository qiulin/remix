# Go Reliable Queue

基于 Redis 的可靠队列实现，参考 Redisson 的 Reliable Queue 功能。

## 功能特性

- **FIFO 队列**：严格的先进先出消息处理
- **消息确认机制**：消息需要显式确认（Ack）才会从队列中删除
- **可见性超时**：未确认的消息在超时后重新变为可见，支持消息重新处理
- **重试机制**：失败的消息可以重试指定次数
- **死信队列**：超过重试次数的消息自动进入死信队列
- **延迟消息**：支持延迟指定时间后处理的消息
- **并发安全**：支持多个生产者和消费者并发操作
- **持久化**：基于 Redis 的持久化存储
- **统计信息**：提供详细的队列统计信息

## 快速开始

### 安装

```bash
go get github.com/qiulin/remix
```

### 基本使用

```go
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/redis/go-redis/v9"
    "github.com/qiulin/remix/reliablequeue"
)

func main() {
    // 创建 Redis 客户端
    rdb := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })
    
    // 创建队列
    config := reliablequeue.DefaultConfig()
    queue := reliablequeue.NewReliableQueue(rdb, "my_queue", config)
    defer queue.Destroy(context.Background())
    
    ctx := context.Background()
    
    // 生产者：发送消息
    err := queue.Offer(ctx, "Hello, World!")
    if err != nil {
        panic(err)
    }
    
    // 消费者：接收和处理消息
    msg, err := queue.Poll(ctx)
    if err != nil {
        panic(err)
    }
    
    if msg != nil {
        fmt.Printf("收到消息: %s\n", msg.Body)
        
        // 处理成功，确认消息
        err = queue.Ack(ctx, msg.ID)
        if err != nil {
            panic(err)
        }
    }
}
```

### 配置选项

```go
config := &reliablequeue.Config{
    VisibilityTimeout: 30 * time.Second,  // 消息可见性超时
    MaxRetries:        3,                 // 最大重试次数
    RetryDelay:        1 * time.Second,   // 重试延迟
    CleanupInterval:   60 * time.Second,  // 清理任务间隔
    EnableDeadLetter:  true,              // 启用死信队列
}
```

## 详细功能

### 1. 基本队列操作

```go
// 发送消息
err := queue.Offer(ctx, "message content")

// 发送延迟消息
err := queue.OfferWithDelay(ctx, "delayed message", 5*time.Second)

// 接收消息
msg, err := queue.Poll(ctx)

// 带超时的接收消息
msg, err := queue.PollWithTimeout(ctx, 10*time.Second)

// 确认消息处理成功
err := queue.Ack(ctx, msg.ID)

// 拒绝消息（重新排队或进入死信队列）
err := queue.Nack(ctx, msg.ID)
```

### 2. 队列状态查询

```go
// 获取待处理消息数量
size, err := queue.Size(ctx)

// 获取正在处理的消息数量
processingSize, err := queue.ProcessingSize(ctx)

// 获取死信队列消息数量
deadLetterSize, err := queue.DeadLetterSize(ctx)

// 获取详细统计信息
stats, err := queue.GetStats(ctx)
```

### 3. 死信队列管理

```go
// 获取死信队列中的消息
deadLetters, err := queue.GetDeadLetters(ctx, 10)

// 重新排队死信消息
err := queue.RequeueDeadLetter(ctx, messageID)

// 清空死信队列
err := queue.PurgeDeadLetters(ctx)
```

### 4. 队列管理

```go
// 清空队列
err := queue.Clear(ctx)

// 删除队列
err := queue.Delete(ctx)

// 销毁队列（清理所有相关数据）
err := queue.Destroy(ctx)
```

## 消息结构

```go
type Message struct {
    ID          string    `json:"id"`           // 消息唯一标识
    Body        string    `json:"body"`         // 消息内容
    EnqueueTime time.Time `json:"enqueue_time"` // 入队时间
    VisibleTime time.Time `json:"visible_time"` // 可见时间
    DequeueTime time.Time `json:"dequeue_time"` // 出队时间
    RetryCount  int       `json:"retry_count"`  // 重试次数
    MaxRetries  int       `json:"max_retries"`  // 最大重试次数
}
```

## 与 Redisson Reliable Queue 的对比

| 功能 | Redisson | 本实现 | 说明 |
|------|----------|--------|---------|
| FIFO 队列 | ✅ | ✅ | 严格先进先出 |
| 消息确认 | ✅ | ✅ | 支持 Ack/Nack |
| 可见性超时 | ✅ | ✅ | 可配置超时时间 |
| 重试机制 | ✅ | ✅ | 可配置重试次数和延迟 |
| 死信队列 | ✅ | ✅ | 自动处理失败消息 |
| 延迟消息 | ✅ | ✅ | 支持延迟处理 |
| 持久化 | ✅ | ✅ | 基于 Redis 持久化 |
| 并发安全 | ✅ | ✅ | 线程/协程安全 |
| 统计信息 | ✅ | ✅ | 详细的队列统计 |

## 架构设计

### Redis 数据结构

1. **待处理队列** (`reliable_queue:name:pending`): Redis List，存储待处理的消息 ID
2. **处理队列** (`reliable_queue:name:processing`): Redis Sorted Set，存储正在处理的消息 ID（按超时时间排序）
3. **延迟队列** (`reliable_queue:name:delayed`): Redis Sorted Set，存储延迟消息 ID（按可见时间排序）
4. **死信队列** (`reliable_queue:name:dead_letter`): Redis List，存储失败的消息 ID
5. **消息数据** (`reliable_queue:name:message:id`): Redis String，存储具体的消息内容
6. **统计信息** (`reliable_queue:name:stats`): Redis Hash，存储队列统计数据

### 工作流程

1. **消息入队**：消息被添加到待处理队列或延迟队列
2. **消息出队**：从待处理队列获取消息，移动到处理队列
3. **消息确认**：从处理队列删除消息，更新统计信息
4. **消息拒绝**：增加重试次数，重新排队或移入死信队列
5. **超时处理**：定期检查处理队列中的超时消息，自动重试
6. **延迟处理**：定期将到期的延迟消息移动到待处理队列

## 测试

### 运行单元测试

```bash
# 确保 Redis 服务运行在 localhost:6379
go test ./reliablequeue -v
```

### 运行集成测试

```bash
go test ./reliablequeue -v -run TestConcurrent
```

### 运行性能测试

```bash
go test ./reliablequeue -bench=.
```

## 示例程序

运行完整的示例程序：

```bash
go run examples/main.go
```

示例程序包含：
- 基本的生产者和消费者
- 延迟消息处理
- 重试和死信队列
- 统计信息获取

## 注意事项

1. **Redis 版本要求**：需要 Redis 6.0+ （支持 `LPOS` 命令）
2. **Go-Redis 版本**：使用 github.com/redis/go-redis/v9
3. **并发控制**：多个消费者可以安全地并发处理消息
4. **消息顺序**：严格保证 FIFO 顺序
5. **内存使用**：大量消息时注意 Redis 内存使用
6. **网络分区**：Redis 连接断开时，正在处理的消息可能会超时重试

## 许可证

Apache License 2.0
