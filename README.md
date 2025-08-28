# Remix - Go 分布式中间件

## 📋 项目概述

**Remix** 是 Redisson 的 Go 语言开源实现，致力于为 Go 开发者提供完整的 Redis 分布式中间件解决方案。项目参考 Redisson 的成熟设计，实现了其核心功能模块，包括可靠队列（Reliable Queue）和分布式锁（Distributed Lock）等组件。

### 🎯 项目目标

- **完整性**：全面实现 Redisson 的核心功能，为 Go 生态提供成熟的分布式解决方案
- **兼容性**：API 设计尽可能贴近 Redisson，降低从 Java 迁移的学习成本
- **性能**：充分利用 Go 语言的并发特性，提供高性能的分布式组件
- **易用性**：提供简洁友好的 API，完善的文档和示例
- **可靠性**：借鉴 Redisson 的成熟架构，确保在生产环境的稳定性

### 🔧 技术栈

- **语言**：Go 1.21+
- **存储**：Redis 6.0+
- **客户端**：github.com/redis/go-redis/v9
- **测试**：github.com/stretchr/testify

### 📦 核心模块

目前已实现的核心模块包括：

1. **Reliable Queue (可靠队列)**：企业级消息队列解决方案
2. **Distributed Lock (分布式锁)**：完整的分布式锁工具集

未来计划实现：
- Rate Limiter (限流器)
- Semaphore (信号量)
- Pub/Sub (发布订阅)
- 更多 Redisson 核心功能...

---

## 📫 Reliable Queue (可靠队列)

可靠队列是一个企业级的消息队列实现，提供与 Redisson Reliable Queue 完全兼容的功能特性。

### ✨ 功能特性

- **FIFO 队列**：严格的先进先出消息处理
- **消息确认机制**：消息需要显式确认（Ack）才会从队列中删除
- **可见性超时**：未确认的消息在超时后重新变为可见，支持消息重新处理
- **重试机制**：失败的消息可以重试指定次数
- **死信队列**：超过重试次数的消息自动进入死信队列
- **延迟消息**：支持延迟指定时间后处理的消息
- **并发安全**：支持多个生产者和消费者并发操作
- **持久化**：基于 Redis 的持久化存储
- **统计信息**：提供详细的队列运行统计

### 🚀 快速开始

#### 安装

```bash
go get github.com/qiulin/remix
```

#### 基本使用

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

### ⚙️ 配置选项

```go
config := &reliablequeue.Config{
    VisibilityTimeout: 30 * time.Second,  // 消息可见性超时
    MaxRetries:        3,                 // 最大重试次数
    RetryDelay:        1 * time.Second,   // 重试延迟
    CleanupInterval:   60 * time.Second,  // 清理任务间隔
    EnableDeadLetter:  true,              // 启用死信队列
}
```

### 📖 详细功能

#### 1. 基本队列操作

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

#### 2. 队列状态查询

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

#### 3. 死信队列管理

```go
// 获取死信队列中的消息
deadLetters, err := queue.GetDeadLetters(ctx, 10)

// 重新排队死信消息
err := queue.RequeueDeadLetter(ctx, messageID)

// 清空死信队列
err := queue.PurgeDeadLetters(ctx)
```

#### 4. 队列管理

```go
// 清空队列
err := queue.Clear(ctx)

// 删除队列
err := queue.Delete(ctx)

// 销毁队列（清理所有相关数据）
err := queue.Destroy(ctx)
```

### 📋 消息结构

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

### 🔍 与 Redisson Reliable Queue 的对比

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

### 🏗️ 架构设计

#### Redis 数据结构

1. **待处理队列** (`reliable_queue:name:pending`): Redis List，存储待处理的消息 ID
2. **处理队列** (`reliable_queue:name:processing`): Redis Sorted Set，存储正在处理的消息 ID（按超时时间排序）
3. **延迟队列** (`reliable_queue:name:delayed`): Redis Sorted Set，存储延迟消息 ID（按可见时间排序）
4. **死信队列** (`reliable_queue:name:dead_letter`): Redis List，存储失败的消息 ID
5. **消息数据** (`reliable_queue:name:message:id`): Redis String，存储具体的消息内容
6. **统计信息** (`reliable_queue:name:stats`): Redis Hash，存储队列统计数据

#### 工作流程

1. **消息入队**：消息被添加到待处理队列或延迟队列
2. **消息出队**：从待处理队列获取消息，移动到处理队列
3. **消息确认**：从处理队列删除消息，更新统计信息
4. **消息拒绝**：增加重试次数，重新排队或移入死信队列
5. **超时处理**：定期检查处理队列中的超时消息，自动重试
6. **延迟处理**：定期将到期的延迟消息移动到待处理队列

### 🧪 测试

#### 运行单元测试

```bash
# 确保 Redis 服务运行在 localhost:6379
go test ./reliablequeue -v
```

#### 运行集成测试

```bash
go test ./reliablequeue -v -run TestConcurrent
```

#### 运行性能测试

```bash
go test ./reliablequeue -bench=.
```

### 📝 示例程序

运行完整的示例程序：

```bash
go run examples/main.go
```

示例程序包含：
- 基本的生产者和消费者
- 延迟消息处理
- 重试和死信队列
- 统计信息获取

### ⚠️ 注意事项

1. **Redis 版本要求**：需要 Redis 6.0+ （支持 `LPOS` 命令）
2. **Go-Redis 版本**：使用 github.com/redis/go-redis/v9
3. **并发控制**：多个消费者可以安全地并发处理消息
4. **消息顺序**：严格保证 FIFO 顺序
5. **内存使用**：大量消息时注意 Redis 内存使用
6. **网络分区**：Redis 连接断开时，正在处理的消息可能会超时重试

---

## 🔒 Distributed Lock (分布式锁)

分布式锁模块提供了完整的分布式锁解决方案，包括基础锁、公平锁、读写锁等多种类型。

### ✨ 功能特性

- **互斥锁**：基本的分布式互斥锁，支持可重入
- **公平锁**：FIFO 顺序获取锁，防止线程饥饿
- **读写锁**：支持多个读者和单个写者的读写锁
- **自动续期**：防止锁在业务处理过程中意外过期（Watch Dog）
- **锁统计**：提供详细的锁使用统计信息
- **强制释放**：支持在异常情况下强制释放锁
- **超时控制**：支持获取锁的超时设置
- **异步获取**：支持异步方式获取锁

### 🚀 快速开始

#### 基本使用

```go
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/redis/go-redis/v9"
    "github.com/qiulin/remix/lock"
)

func main() {
    // 创建 Redis 客户端
    rdb := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })
    
    // 创建分布式锁
    config := lock.DefaultLockConfig()
    distributedLock := lock.NewLock(rdb, "my_lock", config)
    defer distributedLock.Delete(context.Background())
    
    ctx := context.Background()
    
    // 获取锁
    err := distributedLock.Lock(ctx)
    if err != nil {
        panic(err)
    }
    fmt.Println("获取到分布式锁")
    
    // 业务处理
    time.Sleep(1 * time.Second)
    
    // 释放锁
    err = distributedLock.Unlock(ctx)
    if err != nil {
        panic(err)
    }
    fmt.Println("释放分布式锁")
}
```

### ⚙️ 配置选项

```go
config := &lock.LockConfig{
    TTL:            30 * time.Second,  // 锁的生存时间
    WatchDogTimeout: 10 * time.Second, // 自动续期间隔
    RetryDelay:     100 * time.Millisecond, // 重试延迟
    RetryJitter:    50 * time.Millisecond,  // 重试抖动
}
```

### 📖 详细功能

#### 1. 基础互斥锁

```go
// 创建锁
distributedLock := lock.NewLock(rdb, "my_lock", config)

// 获取锁
err := distributedLock.Lock(ctx)

// 尝试获取锁（非阻塞）
locked, err := distributedLock.TryLock(ctx)

// 带超时获取锁
err := distributedLock.TryLockWithTimeout(ctx, 5*time.Second)

// 释放锁
err := distributedLock.Unlock(ctx)

// 检查是否被当前线程持有
held := distributedLock.IsHeldByCurrentThread(ctx)

// 获取重入次数
count := distributedLock.GetHoldCount(ctx)
```

#### 2. 公平锁

```go
// 创建公平锁
fairLock := lock.NewFairLock(rdb, "fair_lock", config)

// 获取锁（FIFO 顺序）
err := fairLock.Lock(ctx)

// 异步获取锁
resultCh := fairLock.LockAsync(ctx)
select {
case err := <-resultCh:
    if err != nil {
        // 处理错误
    }
    // 获取锁成功
case <-time.After(10 * time.Second):
    // 超时处理
}

// 获取等待队列长度
queueLength, err := fairLock.GetQueueLength(ctx)
```

#### 3. 读写锁

```go
// 创建读写锁
rwLock := lock.NewReadWriteLock(rdb, "rw_lock", config)

// 获取读锁
readLock := rwLock.ReadLock()
err := readLock.Lock(ctx)
// ... 读操作 ...
err = readLock.Unlock(ctx)

// 获取写锁
writeLock := rwLock.WriteLock()
err := writeLock.Lock(ctx)
// ... 写操作 ...
err = writeLock.Unlock(ctx)

// 获取当前读者数量
readerCount, err := rwLock.GetReadLockCount(ctx)

// 检查是否有写锁
hasWriteLock, err := rwLock.IsWriteLocked(ctx)
```

#### 4. 锁统计和管理

```go
// 获取锁统计信息
stats, err := distributedLock.GetStats(ctx)
fmt.Printf("锁持有次数: %d\n", stats.HoldCount)
fmt.Printf("锁获取次数: %d\n", stats.AcquireCount)
fmt.Printf("锁释放次数: %d\n", stats.ReleaseCount)

// 获取锁信息
info, err := distributedLock.GetLockInfo(ctx)
fmt.Printf("锁持有者: %s\n", info.HolderID)
fmt.Printf("锁过期时间: %v\n", info.ExpireTime)

// 强制释放锁
err := distributedLock.ForceUnlock(ctx)

// 删除锁
err := distributedLock.Delete(ctx)
```

### 🏗️ 架构设计

#### Redis 数据结构

1. **锁键** (`lock:name`): Redis String，存储锁的持有者和重入次数
2. **等待队列** (`lock:name:queue`): Redis Sorted Set，公平锁的等待队列
3. **读锁计数** (`lock:name:rwlock:read`): Redis Hash，读写锁的读者计数
4. **写锁标识** (`lock:name:rwlock:write`): Redis String，写锁的持有者
5. **统计信息** (`lock:name:stats`): Redis Hash，锁的统计数据

#### 核心算法

1. **可重入实现**：使用线程 ID 和计数器实现可重入锁
2. **公平性保证**：使用有序集合维护等待队列，确保 FIFO 顺序
3. **自动续期**：Watch Dog 机制定期延长锁的 TTL
4. **原子操作**：所有锁操作使用 Lua 脚本保证原子性

### 🧪 测试

#### 运行单元测试

```bash
go test ./lock -v
```

#### 运行集成测试

```bash
go test ./lock -v -run TestIntegration
```

#### 运行并发测试

```bash
go test ./lock -v -run TestConcurrent
```

### 📝 示例程序

运行分布式锁示例：

```bash
go run examples/distributed_lock_demo.go
```

示例程序包含：
- 基础锁的使用
- 公平锁的演示
- 读写锁的应用
- 并发场景测试

### ⚠️ 注意事项

1. **Redis 版本要求**：需要 Redis 2.6+ （支持 Lua 脚本）
2. **时钟同步**：分布式环境下确保各节点时钟同步
3. **网络分区**：Redis 连接断开时，锁可能会因 TTL 过期而自动释放
4. **Watch Dog**：长时间持有锁时，Watch Dog 会自动续期防止意外释放
5. **异常处理**：确保在异常情况下正确释放锁，避免死锁

---

## 📋 总体说明

### 🧪 运行测试

确保 Redis 服务运行在 `localhost:6379`，然后运行测试：

```bash
# 运行所有测试
go test ./... -v

# 运行特定模块测试
go test ./reliablequeue -v
go test ./lock -v

# 运行性能测试
go test ./... -bench=.
```

### 📝 完整示例

查看 `examples/` 目录下的完整示例程序：

- `examples/main.go` - 可靠队列完整示例
- `examples/distributed_lock_demo.go` - 分布式锁完整示例

### 🤝 贡献指南

欢迎贡献代码和建议！请查看 [CONTRIBUTING.md](CONTRIBUTING.md) 了解详细信息。

### 📄 许可证

Apache License 2.0
