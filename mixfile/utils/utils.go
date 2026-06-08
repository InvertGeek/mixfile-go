package utils

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

type SortedTask struct {
	limit     int
	semaphore chan struct{}
	taskMap   sync.Map
	orders    []int
	mu        sync.Mutex
	cond      *sync.Cond
	nextOrder int
	aborted   bool
	done      chan struct{}
	once      sync.Once
}

func NewSortedTask(limit int) *SortedTask {
	st := &SortedTask{
		limit:     limit,
		semaphore: make(chan struct{}, limit),
		nextOrder: 0,
		done:      make(chan struct{}),
	}
	st.cond = sync.NewCond(&st.mu)
	return st
}

// Acquire 获取一个并发槽位。若任务序列已中止则立即返回 false，避免主循环阻塞。
// 优先检查 done：序列已中止时绝不再占用槽位（select 多路就绪时是随机的，
// 单独提前判断可避免中止后又误提交一个任务）。
func (st *SortedTask) Acquire() bool {
	select {
	case <-st.done:
		return false
	default:
	}
	select {
	case st.semaphore <- struct{}{}:
		return true
	case <-st.done:
		return false
	}
}

func (st *SortedTask) Release() {
	<-st.semaphore
}

// Abort 标记任务序列已中止，唤醒所有正在等待序号的协程以及阻塞在 Acquire 的协程。
// 流式顺序写出场景下，任意分片失败都应整体中断，避免写出损坏的文件。
func (st *SortedTask) Abort() {
	st.mu.Lock()
	st.aborted = true
	st.cond.Broadcast()
	st.mu.Unlock()
	st.once.Do(func() { close(st.done) })
}

// AddAndExecute 将任务放入 Map，如果轮到该序号则执行并递增序号。
// 不负责并发槽位的归还：槽位由调用方在 goroutine 中以 defer Release 统一管理，
// 保证无论成功、写出失败还是序列中止，每个 Acquire 都恰好对应一次 Release。
func (st *SortedTask) AddAndExecute(order int, task func() error) error {
	st.taskMap.Store(order, task)

	st.mu.Lock()
	defer st.mu.Unlock()

	for {
		if st.aborted {
			return fmt.Errorf("task sequence aborted")
		}

		val, ok := st.taskMap.Load(st.nextOrder)
		if !ok {
			// 还没轮到，或者还没准备好
			st.cond.Wait()
			continue
		}

		// 执行任务
		f := val.(func() error)
		if err := f(); err != nil {
			// 写出失败，整体中止序列，唤醒其余等待者
			st.aborted = true
			st.cond.Broadcast()
			st.once.Do(func() { close(st.done) })
			return err
		}

		st.taskMap.Delete(st.nextOrder)
		st.nextOrder++
		st.cond.Broadcast() // 通知其他在等待序号的协程
		break
	}
	return nil
}

// SubstringAfter 返回 delimiter 第一次出现之后的子串
func SubstringAfter(s, delimiter string) string {
	pos := strings.Index(s, delimiter)
	if pos == -1 {
		return s
	}
	return s[pos+len(delimiter):]
}

func FindAvailablePort(startPort int) (int, error) {
	for port := startPort; port <= 65535; port++ {
		addr := fmt.Sprintf(":%d", port)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			err := listener.Close()
			if err != nil {
				return 0, err
			} // 关闭监听器，释放端口
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found")
}
