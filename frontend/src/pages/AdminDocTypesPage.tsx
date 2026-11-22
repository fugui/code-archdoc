import React, { useCallback, useEffect, useState } from 'react';
import {
  Copy,
  Edit3,
  FileText,
  FolderSync,
  Loader2,
  Plus,
  ShieldAlert,
  Trash2,
} from 'lucide-react';
import { EmptyState, Modal, StatusTag, useConfirm, useToast } from '@code/common';
import {
  cloneDocType,
  deleteDocType,
  listAdminDocTypes,
  syncDocTypes,
  toggleDocTypeStatus,
} from '../api/admin';
import DocTypeEditorDrawer from '../components/doctype/DocTypeEditorDrawer';
import { useModuleUser } from '../context/ModuleUserContext';
import type { DocTypeSummaryVO } from '../types';
import { formatApiError } from '../utils/errorMessages';
import { formatDateTime } from '../utils/urlQuery';
import './AdminDocTypesPage.css';

export const AdminDocTypesPage: React.FC = () => {
  const { user } = useModuleUser();
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [items, setItems] = useState<DocTypeSummaryVO[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [syncing, setSyncing] = useState<boolean>(false);
  const [togglingSlug, setTogglingSlug] = useState<string>('');

  // 编辑器抽屉状态
  const [drawerOpen, setDrawerOpen] = useState<boolean>(false);
  const [editingSlug, setEditingSlug] = useState<string | null>(null);

  // 克隆弹窗状态
  const [cloneSource, setCloneSource] = useState<DocTypeSummaryVO | null>(null);
  const [cloneSlug, setCloneSlug] = useState<string>('');
  const [cloneDisplayName, setCloneDisplayName] = useState<string>('');
  const [cloning, setCloning] = useState<boolean>(false);

  const loadDocTypes = useCallback(async () => {
    if (!user?.is_module_admin) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const list = await listAdminDocTypes(false);
      setItems(list);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载文档类型列表失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [user?.is_module_admin, showToast]);

  useEffect(() => {
    void loadDocTypes();
  }, [loadDocTypes]);

  const handleOpenCreate = () => {
    setEditingSlug(null);
    setDrawerOpen(true);
  };

  const handleOpenEdit = (slug: string) => {
    setEditingSlug(slug);
    setDrawerOpen(true);
  };

  const handleOpenClone = (dt: DocTypeSummaryVO) => {
    setCloneSource(dt);
    setCloneSlug(`${dt.slug}-custom`);
    setCloneDisplayName(`${dt.display_name} (定制版)`);
  };

  const handleConfirmClone = async () => {
    if (!cloneSource) return;
    const trimmedSlug = cloneSlug.trim().toLowerCase();
    if (!trimmedSlug) {
      showToast('请输入新文档类型 slug', 'error');
      return;
    }
    setCloning(true);
    try {
      await cloneDocType(cloneSource.slug, {
        new_slug: trimmedSlug,
        display_name: cloneDisplayName.trim() || `${cloneSource.display_name} (副本)`,
      });
      showToast(`已成功克隆文档类型 "${trimmedSlug}"`, 'success');
      setCloneSource(null);
      await loadDocTypes();
    } catch (err: unknown) {
      showToast(formatApiError(err, '克隆文档类型失败').message, 'error');
    } finally {
      setCloning(false);
    }
  };

  const handleToggleStatus = async (dt: DocTypeSummaryVO) => {
    if (togglingSlug) return;
    const nextActive = !dt.is_active;
    setTogglingSlug(dt.slug);
    try {
      await toggleDocTypeStatus(dt.slug, { is_active: nextActive });
      showToast(
        nextActive
          ? `已启用文档类型 "${dt.display_name}"`
          : `已停用文档类型 "${dt.display_name}"（不影响已发布历史文档浏览）`,
        'success',
      );
      await loadDocTypes();
    } catch (err: unknown) {
      showToast(formatApiError(err, '切换启用状态失败').message, 'error');
    } finally {
      setTogglingSlug('');
    }
  };

  const handleDelete = async (dt: DocTypeSummaryVO) => {
    if (dt.published_doc_count > 0) {
      showToast(
        `文档类型 "${dt.slug}" 已被 ${dt.published_doc_count} 篇已发布文档引用，禁止物理删除，请改用「停用」开关`,
        'warning',
      );
      return;
    }

    const ok = await confirm({
      title: `确认物理删除文档类型 "${dt.slug}"？`,
      content:
        '该操作将同步删除服务端 doc_types/<slug>/ 目录与数据库索引，删除后无法恢复。',
      confirmText: '确认删除',
      cancelText: '取消',
      type: 'danger',
    });
    if (!ok) return;

    try {
      await deleteDocType(dt.slug);
      showToast(`已物理删除文档类型 "${dt.slug}"`, 'success');
      await loadDocTypes();
    } catch (err: unknown) {
      showToast(formatApiError(err, '删除文档类型失败').message, 'error');
    }
  };

  const handleSync = async () => {
    setSyncing(true);
    try {
      const res = await syncDocTypes();
      showToast(
        `目录同步完成：共扫描 ${res.synced_count} 个类型，新增 ${res.new_revisions} 个修订快照`,
        'success',
      );
      await loadDocTypes();
    } catch (err: unknown) {
      showToast(formatApiError(err, '目录重同步失败').message, 'error');
    } finally {
      setSyncing(false);
    }
  };

  if (!user?.is_module_admin) {
    return (
      <div className="code-archdoc-workspace">
        <EmptyState
          type="error"
          icon={<ShieldAlert size={40} />}
          title="仅模块管理员可访问 DocType 治理台"
          description="请使用具备 archdoc_admin 或 super_admin 角色的账号登录管理文档类型资产。"
        />
      </div>
    );
  }

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">文档类型管理 (DocType 注册表)</h1>
          <p className="code-archdoc-page-subtitle">
            服务端 <code>doc_types/&lt;slug&gt;/</code> 目录为唯一事实源：所有在线编辑均经原子写盘并自动生成不可变修订快照与 Spec Drift 通知。
          </p>
        </div>

        <div className="code-archdoc-page-actions">
          <button
            type="button"
            className="btn btn-secondary"
            disabled={syncing}
            onClick={() => void handleSync()}
          >
            {syncing ? (
              <Loader2 size={15} className="code-archdoc-spin" />
            ) : (
              <FolderSync size={15} />
            )}
            <span>{syncing ? '同步中...' : '从目录强制重同步'}</span>
          </button>
          <button type="button" className="btn btn-primary" onClick={handleOpenCreate}>
            <Plus size={15} />
            <span>新建 DocType</span>
          </button>
        </div>
      </div>

      <div className="code-card code-archdoc-doctype-card">
        {loading ? (
          <div className="code-archdoc-loading">
            <Loader2 size={18} className="code-archdoc-spin" />
            <span>正在加载文档类型注册表...</span>
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            icon={<FileText size={38} />}
            title="暂无已注册文档类型"
            description="点击「新建 DocType」或「从目录强制重同步」初始化文档类型资产。"
          />
        ) : (
          <div className="code-archdoc-table-wrap">
            <table className="code-archdoc-table">
              <thead>
                <tr>
                  <th>标识 (slug)</th>
                  <th>名称与定位</th>
                  <th>指定模型</th>
                  <th>槽位数</th>
                  <th>当前修订</th>
                  <th>已发布引用</th>
                  <th>启停状态</th>
                  <th>最近更新</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {items.map((dt) => {
                  const inUse = dt.published_doc_count > 0;
                  const isToggling = togglingSlug === dt.slug;
                  return (
                    <tr key={dt.slug}>
                      <td>
                        <div className="code-archdoc-doctype-slug-cell">
                          <code className="code-archdoc-mono">{dt.slug}</code>
                          {dt.is_builtin && (
                            <span className="code-badge code-badge--muted">内置</span>
                          )}
                        </div>
                      </td>
                      <td>
                        <div className="code-archdoc-doctype-slug-cell">
                          <span className="code-archdoc-doctype-name">{dt.display_name}</span>
                          {dt.category && (
                            <span className="code-badge code-badge--primary">{dt.category}</span>
                          )}
                          {dt.short_name && dt.short_name !== dt.display_name && (
                            <span className="code-badge code-badge--muted" title="紧凑短标题">
                              短名：{dt.short_name}
                            </span>
                          )}
                        </div>
                        {dt.description && (
                          <div className="code-archdoc-doctype-desc">{dt.description}</div>
                        )}
                      </td>
                      <td>
                        {dt.preferred_model ? (
                          <span className="code-badge code-badge--primary">
                            {dt.preferred_model}
                          </span>
                        ) : (
                          <span className="code-badge code-badge--muted">默认模型</span>
                        )}
                      </td>
                      <td>{dt.slot_count}</td>
                      <td>
                        <span className="code-badge code-badge--muted">
                          Rev v{dt.latest_revision_no}
                        </span>
                      </td>
                      <td>
                        <span
                          className={`code-badge ${
                            inUse ? 'code-badge--success' : 'code-badge--muted'
                          }`}
                        >
                          {dt.published_doc_count} 篇
                        </span>
                      </td>
                      <td>
                        <button
                          type="button"
                          className="code-archdoc-status-toggle-btn"
                          disabled={isToggling}
                          onClick={() => void handleToggleStatus(dt)}
                          title={dt.is_active ? '点击停用' : '点击启用'}
                        >
                          <StatusTag
                            status={dt.is_active ? 'success' : 'neutral'}
                            text={isToggling ? '切换中...' : dt.is_active ? '已启用' : '已停用'}
                            size="sm"
                          />
                        </button>
                      </td>
                      <td className="text-secondary">{formatDateTime(dt.updated_at)}</td>
                      <td>
                        <div className="code-archdoc-doctype-actions">
                          <button
                            type="button"
                            className="btn btn-secondary btn-small"
                            onClick={() => handleOpenEdit(dt.slug)}
                          >
                            <Edit3 size={13} />
                            <span>编辑</span>
                          </button>
                          <button
                            type="button"
                            className="btn btn-outline btn-small"
                            onClick={() => handleOpenClone(dt)}
                          >
                            <Copy size={13} />
                            <span>克隆</span>
                          </button>
                          <button
                            type="button"
                            className="btn btn-outline btn-small code-archdoc-btn-danger-outline"
                            disabled={inUse}
                            onClick={() => void handleDelete(dt)}
                            title={
                              inUse
                                ? `已被 ${dt.published_doc_count} 篇已发布文档引用，禁止物理删除（请改用停用）`
                                : '物理删除该文档类型目录与数据库索引'
                            }
                          >
                            <Trash2 size={13} />
                            <span>删除</span>
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* 在线新建 / 编辑抽屉 */}
      <DocTypeEditorDrawer
        open={drawerOpen}
        editingSlug={editingSlug}
        onClose={() => setDrawerOpen(false)}
        onSaved={() => void loadDocTypes()}
      />

      {/* 克隆 DocType 弹窗 */}
      <Modal
        open={cloneSource !== null}
        onClose={() => setCloneSource(null)}
        width="sm"
        title={`克隆文档类型：${cloneSource?.slug ?? ''}`}
        subtitle="完整复制元数据、槽位 Schema、模板与提示词资产为独立新类型"
        footer={
          <div className="code-archdoc-modal-footer">
            <button
              type="button"
              className="btn btn-secondary"
              disabled={cloning}
              onClick={() => setCloneSource(null)}
            >
              取消
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={cloning}
              onClick={() => void handleConfirmClone()}
            >
              {cloning ? <Loader2 size={14} className="code-archdoc-spin" /> : <Copy size={14} />}
              <span>{cloning ? '克隆中...' : '确认克隆'}</span>
            </button>
          </div>
        }
      >
        <div className="code-archdoc-clone-form">
          <div className="code-archdoc-form-field">
            <label className="code-archdoc-field-label">新目录标识 (new_slug)</label>
            <input
              type="text"
              className="code-archdoc-input code-archdoc-mono-input"
              value={cloneSlug}
              onChange={(e) => setCloneSlug(e.target.value.toLowerCase())}
              placeholder="例如: security-review-v2"
            />
          </div>
          <div className="code-archdoc-form-field">
            <label className="code-archdoc-field-label">新展示名称</label>
            <input
              type="text"
              className="code-archdoc-input"
              value={cloneDisplayName}
              onChange={(e) => setCloneDisplayName(e.target.value)}
              placeholder="例如: 安全架构评审报告 V2"
            />
          </div>
        </div>
      </Modal>
    </div>
  );
};

export default AdminDocTypesPage;
