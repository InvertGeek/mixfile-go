package utils

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

// SortedTask 对标 Kotlin 的 SortedTask：用占位符按序预占，execute 时从最小序号
// 连续冲刷已就绪的任务，遇占位符即停。槽位从 PrepareTask 持有到任务被写出才归还，
// 天然把在途分片数限制在 limit 个。
//
// 与 Kotlin 的唯一区别：Kotlin 靠结构化并发在分片下载异常时自动取消全部协程，
// Go 没有此机制，故保留 done/Abort 用于在下载失败时唤醒阻塞的提交循环，避免死锁。
type SortedTask struct {
	mu        sync.Mutex
	taskMap   map[int]func() error // nil 值表示占位符（对应 Kotlin 的 placeholder）
	next      int
	semaphore chan struct{}
	done      chan struct{}
	once      sync.Once
}

func NewSortedTask(limit int) *SortedTask {
	if limit < 1 {
		limit = 1
	}
	return &SortedTask{
		taskMap:   make(map[int]func() error),
		semaphore: make(chan struct{}, limit),
		done:      make(chan struct{}),
	}
}

// PrepareTask 获取并发槽位并按序占位（对应 Kotlin 的 prepareTask）。
// 必须在提交循环里顺序调用，以保证 taskMap 的 key 有序。
// 序列已中止时返回 false，使提交循环及时退出，避免死锁。
func (st *SortedTask) PrepareTask(order int) bool {
	// 优先检查中止：select 多路就绪时是随机的，单独提前判断可避免中止后又误占一个槽位
	select {
	case <-st.done:
		return false
	default:
	}
	select {
	case st.semaphore <- struct{}{}:
	case <-st.done:
		return false
	}
	st.mu.Lock()
	st.taskMap[order] = nil // 占位符
	st.mu.Unlock()
	return true
}

// AddTask 用真实任务替换占位符（对应 Kotlin 的 addTask）。
func (st *SortedTask) AddTask(order int, task func() error) {
	st.mu.Lock()
	st.taskMap[order] = task
	st.mu.Unlock()
}

// Execute 在锁内从最小序号开始连续执行已就绪的任务，遇到占位符或缺口即停止
// （对应 Kotlin 的 execute）。每执行一个任务归还一个槽位。
func (st *SortedTask) Execute() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	for {
		task, ok := st.taskMap[st.next]
		if !ok || task == nil {
			// 不存在或仍是占位符：更靠前的分片尚未下载完，停止
			return nil
		}
		delete(st.taskMap, st.next)
		st.next++
		err := task()
		<-st.semaphore // 归还槽位（对应 Kotlin finally 中的 release）
		if err != nil {
			return err
		}
	}
}

// Abort 中止序列，唤醒阻塞在 PrepareTask 的提交循环。
// 下载失败时需显式调用（Kotlin 靠结构化并发自动取消，Go 没有）。
func (st *SortedTask) Abort() {
	st.once.Do(func() { close(st.done) })
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
