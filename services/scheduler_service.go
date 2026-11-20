package services

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"code-archdoc/models"
)

// ErrQueueFull 表示交互或批量调度队列已满（HTTP 429 QUEUE_FULL）。
var ErrQueueFull = errors.New("调度队列已满，请稍后重试")

// ErrSchedulerStopped 表示调度器正在停机，不再接收新任务。
var ErrSchedulerStopped = errors.New("任务调度器正在关闭，暂不接收新任务")

// SchedulerConfig 定义双优先级队列与多维并发限流参数（F-27）。
type SchedulerConfig struct {
	MaxConcurrency      int // 全局并发执行槽位与 LLM 并发信号量上限，默认 8
	MaxUserConcurrency  int // 单用户最大并发任务数，默认 2
	InteractiveQueueCap int // HighQ 高优交互队列容量，默认 64
	BatchQueueCap       int // LowQ 低优批量队列容量，默认 256
	HighBurstRatio      int // 防饥饿加权比 High:Low = N:1，默认 4
}

// SchedulerStatsVO 对应 GET /api/admin/storage/stats 中的 scheduler 观测块。
type SchedulerStatsVO struct {
	InteractiveQueueLen int `json:"interactive_queue_len"`
	BatchQueueLen       int `json:"batch_queue_len"`
	RunningLLMCalls     int `json:"running_llm_calls"`
	MaxConcurrency      int `json:"max_concurrency"`
}

// ScheduledJob 表示一个待调度的异步任务单元。
type ScheduledJob struct {
	ID       string
	UserID   uint
	Priority string // models.TaskPriorityInteractive | models.TaskPriorityBatch
	Ctx      context.Context
	Run      func(ctx context.Context)
}

// TaskScheduler 实现 High(Interactive) / Low(Batch) 4:1 防饥饿调度、单用户并发限流与全局 LLM 信号量（F-27）。
type TaskScheduler struct {
	cfg SchedulerConfig

	mu              sync.Mutex
	highQ           []ScheduledJob
	lowQ            []ScheduledJob
	runningTotal    int
	runningByUser   map[uint]int
	consecutiveHigh int
	started         bool
	stopped         bool

	notifyCh chan struct{}
	stopCtx  context.Context
	stopFunc context.CancelFunc
	loopWG   sync.WaitGroup
	workerWG sync.WaitGroup

	llmSem          chan struct{}
	runningLLMCalls atomic.Int64
}

// NewTaskScheduler 构造双优先级防饥饿任务调度器。
func NewTaskScheduler(cfg SchedulerConfig) *TaskScheduler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 8
	}
	if cfg.MaxUserConcurrency <= 0 {
		cfg.MaxUserConcurrency = 2
	}
	if cfg.InteractiveQueueCap <= 0 {
		cfg.InteractiveQueueCap = 64
	}
	if cfg.BatchQueueCap <= 0 {
		cfg.BatchQueueCap = 256
	}
	if cfg.HighBurstRatio <= 0 {
		cfg.HighBurstRatio = 4
	}
	return &TaskScheduler{
		cfg:           cfg,
		highQ:         make([]ScheduledJob, 0, minInt(cfg.InteractiveQueueCap, 16)),
		lowQ:          make([]ScheduledJob, 0, minInt(cfg.BatchQueueCap, 32)),
		runningByUser: make(map[uint]int),
		notifyCh:      make(chan struct{}, 1),
		llmSem:        make(chan struct{}, cfg.MaxConcurrency),
	}
}

// Start 启动后台调度分发循环（支持绑定父级 ctx，用于优雅停机 AC-M6-03）。
func (s *TaskScheduler) Start(parentCtx context.Context) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.stopped = false
	s.stopCtx, s.stopFunc = context.WithCancel(parentCtx)
	s.mu.Unlock()

	s.loopWG.Add(1)
	go s.dispatchLoop()
}

// Stop 触发优雅停机：拒绝新入队，唤醒并退出分发协程，等待所有运行中 worker 干净结束。
func (s *TaskScheduler) Stop() {
	s.mu.Lock()
	if !s.started {
		s.stopped = true
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.stopFunc
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.signal()
	s.loopWG.Wait()
	s.workerWG.Wait()
}

// Enqueue 将任务按优先级提交至 HighQ 或 LowQ；队列满时返回 ErrQueueFull（429），否则排队等待执行不失败。
func (s *TaskScheduler) Enqueue(job ScheduledJob) error {
	if job.Run == nil {
		return nil
	}
	if job.Priority != models.TaskPriorityBatch {
		job.Priority = models.TaskPriorityInteractive
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrSchedulerStopped
	}
	if !s.started {
		// 若未显式调用 Start，自动以 Background 启动确保提交即运行
		s.started = true
		s.stopCtx, s.stopFunc = context.WithCancel(context.Background())
		s.loopWG.Add(1)
		go s.dispatchLoop()
	}

	if job.Priority == models.TaskPriorityInteractive {
		if len(s.highQ) >= s.cfg.InteractiveQueueCap {
			s.mu.Unlock()
			return ErrQueueFull
		}
		s.highQ = append(s.highQ, job)
	} else {
		if len(s.lowQ) >= s.cfg.BatchQueueCap {
			s.mu.Unlock()
			return ErrQueueFull
		}
		s.lowQ = append(s.lowQ, job)
	}
	s.mu.Unlock()

	s.signal()
	return nil
}

// AcquireLLMSlot 获取全局 LLM 并发信号量槽位，支持 ctx 取消与实时运行数统计。
func (s *TaskScheduler) AcquireLLMSlot(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case s.llmSem <- struct{}{}:
	}
	if ctx.Err() != nil {
		<-s.llmSem
		return nil, ctx.Err()
	}
	s.runningLLMCalls.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			s.runningLLMCalls.Add(-1)
			<-s.llmSem
		})
	}, nil
}

// Stats 返回当前双队列长度与 LLM 并发占用统计（供 /api/admin/storage/stats 使用）。
func (s *TaskScheduler) Stats() SchedulerStatsVO {
	s.mu.Lock()
	highLen := len(s.highQ)
	lowLen := len(s.lowQ)
	s.mu.Unlock()

	return SchedulerStatsVO{
		InteractiveQueueLen: highLen,
		BatchQueueLen:       lowLen,
		RunningLLMCalls:     int(s.runningLLMCalls.Load()),
		MaxConcurrency:      s.cfg.MaxConcurrency,
	}
}

func (s *TaskScheduler) signal() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func (s *TaskScheduler) dispatchLoop() {
	defer s.loopWG.Done()

	for {
		for {
			job, ok := s.popNextRunnable()
			if !ok {
				break
			}
			s.launchWorker(job)
		}

		select {
		case <-s.stopCtx.Done():
			return
		case <-s.notifyCh:
		}
	}
}

// popNextRunnable 在全局并发、单用户限流与 4:1 防饥饿约束下选出下一个可执行任务。
func (s *TaskScheduler) popNextRunnable() (ScheduledJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped || s.stopCtx.Err() != nil {
		return ScheduledJob{}, false
	}
	if s.runningTotal >= s.cfg.MaxConcurrency {
		return ScheduledJob{}, false
	}

	canRunUser := func(uid uint) bool {
		if uid == 0 || s.cfg.MaxUserConcurrency <= 0 {
			return true
		}
		return s.runningByUser[uid] < s.cfg.MaxUserConcurrency
	}

	popFrom := func(q *[]ScheduledJob) (ScheduledJob, bool) {
		slice := *q
		for i := range slice {
			if canRunUser(slice[i].UserID) {
				picked := slice[i]
				*q = append(slice[:i], slice[i+1:]...)
				return picked, true
			}
		}
		return ScheduledJob{}, false
	}

	var (
		job ScheduledJob
		ok  bool
	)

	// 4:1 防饥饿策略：连续派发 >= HighBurstRatio 个高优任务且低优队列存在可执行任务时，让渡 1 槽位给低优队列
	if s.consecutiveHigh >= s.cfg.HighBurstRatio && len(s.lowQ) > 0 {
		if job, ok = popFrom(&s.lowQ); ok {
			s.consecutiveHigh = 0
		}
	}

	if !ok && len(s.highQ) > 0 {
		if job, ok = popFrom(&s.highQ); ok {
			s.consecutiveHigh++
		}
	}

	if !ok && len(s.lowQ) > 0 {
		if job, ok = popFrom(&s.lowQ); ok {
			s.consecutiveHigh = 0
		}
	}

	if !ok {
		return ScheduledJob{}, false
	}

	s.runningTotal++
	if job.UserID > 0 {
		s.runningByUser[job.UserID]++
	}
	return job, true
}

func (s *TaskScheduler) launchWorker(job ScheduledJob) {
	s.workerWG.Add(1)
	go func() {
		defer s.workerWG.Done()
		defer func() {
			s.mu.Lock()
			s.runningTotal--
			if job.UserID > 0 {
				if cur := s.runningByUser[job.UserID]; cur <= 1 {
					delete(s.runningByUser, job.UserID)
				} else {
					s.runningByUser[job.UserID] = cur - 1
				}
			}
			s.mu.Unlock()
			s.signal()
		}()

		runCtx := job.Ctx
		if runCtx == nil {
			runCtx = s.stopCtx
		}
		job.Run(runCtx)
	}()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
