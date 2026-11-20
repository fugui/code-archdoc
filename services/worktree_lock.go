package services

import "sync"

// DowngradableRWMutex 支持原子写锁降级为读锁的读写互斥锁（ADR-04 D-6 / 消除 P-5 竞态窗口）：
//  1. Lock() 获取独占写锁（用于 gitSyncer.Sync 更新沙箱工作树），且阻塞后续新读锁防止写者饥饿；
//  2. DowngradeToRLock() 在不释放内部互斥锁的情况下，原子将写锁转换为 1 个读锁并唤醒等待读锁的协程，
//     消除 Unlock() -> RLock() 间隙内被其他写者抢占改写工作树的风险；
//  3. RLock() / RUnlock() 供 L1 抽取、只读源码预览、Drift 零签出 Diff 并发共享工作树。
type DowngradableRWMutex struct {
	mu             sync.Mutex
	cond           *sync.Cond
	readers        int
	writerActive   bool
	waitingWriters int
}

// NewDowngradableRWMutex 构造可原子降级的读写锁。
func NewDowngradableRWMutex() *DowngradableRWMutex {
	m := &DowngradableRWMutex{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// RLock 获取共享读锁；若有活跃或等待中的写者则排队等待，避免写者饥饿。
func (m *DowngradableRWMutex) RLock() {
	m.mu.Lock()
	for m.writerActive || m.waitingWriters > 0 {
		m.cond.Wait()
	}
	m.readers++
	m.mu.Unlock()
}

// RUnlock 释放共享读锁；最后一个读者退出时唤醒等待中的写者。
func (m *DowngradableRWMutex) RUnlock() {
	m.mu.Lock()
	if m.readers <= 0 {
		m.mu.Unlock()
		panic("DowngradableRWMutex: RUnlock of unlocked reader")
	}
	m.readers--
	if m.readers == 0 {
		m.cond.Broadcast()
	}
	m.mu.Unlock()
}

// Lock 获取独占写锁；等待所有在途读者与其他写者退出。
func (m *DowngradableRWMutex) Lock() {
	m.mu.Lock()
	m.waitingWriters++
	for m.writerActive || m.readers > 0 {
		m.cond.Wait()
	}
	m.waitingWriters--
	m.writerActive = true
	m.mu.Unlock()
}

// Unlock 释放独占写锁并唤醒所有等待者。
func (m *DowngradableRWMutex) Unlock() {
	m.mu.Lock()
	if !m.writerActive {
		m.mu.Unlock()
		panic("DowngradableRWMutex: Unlock of unlocked writer")
	}
	m.writerActive = false
	m.cond.Broadcast()
	m.mu.Unlock()
}

// DowngradeToRLock 将当前持有的独占写锁原子降级为共享读锁（零间隙窗口），并唤醒等待的读者。
func (m *DowngradableRWMutex) DowngradeToRLock() {
	m.mu.Lock()
	if !m.writerActive {
		m.mu.Unlock()
		panic("DowngradableRWMutex: DowngradeToRLock without active writer")
	}
	m.writerActive = false
	m.readers = 1
	m.cond.Broadcast()
	m.mu.Unlock()
}

// RepoWorktreeLocker 按 repoID 维护每仓独立的可降级读写锁，统一收口在 SnapshotService 内。
type RepoWorktreeLocker struct {
	mu    sync.Mutex
	locks map[uint]*DowngradableRWMutex
}

// NewRepoWorktreeLocker 构造按仓库粒度隔离的工作树读写锁管理器。
func NewRepoWorktreeLocker() *RepoWorktreeLocker {
	return &RepoWorktreeLocker{
		locks: make(map[uint]*DowngradableRWMutex),
	}
}

// Get 返回指定仓库的锁实例（懒初始化、并发安全）。
func (l *RepoWorktreeLocker) Get(repoID uint) *DowngradableRWMutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	rw, ok := l.locks[repoID]
	if !ok {
		rw = NewDowngradableRWMutex()
		l.locks[repoID] = rw
	}
	return rw
}
