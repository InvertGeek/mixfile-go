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
}

func NewSortedTask(limit int) *SortedTask {
	st := &SortedTask{
		limit:     limit,
		semaphore: make(chan struct{}, limit),
		nextOrder: 0,
	}
	st.cond = sync.NewCond(&st.mu)
	return st
}

func (st *SortedTask) Acquire() {
	st.semaphore <- struct{}{}
}

func (st *SortedTask) Release() {
	<-st.semaphore
}

// AddAndExecute 将任务放入 Map，如果轮到该序号则执行并递增序号
func (st *SortedTask) AddAndExecute(order int, task func() error) error {
	st.taskMap.Store(order, task)

	st.mu.Lock()
	defer st.mu.Unlock()

	for {
		val, ok := st.taskMap.Load(st.nextOrder)
		if !ok {
			// 还没轮到，或者还没准备好
			st.cond.Wait()
			continue
		}

		// 执行任务
		f := val.(func() error)
		if err := f(); err != nil {
			return err
		}

		st.taskMap.Delete(st.nextOrder)
		st.nextOrder++
		st.Release()
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
