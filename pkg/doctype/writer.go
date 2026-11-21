package doctype

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"code-archdoc/pkg/sandbox"

	"github.com/google/uuid"
)

// Exists 检查受控目录下指定 slug 的 DocType 是否已存在。
func (r *Registry) Exists(slug string) (bool, error) {
	if err := ValidateSlug(slug); err != nil {
		return false, err
	}
	dirPath, err := sandbox.SafeJoin(r.rootDir, slug)
	if err != nil {
		return false, err
	}
	st, statErr := os.Stat(dirPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return false, nil
		}
		return false, statErr
	}
	return st.IsDir(), nil
}

// CreateAsset 持 slug 锁新建 DocType 目录资产；若目标 slug 已存在则返回 ErrDocTypeExists。
func (r *Registry) CreateAsset(slug string, meta Meta, templateMD, genPromptMD, refinePromptMD string) (*AssetBundle, error) {
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	meta.Name = slug
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
	if _, statErr := os.Stat(dirPath); statErr == nil {
		return nil, fmt.Errorf("%w: %s", ErrDocTypeExists, slug)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	return r.writeAssetUnlocked(slug, dirPath, meta, templateMD, genPromptMD, refinePromptMD)
}

// CloneAsset 按字典序获取双 slug 写锁（防死锁），将源 DocType 资产完整克隆至 newSlug，保留未知扩展字段并置 is_builtin=false。
func (r *Registry) CloneAsset(srcSlug, newSlug, displayName string) (*AssetBundle, error) {
	if err := ValidateSlug(srcSlug); err != nil {
		return nil, err
	}
	if err := ValidateSlug(newSlug); err != nil {
		return nil, err
	}
	if srcSlug == newSlug {
		return nil, fmt.Errorf("%w: %s", ErrDocTypeExists, newSlug)
	}

	firstSlug, secondSlug := srcSlug, newSlug
	if firstSlug > secondSlug {
		firstSlug, secondSlug = secondSlug, firstSlug
	}
	mu1 := r.lockSlug(firstSlug)
	mu2 := r.lockSlug(secondSlug)
	mu1.Lock()
	defer mu1.Unlock()
	mu2.Lock()
	defer mu2.Unlock()

	srcBundle, err := r.loadOneUnlocked(srcSlug)
	if err != nil {
		return nil, err
	}

	dstDir, err := sandbox.SafeJoin(r.rootDir, newSlug)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(dstDir); statErr == nil {
		return nil, fmt.Errorf("%w: %s", ErrDocTypeExists, newSlug)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	clonedMeta := cloneMeta(srcBundle.Meta)
	clonedMeta.Name = newSlug
	clonedMeta.IsBuiltin = false
	trimmedDisplay := strings.TrimSpace(displayName)
	if trimmedDisplay != "" {
		if trimmedDisplay != strings.TrimSpace(srcBundle.Meta.DisplayName) {
			clonedMeta.ShortName = ""
		}
		clonedMeta.DisplayName = trimmedDisplay
	} else {
		clonedMeta.ShortName = ""
		clonedMeta.DisplayName = srcBundle.Meta.DisplayName + " (副本)"
	}

	return r.writeAssetUnlocked(newSlug, dstDir, clonedMeta, srcBundle.TemplateMD, srcBundle.GenerationPromptMD, srcBundle.RefinePromptMD)
}

// UpdateAsset 持 slug 锁校验并原子更新已有 DocType 资产，自动合并保留磁盘中既有的未知 ExtraFields。
func (r *Registry) UpdateAsset(slug string, meta Meta, templateMD, genPromptMD, refinePromptMD string) (*AssetBundle, error) {
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	if meta.Name == "" {
		meta.Name = slug
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

	existing, err := r.loadOneUnlocked(slug)
	if err != nil {
		return nil, err
	}

	// 无损合并未知扩展字段：磁盘既有未知键作为基底，请求携带的扩展键按需覆盖
	mergedExtras := mergeExtraFields(existing.Meta.ExtraFields, meta.ExtraFields)
	meta.ExtraFields = mergedExtras
	// 防止 Web 误改内置标识
	meta.IsBuiltin = existing.Meta.IsBuiltin

	dirPath, err := sandbox.SafeJoin(r.rootDir, slug)
	if err != nil {
		return nil, err
	}
	return r.writeAssetUnlocked(slug, dirPath, meta, templateMD, genPromptMD, refinePromptMD)
}

// UpdateStatus 持 slug 锁仅原子切换 meta.json 中的 is_active 状态，保持未知扩展键与其他资产文件无损。
func (r *Registry) UpdateStatus(slug string, isActive bool) (*AssetBundle, error) {
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}

	mu := r.lockSlug(slug)
	mu.Lock()
	defer mu.Unlock()

	existing, err := r.loadOneUnlocked(slug)
	if err != nil {
		return nil, err
	}
	updatedMeta := cloneMeta(existing.Meta)
	updatedMeta.IsActive = isActive

	dirPath, err := sandbox.SafeJoin(r.rootDir, slug)
	if err != nil {
		return nil, err
	}
	metaBytes, err := updatedMeta.MarshalPrettyJSON()
	if err != nil {
		return nil, err
	}
	metaPath, err := sandbox.SafeJoin(dirPath, "meta.json")
	if err != nil {
		return nil, err
	}
	if err := atomicWriteAssetFile(metaPath, metaBytes); err != nil {
		return nil, err
	}
	return r.loadOneUnlocked(slug)
}

// WriteAssetAtomic 持 slug 锁校验并原子写盘（tmp + fsync + rename），支持新建或覆盖写入。
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
	return r.writeAssetUnlocked(slug, dirPath, meta, templateMD, genPromptMD, refinePromptMD)
}

func (r *Registry) writeAssetUnlocked(
	slug string,
	dirPath string,
	meta Meta,
	templateMD, genPromptMD, refinePromptMD string,
) (*AssetBundle, error) {
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

func mergeExtraFields(existing, incoming map[string]json.RawMessage) map[string]json.RawMessage {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(existing)+len(incoming))
	for k, v := range existing {
		if !knownMetaKeys[k] {
			cp := make(json.RawMessage, len(v))
			copy(cp, v)
			out[k] = cp
		}
	}
	for k, v := range incoming {
		if !knownMetaKeys[k] {
			cp := make(json.RawMessage, len(v))
			copy(cp, v)
			out[k] = cp
		}
	}
	return out
}

func cloneMeta(m Meta) Meta {
	cp := m
	if len(m.IRFacets) > 0 {
		cp.IRFacets = append([]string(nil), m.IRFacets...)
	}
	if len(m.Slots) > 0 {
		cp.Slots = make([]SlotSpec, len(m.Slots))
		for i, sl := range m.Slots {
			slCopy := sl
			if len(sl.IRFacets) > 0 {
				slCopy.IRFacets = append([]string(nil), sl.IRFacets...)
			}
			cp.Slots[i] = slCopy
		}
	}
	if m.ScopeProfile != nil {
		sp := *m.ScopeProfile
		sp.IncludeGlobs = append([]string(nil), sp.IncludeGlobs...)
		sp.ExcludeGlobs = append([]string(nil), sp.ExcludeGlobs...)
		cp.ScopeProfile = &sp
	}
	if m.VerifyPolicy != nil {
		vp := *m.VerifyPolicy
		vp.AllowedMermaidTypes = append([]string(nil), vp.AllowedMermaidTypes...)
		if vp.BlockOnSecretLeak != nil {
			b := *vp.BlockOnSecretLeak
			vp.BlockOnSecretLeak = &b
		}
		cp.VerifyPolicy = &vp
	}
	cp.ExtraFields = mergeExtraFields(m.ExtraFields, nil)
	return cp
}
