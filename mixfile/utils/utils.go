package utils

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
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
	return &SortedTask{
		taskMap:   make(map[int]func() error),
		semaphore: make(chan struct{}, max(limit, 1)),
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

// noRetryError 包裹一个不应重试的错误（对标 Kotlin 的 NoRetryException）。
// 例如「分片过大」：重试也只会一直大，没有意义。
type noRetryError struct{ err error }

func (e *noRetryError) Error() string { return e.err.Error() }
func (e *noRetryError) Unwrap() error { return e.err }

// NoRetry 标记一个错误为不可重试。Retry 遇到被它包裹的错误会立即返回，不再重试。
func NoRetry(err error) error {
	if err == nil {
		return nil
	}
	return &noRetryError{err: err}
}

// IsNoRetry 判断错误链上是否存在 NoRetry 标记。
func IsNoRetry(err error) bool {
	var e *noRetryError
	return errors.As(err, &e)
}

// Retry 重试 block 最多 times 次，每次失败后等待 delay（对标 Kotlin 的 retry）。
// 被 NoRetry 标记的错误立即返回，不重试。times <= 1 时只执行一次。
// 与 Kotlin 不同：Go 无法通过异常区分「取消」，调用方如需取消应在 block 内自行响应 ctx。
func Retry[T any](times int, delay time.Duration, block func() (T, error)) (T, error) {
	var result T
	var err error
	times = max(times, 1)
	for i := 0; i < times; i++ {
		result, err = block()
		if err == nil {
			return result, nil
		}
		if IsNoRetry(err) {
			return result, err
		}
		// 最后一次失败不再等待
		if i < times-1 && delay > 0 {
			time.Sleep(delay)
		}
	}
	return result, err
}
