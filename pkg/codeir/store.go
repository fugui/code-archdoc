package codeir

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"code-archdoc/pkg/sandbox"

	"github.com/google/uuid"
)

const (
	// DefaultFacetCacheMaxBytes 进程内分片热缓存默认上限（128 MB）
	DefaultFacetCacheMaxBytes int64 = 128 * 1024 * 1024
	maxDecompressedFacetBytes int64 = 64 * 1024 * 1024
)

var (
	// ErrFacetNotFound 分片文件不存在
	ErrFacetNotFound = errors.New("code ir facet not found")
	// ErrChecksumMismatch 分片内容校验和不匹配
	ErrChecksumMismatch = errors.New("code ir facet checksum mismatch")
)

// SnapshotLoc 标识快照在 FacetStore 中的物理定位键
type SnapshotLoc struct {
	RepoID    uint
	Subdir    string
	CommitSHA string
}

// FacetWriteMeta 描述一次分片原子落盘后的元数据
type FacetWriteMeta struct {
	FacetName       string
	StoragePath     string
	ContentSHA256   string
	SizeBytes       int64
	CompressedBytes int64
}

// FacetStoreStats 内存 LRU 缓存命中统计（供单测与可观测性使用）
type FacetStoreStats struct {
	CacheHits   int64
	CacheMisses int64
	CachedBytes int64
	CachedItems int
}

// FacetStore 定义 Code IR 分片存储抽象接口（P0 LocalFSFacetStore，P1 可切 S3FacetStore）
type FacetStore interface {
	WriteFacet(ctx context.Context, loc SnapshotLoc, facetName string, payload any) (*FacetWriteMeta, error)
	ReadFacet(ctx context.Context, loc SnapshotLoc, facetName string, expectedSHA256 string, dest any) error
	ReadRawFacet(ctx context.Context, loc SnapshotLoc, facetName string, expectedSHA256 string) ([]byte, string, error)
	DeleteFacet(ctx context.Context, loc SnapshotLoc, facetName string) error
	StatFacet(ctx context.Context, loc SnapshotLoc, facetName string) (size int64, exists bool, err error)
	WriteGlossary(ctx context.Context, loc SnapshotLoc, glossary *GlossaryCache) (string, error)
	ReadGlossary(ctx context.Context, loc SnapshotLoc) (*GlossaryCache, error)
	Stats() FacetStoreStats
}

type lruEntry struct {
	key        string
	rawJSON    []byte
	sha256Hex  string
	byteWeight int64
}

// LocalFSFacetStore 实现本地 gzip 压缩对象文件 + 原子写盘 + 128MB 内存 LRU 缓存。
type LocalFSFacetStore struct {
	baseDir  string
	maxBytes int64

	mu       sync.Mutex
	curBytes int64
	lruList  *list.List
	lruMap   map[string]*list.Element
	hits     int64
	misses   int64
}

// NewLocalFSFacetStore 构建本地分片存储引擎（maxCacheBytes <= 0 时采用默认 128 MB）。
func NewLocalFSFacetStore(baseDir string, maxCacheBytes int64) (*LocalFSFacetStore, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, fmt.Errorf("facet store baseDir 不能为空")
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("解析 facet store 目录失败: %w", err)
	}
	if err := os.MkdirAll(absBase, 0750); err != nil {
		return nil, fmt.Errorf("创建 facet store 根目录失败: %w", err)
	}
	if maxCacheBytes <= 0 {
		maxCacheBytes = DefaultFacetCacheMaxBytes
	}
	return &LocalFSFacetStore{
		baseDir:  absBase,
		maxBytes: maxCacheBytes,
		lruList:  list.New(),
		lruMap:   make(map[string]*list.Element),
	}, nil
}

// FacetPath 返回受控的相对与物理路径，经 SafeJoin 校验防目录穿越。
func (s *LocalFSFacetStore) facetPaths(loc SnapshotLoc, filename string) (relPath string, absPath string, err error) {
	if loc.CommitSHA == "" || strings.ContainsAny(loc.CommitSHA, "/\\.") {
		return "", "", fmt.Errorf("非法的 commit_sha: %q", loc.CommitSHA)
	}
	normSubdir, normErr := sandbox.NormalizeSubdir(loc.Subdir)
	if normErr != nil {
		return "", "", normErr
	}
	subdirBucket := "root"
	if normSubdir != "" {
		sum := sha256.Sum256([]byte(normSubdir))
		subdirBucket = hex.EncodeToString(sum[:])[:12]
	}
	if strings.ContainsAny(filename, "/\\") || strings.Contains(filename, "..") {
		return "", "", fmt.Errorf("非法的 facet 文件名: %q", filename)
	}

	rel := filepath.ToSlash(filepath.Join(
		fmt.Sprintf("repo_%d", loc.RepoID),
		subdirBucket,
		loc.CommitSHA,
		filename,
	))
	abs, safeErr := sandbox.SafeJoin(s.baseDir, rel)
	if safeErr != nil {
		return "", "", safeErr
	}
	return rel, abs, nil
}

// WriteFacet 将分片对象序列化为确定性 JSON，经 gzip 压缩写入临时文件 + fsync + 原子 rename，并刷新 LRU。
func (s *LocalFSFacetStore) WriteFacet(ctx context.Context, loc SnapshotLoc, facetName string, payload any) (*FacetWriteMeta, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	filename := facetName + ".json.gz"
	relPath, finalPath, err := s.facetPaths(loc, filename)
	if err != nil {
		return nil, err
	}

	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化 facet %s 失败: %w", facetName, err)
	}
	sum := sha256.Sum256(rawJSON)
	shaHex := hex.EncodeToString(sum[:])

	var gzBuf bytes.Buffer
	gzw := gzip.NewWriter(&gzBuf)
	if _, err := gzw.Write(rawJSON); err != nil {
		_ = gzw.Close()
		return nil, fmt.Errorf("压缩 facet %s 失败: %w", facetName, err)
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("完成压缩 facet %s 失败: %w", facetName, err)
	}
	compressed := gzBuf.Bytes()

	if err := atomicWriteFile(finalPath, compressed, ".tmp-"+facetName+"-"); err != nil {
		return nil, fmt.Errorf("原子落盘 facet %s 失败: %w", facetName, err)
	}

	s.putCache(relPath, rawJSON, shaHex)

	return &FacetWriteMeta{
		FacetName:       facetName,
		StoragePath:     relPath,
		ContentSHA256:   shaHex,
		SizeBytes:       int64(len(rawJSON)),
		CompressedBytes: int64(len(compressed)),
	}, nil
}

// ReadRawFacet 按分片名增量读取解压后的 JSON 字节流与内容校验和（优先命中内存 LRU）。
func (s *LocalFSFacetStore) ReadRawFacet(ctx context.Context, loc SnapshotLoc, facetName string, expectedSHA256 string) ([]byte, string, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	filename := facetName + ".json.gz"
	relPath, finalPath, err := s.facetPaths(loc, filename)
	if err != nil {
		return nil, "", err
	}

	if cached, cachedSHA, ok := s.getCache(relPath); ok {
		if expectedSHA256 != "" && cachedSHA != expectedSHA256 {
			s.evictKey(relPath)
			return nil, "", ErrChecksumMismatch
		}
		return cached, cachedSHA, nil
	}

	f, err := os.Open(finalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", ErrFacetNotFound
		}
		return nil, "", err
	}
	defer func() { _ = f.Close() }()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", fmt.Errorf("解压 facet %s 失败: %w", facetName, err)
	}
	defer func() { _ = gzr.Close() }()

	rawJSON, err := io.ReadAll(io.LimitReader(gzr, maxDecompressedFacetBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("读取 facet %s 内容失败: %w", facetName, err)
	}
	if int64(len(rawJSON)) > maxDecompressedFacetBytes {
		return nil, "", fmt.Errorf("facet %s 解压体积超过安全上限", facetName)
	}

	sum := sha256.Sum256(rawJSON)
	shaHex := hex.EncodeToString(sum[:])
	if expectedSHA256 != "" && shaHex != expectedSHA256 {
		return nil, "", ErrChecksumMismatch
	}

	s.putCache(relPath, rawJSON, shaHex)
	cp := make([]byte, len(rawJSON))
	copy(cp, rawJSON)
	return cp, shaHex, nil
}

// ReadFacet 读取并反序列化指定分片。
func (s *LocalFSFacetStore) ReadFacet(ctx context.Context, loc SnapshotLoc, facetName string, expectedSHA256 string, dest any) error {
	raw, _, err := s.ReadRawFacet(ctx, loc, facetName, expectedSHA256)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("反序列化 facet %s 失败: %w", facetName, err)
	}
	return nil
}

// DeleteFacet 删除磁盘上的分片文件并清理 LRU 缓存。
func (s *LocalFSFacetStore) DeleteFacet(ctx context.Context, loc SnapshotLoc, facetName string) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	relPath, finalPath, err := s.facetPaths(loc, facetName+".json.gz")
	if err != nil {
		return err
	}
	s.evictKey(relPath)
	if rmErr := os.Remove(finalPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return rmErr
	}
	return nil
}

// StatFacet 查询磁盘上的压缩分片大小与存在状态。
func (s *LocalFSFacetStore) StatFacet(ctx context.Context, loc SnapshotLoc, facetName string) (int64, bool, error) {
	if ctx != nil && ctx.Err() != nil {
		return 0, false, ctx.Err()
	}
	_, finalPath, err := s.facetPaths(loc, facetName+".json.gz")
	if err != nil {
		return 0, false, err
	}
	st, err := os.Stat(finalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return st.Size(), true, nil
}

// WriteGlossary 原子落盘快照级共享术语表（glossary.json.gz）。
func (s *LocalFSFacetStore) WriteGlossary(ctx context.Context, loc SnapshotLoc, glossary *GlossaryCache) (string, error) {
	meta, err := s.WriteFacet(ctx, loc, "glossary", glossary)
	if err != nil {
		return "", err
	}
	return meta.StoragePath, nil
}

// ReadGlossary 读取快照级共享术语表缓存。
func (s *LocalFSFacetStore) ReadGlossary(ctx context.Context, loc SnapshotLoc) (*GlossaryCache, error) {
	var g GlossaryCache
	if err := s.ReadFacet(ctx, loc, "glossary", "", &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// Stats 返回内存 LRU 统计信息。
func (s *LocalFSFacetStore) Stats() FacetStoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return FacetStoreStats{
		CacheHits:   s.hits,
		CacheMisses: s.misses,
		CachedBytes: s.curBytes,
		CachedItems: len(s.lruMap),
	}
}

func (s *LocalFSFacetStore) getCache(key string) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if elem, ok := s.lruMap[key]; ok {
		s.lruList.MoveToFront(elem)
		s.hits++
		ent := elem.Value.(*lruEntry)
		cp := make([]byte, len(ent.rawJSON))
		copy(cp, ent.rawJSON)
		return cp, ent.sha256Hex, true
	}
	s.misses++
	return nil, "", false
}

func (s *LocalFSFacetStore) putCache(key string, rawJSON []byte, shaHex string) {
	weight := int64(len(rawJSON))
	if weight > s.maxBytes {
		return
	}
	cp := make([]byte, len(rawJSON))
	copy(cp, rawJSON)

	s.mu.Lock()
	defer s.mu.Unlock()

	if elem, ok := s.lruMap[key]; ok {
		old := elem.Value.(*lruEntry)
		s.curBytes -= old.byteWeight
		old.rawJSON = cp
		old.sha256Hex = shaHex
		old.byteWeight = weight
		s.curBytes += weight
		s.lruList.MoveToFront(elem)
	} else {
		ent := &lruEntry{
			key:        key,
			rawJSON:    cp,
			sha256Hex:  shaHex,
			byteWeight: weight,
		}
		elem := s.lruList.PushFront(ent)
		s.lruMap[key] = elem
		s.curBytes += weight
	}

	for s.curBytes > s.maxBytes && s.lruList.Len() > 0 {
		back := s.lruList.Back()
		if back == nil {
			break
		}
		ent := back.Value.(*lruEntry)
		delete(s.lruMap, ent.key)
		s.lruList.Remove(back)
		s.curBytes -= ent.byteWeight
	}
}

func (s *LocalFSFacetStore) evictKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if elem, ok := s.lruMap[key]; ok {
		ent := elem.Value.(*lruEntry)
		s.curBytes -= ent.byteWeight
		delete(s.lruMap, key)
		s.lruList.Remove(elem)
	}
}

// atomicWriteFile 在目标同目录下创建临时文件 -> 写入 -> fsync -> 关闭 -> 原子 rename。
func atomicWriteFile(finalPath string, data []byte, tmpPrefix string) error {
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	tmpPath := filepath.Join(dir, tmpPrefix+uuid.NewString()+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}
