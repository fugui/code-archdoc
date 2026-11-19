package doctype

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"code-archdoc/pkg/sandbox"

	"github.com/google/uuid"
)

// AssetBundle 表示从 doc_types/<slug>/ 目录加载的完整不可变资产快照。
type AssetBundle struct {
	Slug               string
	Meta               Meta
	RawMetaJSON        []byte
	TemplateMD         string
	GenerationPromptMD string
	RefinePromptMD     string
	AggregateHash      string
	PromptHash         string
}

// Registry 以服务端受控目录 doc_types/<slug>/ 为唯一事实源（SSOT），管理加载、哈希计算与原子写盘。
type Registry struct {
	rootDir   string
	slugLocks sync.Map // map[string]*sync.Mutex
}

// NewRegistry 创建目录注册表实例（自动确保 rootDir 存在）。
func NewRegistry(rootDir string) (*Registry, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, fmt.Errorf("doc_types rootDir 不能为空")
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absRoot, 0750); err != nil {
		return nil, err
	}
	return &Registry{rootDir: absRoot}, nil
}

// RootDir 返回受控根目录物理路径。
func (r *Registry) RootDir() string {
	return r.rootDir
}

func (r *Registry) lockSlug(slug string) *sync.Mutex {
	mu, _ := r.slugLocks.LoadOrStore(slug, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// SeedBuiltin 幂等初始化内置 5 类 DocType 资产目录。
func (r *Registry) SeedBuiltin() ([]string, error) {
	return SeedBuiltinDocTypes(r.rootDir)
}

// LoadOne 安全读取并校验单个 doc_types/<slug>/ 目录资产。
func (r *Registry) LoadOne(slug string) (*AssetBundle, error) {
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	mu := r.lockSlug(slug)
	mu.Lock()
	defer mu.Unlock()

	return r.loadOneUnlocked(slug)
}

func (r *Registry) loadOneUnlocked(slug string) (*AssetBundle, error) {
	dirPath, err := sandbox.SafeJoin(r.rootDir, slug)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(dirPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrDocTypeNotFound
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, ErrDocTypeNotFound
	}

	filesMap := make(map[string][]byte, len(OrderedAssetFiles))
	for _, name := range OrderedAssetFiles {
		fp, joinErr := sandbox.SafeJoin(dirPath, name)
		if joinErr != nil {
			return nil, joinErr
		}
		raw, readErr := os.ReadFile(fp)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) && name == "refine_prompt.md" {
				continue
			}
			return nil, fmt.Errorf("读取 %s/%s 失败: %w", slug, name, readErr)
		}
		filesMap[name] = raw
	}

	var meta Meta
	if err := json.Unmarshal(filesMap["meta.json"], &meta); err != nil {
		return nil, fmt.Errorf("%w: 解析 %s/meta.json 失败: %v", ErrInvalidMeta, slug, err)
	}
	if err := meta.Validate(slug); err != nil {
		return nil, err
	}

	templateMD := string(NormalizeCRLFToLF(filesMap["template.md"]))
	if err := ValidateTemplateSlots(templateMD, meta.Slots); err != nil {
		return nil, err
	}
	genPromptMD := string(NormalizeCRLFToLF(filesMap["generation_prompt.md"]))
	refinePromptMD := string(NormalizeCRLFToLF(filesMap["refine_prompt.md"]))

	return &AssetBundle{
		Slug:               slug,
		Meta:               meta,
		RawMetaJSON:        filesMap["meta.json"],
		TemplateMD:         templateMD,
		GenerationPromptMD: genPromptMD,
		RefinePromptMD:     refinePromptMD,
		AggregateHash:      ComputeAggregateHash(filesMap),
		PromptHash:         ComputePromptHash(genPromptMD, refinePromptMD),
	}, nil
}

// LoadAll 扫描 doc_types/ 根目录下所有合法 <slug>/ 子目录并返回按 slug 排序的资产集合。
func (r *Registry) LoadAll() ([]*AssetBundle, error) {
	entries, err := os.ReadDir(r.rootDir)
	if err != nil {
		return nil, fmt.Errorf("扫描 doc_types 目录失败: %w", err)
	}

	var slugs []string
	for _, ent := range entries {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		if ValidateSlug(ent.Name()) != nil {
			continue
		}
		slugs = append(slugs, ent.Name())
	}
	sort.Strings(slugs)

	bundles := make([]*AssetBundle, 0, len(slugs))
	for _, slug := range slugs {
		b, loadErr := r.LoadOne(slug)
		if loadErr != nil {
			return nil, loadErr
		}
		bundles = append(bundles, b)
	}
	return bundles, nil
}

// WriteAssetAtomic 持 slug 锁校验并原子写盘（tmp + fsync + rename），供 W2 在线管理与单测复用。
func (r *Registry) WriteAssetAtomic(slug string, meta Meta, templateMD, genPromptMD, refinePromptMD string) (*AssetBundle, error) {
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	if err := meta.Validate(slug); err != nil {
		return nil, err
	}
	if err := ValidateTemplateSlots(templateMD, meta.Slots); err != nil {
		return nil, err
	}

	mu := r.lockSlug(slug)
	mu.Lock()
	defer mu.Unlock()

	dirPath, err := sandbox.SafeJoin(r.rootDir, slug)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dirPath, 0750); err != nil {
		return nil, err
	}

	metaBytes, err := meta.MarshalPrettyJSON()
	if err != nil {
		return nil, err
	}

	toWrite := map[string][]byte{
		"meta.json":            metaBytes,
		"template.md":          NormalizeCRLFToLF([]byte(templateMD)),
		"generation_prompt.md": NormalizeCRLFToLF([]byte(genPromptMD)),
		"refine_prompt.md":     NormalizeCRLFToLF([]byte(refinePromptMD)),
	}
	for _, name := range OrderedAssetFiles {
		fp, joinErr := sandbox.SafeJoin(dirPath, name)
		if joinErr != nil {
			return nil, joinErr
		}
		if err := atomicWriteAssetFile(fp, toWrite[name]); err != nil {
			return nil, err
		}
	}

	return r.loadOneUnlocked(slug)
}

// DeleteDirectory 持 slug 锁安全删除 doc_types/<slug>/ 目录。
func (r *Registry) DeleteDirectory(slug string) error {
	if err := ValidateSlug(slug); err != nil {
		return err
	}
	mu := r.lockSlug(slug)
	mu.Lock()
	defer mu.Unlock()

	dirPath, err := sandbox.SafeJoin(r.rootDir, slug)
	if err != nil {
		return err
	}
	return os.RemoveAll(dirPath)
}

func atomicWriteAssetFile(finalPath string, data []byte) error {
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	tmpPath := filepath.Join(dir, ".tmp-"+filepath.Base(finalPath)+"-"+uuid.NewString())
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
