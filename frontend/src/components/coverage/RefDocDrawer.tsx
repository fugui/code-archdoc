import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  Archive,
  BookOpen,
  Edit3,
  FilePlus2,
  Link2,
  Loader2,
  Plus,
  RotateCcw,
  Trash2,
  Unlink,
  Upload,
  X,
} from 'lucide-react';
import { EmptyState, useConfirm, useToast } from '@code/common';
import {
  createRefDoc,
  createRefDocBinding,
  deleteRefDocBinding,
  getEffectiveRefDocs,
  getRefDocDetail,
  listRefDocs,
  purgeRefDoc,
  updateRefDoc,
} from '../../api/refDocs';
import type {
  ArchTreeNodeVO,
  DocTypeSummaryVO,
  EffectiveRefDocVO,
  EffectiveRefDocsResponse,
  RefDocKind,
  RefDocScopeLevel,
  ReferenceDocDetailVO,
  ReferenceDocSummaryVO,
} from '../../types';
import { formatApiError } from '../../utils/errorMessages';
import {
  formatRefDocKindLabel,
  formatRefDocScopeLabel,
  formatRefDocSummaryStaleHint,
  formatRefDocTokenBadge,
} from '../../utils/refDocUtils';
import SectionMarkdownRenderer from '../studio/SectionMarkdownRenderer';

export type RefDocDrawerTarget =
  | { mode: 'system'; label: string }
  | { mode: 'arch'; node: ArchTreeNodeVO };

export interface RefDocDrawerProps {
  open: boolean;
  target: RefDocDrawerTarget | null;
  docTypes: DocTypeSummaryVO[];
  onClose: () => void;
  onChanged: () => void;
}

const DOC_KIND_OPTIONS: Array<{ value: RefDocKind; label: string }> = [
  { value: 'arch_design', label: '架构设计' },
  { value: 'domain_glossary', label: '领域术语' },
  { value: 'interface_spec', label: '接口契约' },
  { value: 'adr_constraint', label: 'ADR 约束' },
  { value: 'other', label: '其他参考' },
];

type DrawerViewMode = 'list' | 'create' | 'detail' | 'bind_existing';

export const RefDocDrawer: React.FC<RefDocDrawerProps> = ({
  open,
  target,
  docTypes,
  onClose,
  onChanged,
}) => {
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [viewMode, setViewMode] = useState<DrawerViewMode>('list');
  const [loading, setLoading] = useState<boolean>(false);
  const [effectiveData, setEffectiveData] = useState<EffectiveRefDocsResponse | null>(null);
  const [systemDocs, setSystemDocs] = useState<ReferenceDocSummaryVO[]>([]);

  // Detail / Edit state
  const [detailDoc, setDetailDoc] = useState<ReferenceDocDetailVO | null>(null);
  const [loadingDetail, setLoadingDetail] = useState<boolean>(false);
  const [editing, setEditing] = useState<boolean>(false);
  const [editTitle, setEditTitle] = useState<string>('');
  const [editKind, setEditKind] = useState<RefDocKind>('arch_design');
  const [editDesc, setEditDesc] = useState<string>('');
  const [editSummary, setEditSummary] = useState<string>('');
  const [editContent, setEditContent] = useState<string>('');
  const [editTermsRaw, setEditTermsRaw] = useState<string>('');
  const [editConfirmRedact, setEditConfirmRedact] = useState<boolean>(false);

  // Create state
  const [newTitle, setNewTitle] = useState<string>('');
  const [newKind, setNewKind] = useState<RefDocKind>('arch_design');
  const [newDesc, setNewDesc] = useState<string>('');
  const [newSummary, setNewSummary] = useState<string>('');
  const [newContent, setNewContent] = useState<string>('');
  const [newFile, setNewFile] = useState<File | null>(null);
  const [newTermsRaw, setNewTermsRaw] = useState<string>('');
  const [newInherit, setNewInherit] = useState<boolean>(true);
  const [newDocTypeSlugs, setNewDocTypeSlugs] = useState<string[]>([]);
  const [createConfirmRedact, setCreateConfirmRedact] = useState<boolean>(false);
  const [submitting, setSubmitting] = useState<boolean>(false);

  // Bind existing state
  const [candidateDocs, setCandidateDocs] = useState<ReferenceDocSummaryVO[]>([]);
  const [bindSearch, setBindSearch] = useState<string>('');
  const [bindInherit, setBindInherit] = useState<boolean>(true);

  const scopeMeta = useMemo(() => {
    if (!target) return null;
    if (target.mode === 'system') {
      return {
        scopeLevel: 'system' as RefDocScopeLevel,
        archElementID: undefined,
        repoID: undefined,
        subdirectory: undefined,
        title: target.label || '全系统全局参考文档库',
      };
    }
    const n = target.node;
    return {
      scopeLevel: (n.type as RefDocScopeLevel) || 'module',
      archElementID: n.id,
      repoID: n.repo_id ?? undefined,
      subdirectory: n.subdirectory || '',
      title: `${n.name_cn || n.name_en || n.identifier} · 参考文档`,
    };
  }, [target]);

  const loadList = useCallback(async () => {
    if (!target || !scopeMeta) return;
    setLoading(true);
    try {
      if (target.mode === 'system') {
        const res = await listRefDocs({
          scope_level: 'system',
          status: 'all',
          page: 1,
          pageSize: 50,
        });
        setSystemDocs(res.items || []);
        setEffectiveData(null);
      } else {
        const eff = await getEffectiveRefDocs({
          arch_element_id: target.node.id,
        });
        setEffectiveData(eff);
        setSystemDocs([]);
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载参考文档失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [target, scopeMeta, showToast]);

  useEffect(() => {
    if (!open || !target) return;
    setViewMode('list');
    setDetailDoc(null);
    setEditing(false);
    setCreateConfirmRedact(false);
    setEditConfirmRedact(false);
    void loadList();
  }, [open, target, loadList]);

  const openDetail = async (docId: number, version?: number) => {
    setViewMode('detail');
    setEditing(false);
    setEditConfirmRedact(false);
    setLoadingDetail(true);
    try {
      const detail = await getRefDocDetail(docId, version);
      setDetailDoc(detail);
      setEditTitle(detail.title);
      setEditKind(detail.doc_kind);
      setEditDesc(detail.description || '');
      setEditSummary(detail.summary_md || '');
      setEditContent(detail.content_md || '');
      setEditTermsRaw((detail.declared_terms || []).join(', '));
    } catch (err: unknown) {
      showToast(formatApiError(err, '读取参考文档详情失败').message, 'error');
      setViewMode('list');
    } finally {
      setLoadingDetail(false);
    }
  };

  const handleOpenBindExisting = async () => {
    setViewMode('bind_existing');
    setLoading(true);
    try {
      const res = await listRefDocs({
        status: 'active',
        search: bindSearch,
        page: 1,
        pageSize: 30,
      });
      setCandidateDocs(res.items || []);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载可选参考文档失败').message, 'error');
    } finally {
      setLoading(false);
    }
  };

  const handleCreateSubmit = async (redactAndSave: boolean) => {
    if (!scopeMeta || submitting) return;
    if (!newTitle.trim()) {
      showToast('请输入参考文档标题', 'warning');
      return;
    }
    if (!newFile && !newContent.trim()) {
      showToast('请粘贴 Markdown 正文或选择上传 .md 文件', 'warning');
      return;
    }

    setSubmitting(true);
    try {
      const terms = newTermsRaw
        .split(/[,，\n]/)
        .map((s) => s.trim())
        .filter(Boolean);
      const created = await createRefDoc(
        {
          title: newTitle.trim(),
          doc_kind: newKind,
          description: newDesc.trim() || undefined,
          summary_md: newSummary.trim() || undefined,
          content_md: newFile ? undefined : newContent,
          declared_terms: terms,
          redact_and_save: redactAndSave,
          initial_binding: {
            scope_level: scopeMeta.scopeLevel,
            arch_element_id: scopeMeta.archElementID,
            repo_id: scopeMeta.repoID,
            subdirectory: scopeMeta.subdirectory,
            inherit_to_children: newInherit,
            doc_type_slugs: newDocTypeSlugs,
          },
        },
        newFile,
      );
      showToast(`参考文档「${created.title}」已创建 (v${created.current_version})`, 'success');
      setCreateConfirmRedact(false);
      setNewTitle('');
      setNewDesc('');
      setNewSummary('');
      setNewContent('');
      setNewFile(null);
      setNewTermsRaw('');
      setNewDocTypeSlugs([]);
      setViewMode('list');
      await loadList();
      onChanged();
    } catch (err: unknown) {
      const formatted = formatApiError(err, '创建参考文档失败');
      if (formatted.code === 'SECRET_CONFIRM_REQUIRED') {
        setCreateConfirmRedact(true);
        showToast(formatted.message, 'warning');
      } else {
        showToast(`${formatted.title}：${formatted.message}`, 'error');
      }
    } finally {
      setSubmitting(false);
    }
  };

  const handleSaveEdit = async (redactAndSave: boolean) => {
    if (!detailDoc || submitting) return;
    setSubmitting(true);
    try {
      const terms = editTermsRaw
        .split(/[,，\n]/)
        .map((s) => s.trim())
        .filter(Boolean);
      const updated = await updateRefDoc(detailDoc.id, {
        expected_version: detailDoc.current_version,
        title: editTitle.trim(),
        doc_kind: editKind,
        description: editDesc.trim(),
        summary_md: editSummary.trim(),
        content_md: editContent,
        declared_terms: terms,
        redact_and_save: redactAndSave,
      });
      setDetailDoc(updated);
      setEditing(false);
      setEditConfirmRedact(false);
      showToast(`已保存更新至 v${updated.current_version}`, 'success');
      await loadList();
      onChanged();
    } catch (err: unknown) {
      const formatted = formatApiError(err, '更新参考文档失败');
      if (formatted.code === 'SECRET_CONFIRM_REQUIRED') {
        setEditConfirmRedact(true);
        showToast(formatted.message, 'warning');
      } else {
        showToast(`${formatted.title}：${formatted.message}`, 'error');
      }
    } finally {
      setSubmitting(false);
    }
  };

  const handleToggleStatus = async (doc: ReferenceDocDetailVO) => {
    const nextStatus = doc.status === 'active' ? 'archived' : 'active';
    setSubmitting(true);
    try {
      const updated = await updateRefDoc(doc.id, {
        expected_version: doc.current_version,
        status: nextStatus,
      });
      setDetailDoc(updated);
      showToast(nextStatus === 'archived' ? '参考文档已归档' : '参考文档已恢复活跃', 'success');
      await loadList();
      onChanged();
    } catch (err: unknown) {
      showToast(formatApiError(err, '切换状态失败').message, 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const handlePurge = async (doc: ReferenceDocDetailVO) => {
    const ok = await confirm({
      title: '彻底删除已归档参考文档',
      content: `将永久清理「${doc.title}」的元数据与全部历史磁盘版本，不可恢复。确定继续？`,
      confirmText: '确认永久删除',
      cancelText: '取消',
      type: 'danger',
    });
    if (!ok) return;
    setSubmitting(true);
    try {
      await purgeRefDoc(doc.id);
      showToast('已彻底清理参考文档', 'success');
      setDetailDoc(null);
      setViewMode('list');
      await loadList();
      onChanged();
    } catch (err: unknown) {
      showToast(formatApiError(err, '删除失败').message, 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const handleUnbind = async (item: EffectiveRefDocVO) => {
    const ok = await confirm({
      title: '解除消费绑定',
      content: `确定从当前作用域解绑参考文档「${item.title}」吗？（文档本体不会被删除）`,
      confirmText: '确认解绑',
      cancelText: '取消',
      type: 'warning',
    });
    if (!ok) return;
    try {
      await deleteRefDocBinding(item.doc_id, item.binding_id);
      showToast('已解除绑定', 'success');
      await loadList();
      onChanged();
    } catch (err: unknown) {
      const formatted = formatApiError(err, '解除绑定失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    }
  };

  const handleBindDoc = async (docId: number) => {
    if (!scopeMeta) return;
    setSubmitting(true);
    try {
      await createRefDocBinding(docId, {
        scope_level: scopeMeta.scopeLevel,
        arch_element_id: scopeMeta.archElementID,
        repo_id: scopeMeta.repoID,
        subdirectory: scopeMeta.subdirectory,
        inherit_to_children: bindInherit,
      });
      showToast('绑定参考文档成功', 'success');
      setViewMode('list');
      await loadList();
      onChanged();
    } catch (err: unknown) {
      const formatted = formatApiError(err, '绑定失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setSubmitting(false);
    }
  };

  if (!open || !target || !scopeMeta) {
    return null;
  }

  const directItems = (effectiveData?.items ?? []).filter((it) => !it.is_inherited);
  const inheritedItems = (effectiveData?.items ?? []).filter((it) => it.is_inherited);

  return (
    <div className="code-archdoc-refdoc-overlay" role="dialog" aria-modal="true" aria-label={scopeMeta.title}>
      <div className="code-archdoc-refdoc-backdrop" onClick={onClose} />
      <aside className="code-archdoc-refdoc-drawer">
        <header className="code-archdoc-refdoc-head">
          <div className="code-archdoc-refdoc-head-title">
            <BookOpen size={17} />
            <span>{scopeMeta.title}</span>
            <span className="code-badge code-badge--primary">
              {formatRefDocScopeLabel(scopeMeta.scopeLevel)}
            </span>
          </div>
          <button
            type="button"
            className="btn btn-text btn-small"
            onClick={onClose}
            aria-label="关闭参考文档抽屉"
          >
            <X size={16} />
          </button>
        </header>

        <div className="code-archdoc-refdoc-toolbar">
          {viewMode !== 'list' ? (
            <button
              type="button"
              className="code-archdoc-ghost-btn"
              onClick={() => setViewMode('list')}
            >
              返回列表
            </button>
          ) : (
            <div className="flex-center gap-sm">
              <button
                type="button"
                className="btn btn-primary btn-small"
                onClick={() => {
                  setCreateConfirmRedact(false);
                  setViewMode('create');
                }}
              >
                <Plus size={14} />
                <span>新建参考文档</span>
              </button>
              {target.mode === 'arch' && (
                <button
                  type="button"
                  className="code-archdoc-ghost-btn"
                  onClick={() => void handleOpenBindExisting()}
                >
                  <Link2 size={14} />
                  <span>关联已有文档</span>
                </button>
              )}
            </div>
          )}
        </div>

        <div className="code-archdoc-refdoc-body">
          {viewMode === 'list' && loading && (
            <div className="code-archdoc-loading">
              <Loader2 size={16} className="code-archdoc-spin" />
              <span>正在加载生效参考文档...</span>
            </div>
          )}

          {viewMode === 'list' && !loading && target.mode === 'system' && (
            systemDocs.length === 0 ? (
              <EmptyState
                type="data"
                title="暂无全系统级参考文档"
                description="全系统级参考文档仅管理员可上传与归属管理，创建后将自动继承覆盖全平台所有子系统与模块。"
              />
            ) : (
              <div className="code-archdoc-refdoc-list">
                {systemDocs.map((d) => (
                  <div key={d.id} className="code-archdoc-refdoc-card">
                    <div className="code-archdoc-refdoc-card-head">
                      <button
                        type="button"
                        className="code-archdoc-refdoc-link"
                        onClick={() => void openDetail(d.id)}
                      >
                        {d.title}
                      </button>
                      <div className="flex-center gap-xs">
                        <span className="code-badge code-badge--muted">
                          {formatRefDocKindLabel(d.doc_kind)}
                        </span>
                        <span className="code-archdoc-mono">v{d.current_version}</span>
                        <span className="code-badge code-badge--info">
                          {formatRefDocTokenBadge(d.estimated_tokens)}
                        </span>
                        {d.status === 'archived' && (
                          <span className="code-badge code-badge--warning">已归档</span>
                        )}
                      </div>
                    </div>
                    {d.summary_md && <p className="code-archdoc-refdoc-summary">{d.summary_md}</p>}
                  </div>
                ))}
              </div>
            )
          )}

          {viewMode === 'list' && !loading && target.mode === 'arch' && (
            <div className="code-archdoc-refdoc-sections">
              <section>
                <h4 className="code-archdoc-refdoc-sec-title">
                  直接绑定 ({directItems.length})
                </h4>
                {directItems.length === 0 ? (
                  <p className="text-muted">当前节点尚无直接绑定的参考文档</p>
                ) : (
                  <div className="code-archdoc-refdoc-list">
                    {directItems.map((it) => (
                      <div key={`${it.doc_id}-${it.binding_id}`} className="code-archdoc-refdoc-card">
                        <div className="code-archdoc-refdoc-card-head">
                          <button
                            type="button"
                            className="code-archdoc-refdoc-link"
                            onClick={() => void openDetail(it.doc_id)}
                          >
                            {it.title}
                          </button>
                          <div className="flex-center gap-xs">
                            <span className="code-badge code-badge--muted">
                              {formatRefDocKindLabel(it.doc_kind)}
                            </span>
                            <span className="code-archdoc-mono">v{it.current_version}</span>
                            <span className="code-badge code-badge--info">
                              {formatRefDocTokenBadge(it.estimated_tokens)}
                            </span>
                            {it.is_home && (
                              <span className="code-badge code-badge--primary">主归属</span>
                            )}
                            {it.can_unbind && (
                              <button
                                type="button"
                                className="btn btn-text btn-small"
                                onClick={() => void handleUnbind(it)}
                                title="解除当前节点绑定"
                              >
                                <Unlink size={12} />
                                <span>解绑</span>
                              </button>
                            )}
                          </div>
                        </div>
                        {it.summary_md && (
                          <p className="code-archdoc-refdoc-summary">{it.summary_md}</p>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </section>

              <section>
                <h4 className="code-archdoc-refdoc-sec-title">
                  上级继承生效 ({inheritedItems.length})
                </h4>
                {inheritedItems.length === 0 ? (
                  <p className="text-muted">暂无来自上位架构层的继承参考文档</p>
                ) : (
                  <div className="code-archdoc-refdoc-list">
                    {inheritedItems.map((it) => (
                      <div key={`inh-${it.doc_id}`} className="code-archdoc-refdoc-card">
                        <div className="code-archdoc-refdoc-card-head">
                          <button
                            type="button"
                            className="code-archdoc-refdoc-link"
                            onClick={() => void openDetail(it.doc_id)}
                          >
                            {it.title}
                          </button>
                          <div className="flex-center gap-xs">
                            <span className="code-badge code-badge--purple">
                              继承自 {it.source_scope_label}
                            </span>
                            <span className="code-archdoc-mono">v{it.current_version}</span>
                            <span className="code-badge code-badge--info">
                              {formatRefDocTokenBadge(it.estimated_tokens)}
                            </span>
                          </div>
                        </div>
                        {it.summary_md && (
                          <p className="code-archdoc-refdoc-summary">{it.summary_md}</p>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </section>
            </div>
          )}

          {viewMode === 'create' && (
            <div className="code-archdoc-refdoc-form">
              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">标题 *</label>
                <input
                  type="text"
                  className="code-input"
                  value={newTitle}
                  onChange={(e) => setNewTitle(e.target.value)}
                  placeholder="例如：订单状态机与事务设计背景"
                />
              </div>

              <div className="code-archdoc-refdoc-form-row">
                <div className="code-archdoc-field">
                  <label className="code-archdoc-field-label">文档分类</label>
                  <select
                    className="code-select"
                    value={newKind}
                    onChange={(e) => setNewKind(e.target.value as RefDocKind)}
                  >
                    {DOC_KIND_OPTIONS.map((opt) => (
                      <option key={opt.value} value={opt.value}>
                        {opt.label}
                      </option>
                    ))}
                  </select>
                </div>
                <label className="code-archdoc-radio-label">
                  <input
                    type="checkbox"
                    checked={newInherit}
                    onChange={(e) => setNewInherit(e.target.checked)}
                  />
                  <span>向下继承至子层级</span>
                </label>
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">
                  适用文档类型过滤（不勾选表示全部 DocType 均注入）
                </label>
                <div className="code-archdoc-refdoc-slug-chips">
                  {docTypes
                    .filter((dt) => dt.is_active)
                    .map((dt) => {
                      const active = newDocTypeSlugs.includes(dt.slug);
                      return (
                        <label key={dt.slug} className="code-archdoc-radio-label">
                          <input
                            type="checkbox"
                            checked={active}
                            onChange={() =>
                              setNewDocTypeSlugs((prev) =>
                                active ? prev.filter((s) => s !== dt.slug) : [...prev, dt.slug],
                              )
                            }
                          />
                          <span>{dt.display_name}</span>
                        </label>
                      );
                    })}
                </div>
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">领域术语白名单（逗号分隔，仅用于 Verifier 降级告警）</label>
                <input
                  type="text"
                  className="code-input"
                  value={newTermsRaw}
                  onChange={(e) => setNewTermsRaw(e.target.value)}
                  placeholder="例如：OrderSagaCoordinator, PaymentGateway"
                />
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">可选摘要（留空则自动提取）</label>
                <textarea
                  rows={2}
                  className="code-textarea"
                  value={newSummary}
                  onChange={(e) => setNewSummary(e.target.value)}
                  placeholder="≤ 300 字符精炼背景摘要"
                />
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">
                  上传 Markdown 文件或直接粘贴正文（≤ 512KB）
                </label>
                <div className="code-archdoc-refdoc-upload-bar">
                  <label className="code-archdoc-ghost-btn">
                    <Upload size={14} />
                    <span>{newFile ? newFile.name : '选择 .md 文件'}</span>
                    <input
                      type="file"
                      accept=".md,.markdown,.txt"
                      className="code-archdoc-sr-only"
                      onChange={(e) => {
                        const f = e.target.files?.[0] ?? null;
                        setNewFile(f);
                      }}
                    />
                  </label>
                  {newFile && (
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => setNewFile(null)}
                    >
                      清除文件
                    </button>
                  )}
                </div>
                {!newFile && (
                  <textarea
                    rows={10}
                    className="code-textarea code-archdoc-mono"
                    value={newContent}
                    onChange={(e) => setNewContent(e.target.value)}
                    placeholder="# 背景与设计约束&#10;&#10;支持完整 Markdown..."
                  />
                )}
              </div>

              {createConfirmRedact && (
                <div className="code-archdoc-alert code-archdoc-alert--warning">
                  <AlertTriangle size={16} />
                  <div className="code-archdoc-alert-body">
                    <div>检出疑似密钥赋值或高熵敏感片段，可点击下方按钮自动脱敏为 `[REDACTED]` 后保存。</div>
                    <button
                      type="button"
                      className="btn btn-primary btn-small"
                      disabled={submitting}
                      onClick={() => void handleCreateSubmit(true)}
                    >
                      自动脱敏并保存
                    </button>
                  </div>
                </div>
              )}

              <div className="code-archdoc-refdoc-form-actions">
                <button
                  type="button"
                  className="btn btn-primary"
                  disabled={submitting}
                  onClick={() => void handleCreateSubmit(false)}
                >
                  {submitting ? <Loader2 size={14} className="code-archdoc-spin" /> : <FilePlus2 size={14} />}
                  <span>创建并绑定到当前作用域</span>
                </button>
              </div>
            </div>
          )}

          {viewMode === 'bind_existing' && (
            <div className="code-archdoc-refdoc-form">
              <div className="code-archdoc-refdoc-form-row">
                <input
                  type="text"
                  className="code-input"
                  value={bindSearch}
                  onChange={(e) => setBindSearch(e.target.value)}
                  placeholder="按标题检索活跃参考文档..."
                />
                <button
                  type="button"
                  className="code-archdoc-ghost-btn"
                  onClick={() => void handleOpenBindExisting()}
                >
                  检索
                </button>
              </div>
              <label className="code-archdoc-radio-label">
                <input
                  type="checkbox"
                  checked={bindInherit}
                  onChange={(e) => setBindInherit(e.target.checked)}
                />
                <span>绑定后向下继承至子层级</span>
              </label>
              <div className="code-archdoc-refdoc-list">
                {candidateDocs.map((d) => (
                  <div key={d.id} className="code-archdoc-refdoc-card">
                    <div className="code-archdoc-refdoc-card-head">
                      <span>{d.title}</span>
                      <button
                        type="button"
                        className="btn btn-secondary btn-small"
                        disabled={submitting}
                        onClick={() => void handleBindDoc(d.id)}
                      >
                        关联至此
                      </button>
                    </div>
                    <div className="text-muted">
                      归属：{d.owner_scope_label} · v{d.current_version} · {formatRefDocTokenBadge(d.estimated_tokens)}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {viewMode === 'detail' && (
            loadingDetail ? (
              <div className="code-archdoc-loading">
                <Loader2 size={16} className="code-archdoc-spin" />
                <span>正在加载参考文档正文...</span>
              </div>
            ) : detailDoc ? (
              <div className="code-archdoc-refdoc-detail">
                <div className="code-archdoc-refdoc-detail-meta">
                  <div className="code-archdoc-refdoc-card-head">
                    <h3>{detailDoc.title}</h3>
                    <div className="flex-center gap-xs">
                      <span className="code-badge code-badge--muted">
                        {formatRefDocKindLabel(detailDoc.doc_kind)}
                      </span>
                      <span className="code-archdoc-mono">v{detailDoc.viewing_version}</span>
                      <span className="code-badge code-badge--info">
                        {formatRefDocTokenBadge(detailDoc.estimated_tokens)}
                      </span>
                      {detailDoc.status === 'archived' && (
                        <span className="code-badge code-badge--warning">已归档</span>
                      )}
                    </div>
                  </div>
                  <div className="text-muted">
                    主归属：{detailDoc.owner_scope_label} · SHA256: {detailDoc.content_sha256.slice(0, 12)}
                  </div>
                  {detailDoc.summary_stale && (
                    <div className="code-archdoc-alert code-archdoc-alert--warning">
                      <AlertTriangle size={15} />
                      <span>
                        {formatRefDocSummaryStaleHint(detailDoc.summary_for_version, detailDoc.current_version)}
                      </span>
                    </div>
                  )}
                  {detailDoc.can_edit && !editing && (
                    <div className="code-archdoc-refdoc-detail-actions">
                      <button
                        type="button"
                        className="code-archdoc-ghost-btn"
                        onClick={() => setEditing(true)}
                      >
                        <Edit3 size={13} />
                        <span>编辑 / 发新版</span>
                      </button>
                      <button
                        type="button"
                        className="code-archdoc-ghost-btn"
                        disabled={submitting}
                        onClick={() => void handleToggleStatus(detailDoc)}
                      >
                        {detailDoc.status === 'active' ? <Archive size={13} /> : <RotateCcw size={13} />}
                        <span>{detailDoc.status === 'active' ? '归档' : '恢复活跃'}</span>
                      </button>
                      {detailDoc.status === 'archived' && (
                        <button
                          type="button"
                          className="code-archdoc-ghost-btn code-archdoc-ghost-btn--danger"
                          disabled={submitting}
                          onClick={() => void handlePurge(detailDoc)}
                        >
                          <Trash2 size={13} />
                          <span>永久清理</span>
                        </button>
                      )}
                    </div>
                  )}
                </div>

                {editing ? (
                  <div className="code-archdoc-refdoc-form">
                    <div className="code-archdoc-field">
                      <label className="code-archdoc-field-label">标题</label>
                      <input
                        type="text"
                        className="code-input"
                        value={editTitle}
                        onChange={(e) => setEditTitle(e.target.value)}
                      />
                    </div>
                    <div className="code-archdoc-field">
                      <label className="code-archdoc-field-label">L0 摘要</label>
                      <textarea
                        rows={2}
                        className="code-textarea"
                        value={editSummary}
                        onChange={(e) => setEditSummary(e.target.value)}
                      />
                    </div>
                    <div className="code-archdoc-field">
                      <label className="code-archdoc-field-label">领域术语（逗号分隔）</label>
                      <input
                        type="text"
                        className="code-input"
                        value={editTermsRaw}
                        onChange={(e) => setEditTermsRaw(e.target.value)}
                      />
                    </div>
                    <div className="code-archdoc-field">
                      <label className="code-archdoc-field-label">Markdown 正文（变更正文将升级版本号）</label>
                      <textarea
                        rows={12}
                        className="code-textarea code-archdoc-mono"
                        value={editContent}
                        onChange={(e) => setEditContent(e.target.value)}
                      />
                    </div>
                    {editConfirmRedact && (
                      <div className="code-archdoc-alert code-archdoc-alert--warning">
                        <AlertTriangle size={16} />
                        <div className="code-archdoc-alert-body">
                          <div>检测到疑似敏感内容，确认自动脱敏并保存？</div>
                          <button
                            type="button"
                            className="btn btn-primary btn-small"
                            disabled={submitting}
                            onClick={() => void handleSaveEdit(true)}
                          >
                            自动脱敏并保存
                          </button>
                        </div>
                      </div>
                    )}
                    <div className="code-archdoc-refdoc-form-actions">
                      <button
                        type="button"
                        className="code-archdoc-ghost-btn"
                        onClick={() => setEditing(false)}
                      >
                        取消
                      </button>
                      <button
                        type="button"
                        className="btn btn-primary"
                        disabled={submitting}
                        onClick={() => void handleSaveEdit(false)}
                      >
                        保存修改
                      </button>
                    </div>
                  </div>
                ) : (
                  <div className="code-archdoc-refdoc-markdown-view">
                    <SectionMarkdownRenderer markdown={detailDoc.content_md} />
                  </div>
                )}
              </div>
            ) : null
          )}
        </div>
      </aside>
    </div>
  );
};

export default RefDocDrawer;
