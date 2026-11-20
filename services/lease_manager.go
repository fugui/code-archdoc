package services

import "sync"

// ActiveLeaseManager 维护运行中任务的 repo_id 与 snapshot_id 内存活跃租约计数，
// 确保 ResourceReaper 绝对不会回收正在执行 Git 同步、L1 抽取、L2 生成或源码预览的目录与分片（F-27）。
type ActiveLeaseManager struct {
	mu             sync.RWMutex
	repoLeases     map[uint]int
	snapshotLeases map[uint]int
}

// NewActiveLeaseManager 构造活跃租约管理器。
func NewActiveLeaseManager() *ActiveLeaseManager {
	return &ActiveLeaseManager{
		repoLeases:     make(map[uint]int),
		snapshotLeases: make(map[uint]int),
	}
}

// AcquireRepo 为指定 repoID 增加 1 个活跃租约；返回的释放函数保证幂等（多次调用仅递减 1 次）。
func (m *ActiveLeaseManager) AcquireRepo(repoID uint) func() {
	if m == nil || repoID == 0 {
		return func() {}
	}
	m.mu.Lock()
	m.repoLeases[repoID]++
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if cur := m.repoLeases[repoID]; cur <= 1 {
				delete(m.repoLeases, repoID)
			} else {
				m.repoLeases[repoID] = cur - 1
			}
			m.mu.Unlock()
		})
	}
}

// AcquireSnapshot 为指定 snapshotID 增加 1 个活跃租约；返回的释放函数保证幂等。
func (m *ActiveLeaseManager) AcquireSnapshot(snapshotID uint) func() {
	if m == nil || snapshotID == 0 {
		return func() {}
	}
	m.mu.Lock()
	m.snapshotLeases[snapshotID]++
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if cur := m.snapshotLeases[snapshotID]; cur <= 1 {
				delete(m.snapshotLeases, snapshotID)
			} else {
				m.snapshotLeases[snapshotID] = cur - 1
			}
			m.mu.Unlock()
		})
	}
}

// IsRepoActive 检查仓库沙箱目录当前是否被活跃任务持有租约。
func (m *ActiveLeaseManager) IsRepoActive(repoID uint) bool {
	if m == nil || repoID == 0 {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.repoLeases[repoID] > 0
}

// IsSnapshotActive 检查快照 Code IR 分片当前是否被活跃任务持有租约。
func (m *ActiveLeaseManager) IsSnapshotActive(snapshotID uint) bool {
	if m == nil || snapshotID == 0 {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotLeases[snapshotID] > 0
}

// ActiveRepoCount 返回当前持有活跃租约的仓库数。
func (m *ActiveLeaseManager) ActiveRepoCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.repoLeases)
}

// ActiveSnapshotCount 返回当前持有活跃租约的快照数。
func (m *ActiveLeaseManager) ActiveSnapshotCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.snapshotLeases)
}

// ActiveSnapshotIDs 返回全部当前持有活跃租约的快照 ID 列表（供 SQL NOT IN 过滤）。
func (m *ActiveLeaseManager) ActiveSnapshotIDs() []uint {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.snapshotLeases) == 0 {
		return nil
	}
	out := make([]uint, 0, len(m.snapshotLeases))
	for id := range m.snapshotLeases {
		out = append(out, id)
	}
	return out
}
