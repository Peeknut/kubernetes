/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workqueue

import (
	"container/heap"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/clock"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

// DelayingInterface is an Interface that can Add an item at a later time. This makes it easier to
// requeue items after failures without ending up in a hot-loop.
type DelayingInterface interface {
	Interface
	// AddAfter adds an item to the workqueue after the indicated duration has passed
	// 在指定的时间后将元素添加到工作队列中
	AddAfter(item interface{}, duration time.Duration)
}

// NewDelayingQueue constructs a new workqueue with delayed queuing ability
func NewDelayingQueue() DelayingInterface {
	return NewDelayingQueueWithCustomClock(clock.RealClock{}, "")
}

// NewDelayingQueueWithCustomQueue constructs a new workqueue with ability to
// inject custom queue Interface instead of the default one
func NewDelayingQueueWithCustomQueue(q Interface, name string) DelayingInterface {
	return newDelayingQueue(clock.RealClock{}, q, name)
}

// NewNamedDelayingQueue constructs a new named workqueue with delayed queuing ability
func NewNamedDelayingQueue(name string) DelayingInterface {
	return NewDelayingQueueWithCustomClock(clock.RealClock{}, name)
}

// NewDelayingQueueWithCustomClock constructs a new named workqueue
// with ability to inject real or fake clock for testing purposes
func NewDelayingQueueWithCustomClock(clock clock.Clock, name string) DelayingInterface {
	return newDelayingQueue(clock, NewNamed(name), name)
}

func newDelayingQueue(clock clock.Clock, q Interface, name string) *delayingType {
	ret := &delayingType{
		Interface:       q,
		clock:           clock,
		heartbeat:       clock.NewTicker(maxWait),
		stopCh:          make(chan struct{}),
		waitingForAddCh: make(chan *waitFor, 1000),
		metrics:         newRetryMetrics(name),
	}

	go ret.waitingLoop()
	return ret
}

// delayingType 包装了 Interface 通用接口，并提供了延迟重新入队列——延迟：是指加入队列的时间延迟
// delayingType wraps an Interface and provides delayed re-enquing
type delayingType struct {
	Interface  // 通用队列的接口

	// 时钟用于跟踪延迟触发的时间
	// clock tracks time for delayed firing
	clock clock.Clock

	// 关闭信号
	// stopCh lets us signal a shutdown to the waiting loop
	stopCh chan struct{}
	// 用来保证只发出一次关闭信号
	// stopOnce guarantees we only signal shutdown a single time
	stopOnce sync.Once

	// 在触发之前确保我们等待的时间不超过 maxWait
	// heartbeat ensures we wait no more than maxWait before firing
	heartbeat clock.Ticker

	// waitingForAddCh 是一个 buffered channel，提供了一个缓冲通道，延迟添加的元素封装成 waitFor 放到 channel 中
	// 比较重要的一个属性：当到了指定的时间后就将元素添加到通用队列中去进行处理，还没有到时间的话就放到这个缓冲通道中
	// waitingForAddCh is a buffered channel that feeds waitingForAdd
	waitingForAddCh chan *waitFor

	// 记录重试的次数
	// metrics counts the number of retries
	metrics retryMetrics
}

// waitFor 持有要添加的数据和应该添加的时间
// waitFor holds the data to add and the time it should be added
type waitFor struct {
	data    t  // 添加的元素数据
	readyAt time.Time  // 在什么时候添加到队列中
	// 优先级队列（heap）中的索引
	// index in the priority queue (heap)
	index int
}

// waitForPriorityQueue 为 waitFor 的元素集合实现了一个优先级队列
// 把需要延迟的元素放到一个队列中，然后在队列中按照元素的延时添加时间（readyAt）从小到大排序
// 其实这个优先级队列就是实现的 golang 中内置的 container/heap/heap.go 中的 Interface 接口
// 最终实现的队列就是 waitForPriorityQueue 这个集合是有序的，按照时间从小到大进行排列
// 总结：waitForPriorityQueue 实现了内置的 heap 的接口（小顶堆）

// waitForPriorityQueue implements a priority queue for waitFor items.
//
// waitForPriorityQueue implements heap.Interface. The item occurring next in
// time (i.e., the item with the smallest readyAt) is at the root (index 0).
// Peek returns this minimum item at index 0. Pop returns the minimum item after
// it has been removed from the queue and placed at index Len()-1 by
// container/heap. Push adds an item at index Len(), and container/heap
// percolates it into the correct location.

type waitForPriorityQueue []*waitFor

func (pq waitForPriorityQueue) Len() int {
	return len(pq)
}
// 判断索引 i 和 j 上的元素大小
func (pq waitForPriorityQueue) Less(i, j int) bool {
	// 根据时间先后顺序来决定先后顺序
	// i 位置的元素时间在 j 之前，则证明索引 i 的元素小于索引 j 的元素
	return pq[i].readyAt.Before(pq[j].readyAt)
}
// 交换索引 i 和 j 的元素
func (pq waitForPriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

// 添加元素到队列中
// 要注意不应该直接调用 Push 函数，而应该使用 `heap.Push`（因为 heap.Push 会调用 Push 函数，并且会将加入的节点调整到合适的位置）
// Push adds an item to the queue. Push should not be called directly; instead,
// use `heap.Push`.
func (pq *waitForPriorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*waitFor)
	item.index = n
	*pq = append(*pq, item)
}

// 从队列中弹出最后一个元素
// 要注意不应该直接调用 Pop 函数，而应该使用 `heap.Pop`
// Pop removes an item from the queue. Pop should not be called directly;
// instead, use `heap.Pop`.
func (pq *waitForPriorityQueue) Pop() interface{} {
	n := len(*pq)
	item := (*pq)[n-1]
	item.index = -1
	*pq = (*pq)[0:(n - 1)]
	return item
}

// 直接获取队列开头的元素，不会删除元素或改变队列
// Peek returns the item at the beginning of the queue, without removing the
// item or otherwise mutating the queue. It is safe to call directly.
func (pq waitForPriorityQueue) Peek() interface{} {
	return pq[0]
}

// ShutDown stops the queue. After the queue drains, the returned shutdown bool
// on Get() will be true. This method may be invoked more than once.
func (q *delayingType) ShutDown() {
	q.stopOnce.Do(func() {
		q.Interface.ShutDown()
		close(q.stopCh)
		q.heartbeat.Stop()
	})
}

// Q：谁调用 AddAfter？
// 在指定的延迟时间之后将元素 item 添加到队列中
// AddAfter adds the given item to the work queue after the given delay
func (q *delayingType) AddAfter(item interface{}, duration time.Duration) {
	// Q：函数 ShuttingDown 貌似并没有实现……
	// don't add if we're already shutting down
	if q.ShuttingDown() {
		return
	}

	q.metrics.retry()

	// 如果延迟的时间<=0，则相当于通用队列一样添加元素
	// immediately add things with no delay
	if duration <= 0 {
		q.Add(item)
		return
	}

	// select 没有 default case，所以可能会被阻塞
	select {
	// 如果调用了 ShutDown() 则解除阻塞
	case <-q.stopCh:
		// unblock if ShutDown() is called
		//把元素封装成 waitFor 传给 waitingForAddCh
		// 具体怎么添加的元素需要查看如何从这个通道消费数据的地方，也就是 waitingLoop 函数，这个函数在实例化 DelayingInterface 后就用一个单独的协程启动了
	//	Q：为什么要使用通道，这里其实也可以直接调用真正处理的函数
	// A：这样就可以并行处理了。
	case q.waitingForAddCh <- &waitFor{data: item, readyAt: q.clock.Now().Add(duration)}:
	}
}

// maxWait keeps a max bound on the wait time. It's just insurance against weird things happening.
// Checking the queue every 10 seconds isn't expensive and we know that we'll never end up with an
// expired item sitting for more than 10 seconds.
const maxWait = 10 * time.Second

// waitingLoop 一直运行直到工作队列关闭为止，并对要添加的元素列表进行检查
// waitingLoop runs until the workqueue is shutdown and keeps a check on the list of items to be added.
func (q *delayingType) waitingLoop() {
	defer utilruntime.HandleCrash()

	// 创建一个占位符通道，当列表中没有元素的时候利用这个变量实现长时间等待（单向通道）
	// Make a placeholder channel to use when there are no items in our list
	never := make(<-chan time.Time)

	// 构造一个定时器，当等待队列头部的元素准备好时，该定时器就会失效
	// Make a timer that expires when the item at the head of the waiting queue is ready
	var nextReadyAtTimer clock.Timer

	// 构造一个优先级队列
	waitingForQueue := &waitForPriorityQueue{}
	// 构造小顶堆结构
	heap.Init(waitingForQueue)

	// 用来避免元素重复添加，如果重复添加了就只更新时间——其实就是对堆中的元素进行唯一性标识
	waitingEntryByData := map[t]*waitFor{}

	for {
		// 队列如果关闭了，则直接退出
		if q.Interface.ShuttingDown() {
			return
		}

		// 获取当前时间
		now := q.clock.Now()

		// 取出堆中所有需要加入通用队列中的元素
		// Add ready entries
		for waitingForQueue.Len() > 0 {
			entry := waitingForQueue.Peek().(*waitFor)
			// 如果第一个元素指定的时间还没到时间，则跳出循环
			// 因为第一个元素是时间最小的
			if entry.readyAt.After(now) {
				break
			}

			// 时间已经过了，那就把它从优先队列中拿出来放入通用队列中
			// 同时要把元素从上面提到的 map 中删除，因为不用再判断重复添加了
			entry = heap.Pop(waitingForQueue).(*waitFor)
			q.Add(entry.data)
			delete(waitingEntryByData, entry.data)
		}

		// Set up a wait for the first item's readyAt (if one exists)
		nextReadyAt := never
		// 如果优先队列中还有元素，那就用第一个元素指定的时间减去当前时间作为等待时间
		// 因为优先队列是用时间排序的，后面的元素需要等待的时间更长，所以先处理排序靠前面的元素
		if waitingForQueue.Len() > 0 {
			if nextReadyAtTimer != nil {  // 取消原来的定时器
				nextReadyAtTimer.Stop()
			}
			entry := waitingForQueue.Peek().(*waitFor)
			nextReadyAtTimer = q.clock.NewTimer(entry.readyAt.Sub(now))
			nextReadyAt = nextReadyAtTimer.C()
		}

		select {
		case <-q.stopCh:
			return

		//	定时器，每过一段时间没有任何数据，那就再执行一次大循环
		case <-q.heartbeat.C():
			// continue the loop, which will add ready items

		//	// 上面的等待时间信号，时间到了就有信号
		//    // 激活这个case，然后继续循环，添加准备好了的元素
		case <-nextReadyAt:
			// continue the loop, which will add ready items

		//	// AddAfter 函数中放入到通道中的元素，这里从通道中获取数据
		case waitEntry := <-q.waitingForAddCh:
			// 如果时间已经过了就直接放入通用队列，没过就插入到有序队列
			if waitEntry.readyAt.After(q.clock.Now()) {
				insert(waitingForQueue, waitingEntryByData, waitEntry)  // 插入堆中
			} else {
				// 放入通用队列
				q.Add(waitEntry.data)
			}

			// 下面就是把channel里面的元素全部取出来
			// 如果没有数据了就直接退出
			drained := false
			for !drained {
				select {
				case waitEntry := <-q.waitingForAddCh:
					if waitEntry.readyAt.After(q.clock.Now()) {
						insert(waitingForQueue, waitingEntryByData, waitEntry)
					} else {
						q.Add(waitEntry.data)
					}
				default:
					drained = true
				}
			}
		}
	}
}

// 插入元素到有序队列，如果已经存在了则更新时间
// insert adds the entry to the priority queue, or updates the readyAt if it already exists in the queue
func insert(q *waitForPriorityQueue, knownEntries map[t]*waitFor, entry *waitFor) {
	// 查看元素是否已经存在
	// if the entry already exists, update the time only if it would cause the item to be queued sooner
	existing, exists := knownEntries[entry.data]
	if exists {
		// 元素存在，就比较谁的时间靠后就用谁的时间
		if existing.readyAt.After(entry.readyAt) {
			existing.readyAt = entry.readyAt
			// 时间变了需要重新调整优先级队列
			heap.Fix(q, existing.index)
		}

		return
	}

	// 把元素放入有序队列中
	heap.Push(q, entry)
	// 并记录在上面的 map 里面，用于判断是否存在
	knownEntries[entry.data] = entry
}
