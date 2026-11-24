import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import {
  AlertTriangle,
  ArrowLeft,
  BookOpen,
  Check,
  Edit3,
  FileCode2,
  History,
  Link2,
  Loader2,
  Maximize2,
  Minimize2,
  PanelRightClose,
  PanelRightOpen,
  RotateCcw,
  Save,
  Sparkles,
  Upload,
  X,
} from 'lucide-react';
import { EmptyState, StatusTag, useConfirm, useToast } from '@code/common';
import {
  getRefDocDetail,
  refineRefDoc,
  updateRefDoc,
  updateRefDocBinding,
} from '../api/refDocs';
import { listDocTypes } from '../api/repos';
import RefDocConflictMergeModal from '../components/refdoc/RefDocConflictMergeModal';
import RefDocRefinePanel, {
  type RefDocMemoryTurn,
} from '../components/refdoc/RefDocRefinePanel';
import RefDocSplitEditor from '../components/refdoc/RefDocSplitEditor';
import RefDocVersionHistoryPanel from '../components/refdoc/RefDocVersionHistoryPanel';
import SectionMarkdownRenderer from '../components/studio/SectionMarkdownRenderer';
import SelectionReviewPopover from '../components/studio/SelectionReviewPopover';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  DocTypeSummaryVO,
  RefDocBindingVO,
  RefDocKind,
  RefDocRefineMode,
  RefDocRefinePresetAction,
  RefDocRefineScopeType,
  ReferenceDocDetailVO,
  RefineRefDocSelectionPayload,
  UpdateRefDocPayload,
} from '../types';
import { formatApiError } from '../utils/errorMessages';
import {
  formatRefDocKindLabel,
  formatRefDocScopeLabel,
  formatRefDocSummaryStaleHint,
  formatRefDocTokenBadge,
} from '../utils/refDocUtils';
import {
  computeAbsoluteSelectionLines,
  estimateRefDocTokens,
  hasUnsavedRefDocDraft,
  mergeRefDocRefineProposal,
  parseLocalRefDocTOC,
  replaceLinesInDraft,
  splitRefDocIntoDisplaySections,
  type RefDocDisplaySection,
} from '../utils/refDocStudioUtils';
import { resolveSafeStudioBackTarget } from '../utils/urlQuery';
import './CanvasStudioPage.css';
import './RefDocStudioPage.css';

type RefDocRightTab = 'refine' | 'versions';

const DOC_KIND_OPTIONS: Array<{ value: RefDocKind; label: string }> = [
  { value: 'arch_design', label: '架构设计' },
  { value: 'domain_glossary', label: '领域术语' },
  { value: 'interface_spec', label: '接口契约' },
  { value: 'adr_constraint', label: 'ADR 约束' },
  { value: 'other', label: '其他参考' },
];

interface BindingDraftState {
  inherit_to_children: boolean;
  doc_type_slugs: string[];
}

function parseTermsInput(raw: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const part of raw.split(/[\n,，]+/)) {
    const t = part.trim();
    if (!t || seen.has(t)) continue;
    seen.add(t);
    out.push(t);
  }
  return out;
}

export const RefDocStudioPage: React.FC = () => {
  const { docId: docIdParam } = useParams<{ docId: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();
  const confirm = useConfirm();

  const docId = Number.parseInt(docIdParam ?? '', 10);
  const backTarget = useMemo(
    () => resolveSafeStudioBackTarget(location.state, isEmbedded),
    [location.state, isEmbedded],
  );

  const [doc, setDoc] = useState<ReferenceDocDetailVO | null>(null);
  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [loadError, setLoadError] = useState<string>('');

  // 本地可编辑草稿状态（元信息 + 正文）
  const [draftContent, setDraftContent] = useState<string>('');
  const [draftTitle, setDraftTitle] = useState<string>('');
  const [draftKind, setDraftKind] = useState<RefDocKind>('arch_design');
  const [draftDesc, setDraftDesc] = useState<string>('');
  const [draftSummary, setDraftSummary] = useState<string>('');
  const [draftTermsRaw, setDraftTermsRaw] = useState<string>('');

  const [editingSectionId, setEditingSectionId] = useState<string | null>(null);
  const [fullEditMode, setFullEditMode] = useState<boolean>(false);
  const [saving, setSaving] = useState<boolean>(false);
  const [restoringVersion, setRestoringVersion] = useState<boolean>(false);

  // 绑定策略本地编辑状态
  const [bindingDrafts, setBindingDrafts] = useState<Record<number, BindingDraftState>>({});
  const [savingBindingId, setSavingBindingId] = useState<number | null>(null);

  // 右栏状态与无状态 AI 精修内存会话
  const [drawerOpen, setDrawerOpen] = useState<boolean>(true);
  const [wideDrawer, setWideDrawer] = useState<boolean>(false);
  const [rightTab, setRightTab] = useState<RefDocRightTab>('refine');
  const [refineScope, setRefineScope] = useState<RefDocRefineScopeType>('full');
  const [refineMode, setRefineMode] = useState<RefDocRefineMode>('revise');
  const [activeSectionId, setActiveSectionId] = useState<string>('');
  const [selectionRange, setSelectionRange] = useState<RefineRefDocSelectionPayload | null>(null);
  const [refinePrompt, setRefinePrompt] = useState<string>('');
  const [refining, setRefining] = useState<boolean>(false);
  const [refineError, setRefineError] = useState<string>('');
  const [refineBudgetHint, setRefineBudgetHint] = useState<string>('');
  const [turns, setTurns] = useState<RefDocMemoryTurn[]>([]);

  // 划词浮层状态
  const [selPopover, setSelPopover] = useState<{
    section: RefDocDisplaySection;
    selection: RefineRefDocSelectionPayload;
    position: { left: number; top: number };
  } | null>(null);

  // 409 版本冲突弹窗状态
  const [conflictModal, setConflictModal] = useState<{
    open: boolean;
    serverVersion: number;
    serverContent: string;
    loadingServer: boolean;
    pendingPayload: UpdateRefDocPayload | null;
  }>({
    open: false,
    serverVersion: 0,
    serverContent: '',
    loadingServer: false,
    pendingPayload: null,
  });

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const sectionRefs = useRef<Record<string, HTMLElement | null>>({});

  const syncFormFromDoc = useCallback((loaded: ReferenceDocDetailVO) => {
    setDoc(loaded);
    setDraftContent(loaded.content_md);
    setDraftTitle(loaded.title);
    setDraftKind(loaded.doc_kind);
    setDraftDesc(loaded.description);
    setDraftSummary(loaded.summary_md);
    setDraftTermsRaw((loaded.declared_terms ?? []).join(', '));
    const bMap: Record<number, BindingDraftState> = {};
    for (const b of loaded.bindings ?? []) {
      bMap[b.id] = {
        inherit_to_children: b.inherit_to_children,
        doc_type_slugs: [...(b.doc_type_slugs ?? [])],
      };
    }
    setBindingDrafts(bMap);
    if (!loaded.can_edit) {
      setRefineMode('ask');
    }
  }, []);

  const fetchDoc = useCallback(async () => {
    if (!Number.isSafeInteger(docId) || docId <= 0) {
      setLoadError('非法的参考文档 ID');
      setLoading(false);
      return;
    }
    setLoading(true);
    setLoadError('');
    try {
      const [detail, types] = await Promise.all([
        getRefDocDetail(docId),
        listDocTypes(true).catch(() => [] as DocTypeSummaryVO[]),
      ]);
      syncFormFromDoc(detail);
      setDocTypes(types);
    } catch (err: unknown) {
      const f = formatApiError(err, '加载参考文档失败');
      setLoadError(`${f.title}：${f.message}`);
    } finally {
      setLoading(false);
    }
  }, [docId, syncFormFromDoc]);

  useEffect(() => {
    void fetchDoc();
  }, [fetchDoc]);

  const localTOC = useMemo(() => parseLocalRefDocTOC(draftContent), [draftContent]);
  const displaySections = useMemo(
    () => splitRefDocIntoDisplaySections(draftContent, localTOC),
    [draftContent, localTOC],
  );
  const draftEstimatedTokens = useMemo(() => estimateRefDocTokens(draftContent), [draftContent]);

  useEffect(() => {
    if (displaySections.length === 0) {
      setActiveSectionId('');
      return;
    }
    const stillExists = displaySections.some((s) => s.section_id === activeSectionId);
    if (!stillExists) {
      setActiveSectionId(displaySections[0].section_id);
    }
  }, [displaySections, activeSectionId]);

  const selectedSection = useMemo(
    () => displaySections.find((s) => s.section_id === activeSectionId) ?? null,
    [displaySections, activeSectionId],
  );

  const hasContentDirty = useMemo(
    () => Boolean(doc && hasUnsavedRefDocDraft(doc.content_md, draftContent)),
    [doc, draftContent],
  );

  const hasMetaDirty = useMemo(() => {
    if (!doc) return false;
    const origTerms = (doc.declared_terms ?? []).join(', ');
    return (
      draftTitle.trim() !== doc.title.trim() ||
      draftKind !== doc.doc_kind ||
      draftDesc.trim() !== doc.description.trim() ||
      draftSummary.trim() !== doc.summary_md.trim() ||
      parseTermsInput(draftTermsRaw).join(', ') !== parseTermsInput(origTerms).join(', ')
    );
  }, [doc, draftTitle, draftKind, draftDesc, draftSummary, draftTermsRaw]);

  const hasAnyDirty = hasContentDirty || hasMetaDirty;

  const scrollToSection = (sectionId: string) => {
    setActiveSectionId(sectionId);
    const el = sectionRefs.current[sectionId];
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  };

  const openConflictModal = useCallback(
    async (latestVersionHint: number | undefined, pendingPayload: UpdateRefDocPayload) => {
      if (!doc) return;
      setConflictModal({
        open: true,
        serverVersion: latestVersionHint ?? doc.current_version + 1,
        serverContent: '',
        loadingServer: true,
        pendingPayload,
      });
      try {
        const latest = await getRefDocDetail(doc.id);
        setConflictModal((prev) => ({
          ...prev,
          serverVersion: latest.current_version,
          serverContent: latest.content_md,
          loadingServer: false,
        }));
      } catch {
        setConflictModal((prev) => ({ ...prev, loadingServer: false }));
      }
    },
    [doc],
  );

  const executeSavePayload = useCallback(
    async (
      payload: UpdateRefDocPayload,
      options?: { successMessage?: string; preserveDraftAfterSummary?: boolean },
    ): Promise<ReferenceDocDetailVO | null> => {
      if (!doc) return null;
      setSaving(true);
      try {
        const updated = await updateRefDoc(doc.id, payload);
        if (options?.preserveDraftAfterSummary) {
          const prevDraft = draftContent;
          syncFormFromDoc(updated);
          if (hasUnsavedRefDocDraft(updated.content_md, prevDraft)) {
            setDraftContent(prevDraft);
          }
        } else {
          syncFormFromDoc(updated);
          setEditingSectionId(null);
          setFullEditMode(false);
        }
        showToast(options?.successMessage || `已保存参考文档 v${updated.current_version}`, 'success');
        return updated;
      } catch (err: unknown) {
        const f = formatApiError(err, '保存参考文档失败');
        if (f.code === 'SECRET_CONFIRM_REQUIRED' && !payload.redact_and_save) {
          const ok = await confirm({
            title: '检测到疑似敏感配置示例',
            content: `${f.message}\n\n是否自动将疑似敏感值脱敏为 [REDACTED] 后继续保存？`,
            confirmText: '自动脱敏并保存',
            cancelText: '取消',
            type: 'warning',
          });
          if (ok) {
            return executeSavePayload({ ...payload, redact_and_save: true }, options);
          }
          return null;
        }
        if (f.code === 'VERSION_CONFLICT') {
          await openConflictModal(f.currentVersion, payload);
          return null;
        }
        showToast(`${f.title}：${f.message}`, 'error');
        return null;
      } finally {
        setSaving(false);
      }
    },
    [doc, draftContent, syncFormFromDoc, showToast, confirm, openConflictModal],
  );

  const handleSaveAll = async () => {
    if (!doc || !doc.can_edit || saving) return;
    if (!draftTitle.trim()) {
      showToast('文档标题不能为空', 'error');
      return;
    }
    const payload: UpdateRefDocPayload = {
      expected_version: doc.current_version,
      title: draftTitle.trim(),
      doc_kind: draftKind,
      description: draftDesc.trim(),
      summary_md: draftSummary.trim(),
      declared_terms: parseTermsInput(draftTermsRaw),
      ...(hasContentDirty ? { content_md: draftContent } : {}),
    };
    await executeSavePayload(payload);
  };

  const handleDiscardDraft = async () => {
    if (!doc || !hasAnyDirty) return;
    const ok = await confirm({
      title: '放弃未保存的本地改动？',
      content: '将还原正文与元信息至当前已加载的服务端版本（右栏内存提案记录不受影响）。',
      confirmText: '确认放弃',
      cancelText: '取消',
      type: 'danger',
    });
    if (!ok) return;
    syncFormFromDoc(doc);
    setEditingSectionId(null);
    setFullEditMode(false);
    showToast('已还原至服务端版本', 'info');
  };

  const handleImportMarkdownFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      const text = typeof reader.result === 'string' ? reader.result : '';
      if (!text.trim()) {
        showToast('导入的 Markdown 文件内容为空', 'error');
        return;
      }
      setDraftContent(text.replace(/\r\n/g, '\n'));
      setEditingSectionId(null);
      setFullEditMode(true);
      showToast(`已载入 ${file.name} 至本地草稿，可预览微调后保存发布`, 'success');
    };
    reader.onerror = () => {
      showToast('读取本地 Markdown 文件失败', 'error');
    };
    reader.readAsText(file, 'utf-8');
  };

  const handleSaveBinding = async (binding: RefDocBindingVO) => {
    if (!doc) return;
    const bDraft = bindingDrafts[binding.id];
    if (!bDraft) return;
    setSavingBindingId(binding.id);
    try {
      const updatedBinding = await updateRefDocBinding(doc.id, binding.id, {
        inherit_to_children: bDraft.inherit_to_children,
        doc_type_slugs: bDraft.doc_type_slugs,
      });
      setDoc((prev) =>
        prev
          ? {
              ...prev,
              bindings: prev.bindings.map((item) =>
                item.id === updatedBinding.id ? updatedBinding : item,
              ),
            }
          : prev,
      );
      showToast(`已更新作用域「${updatedBinding.scope_label}」绑定策略`, 'success');
    } catch (err: unknown) {
      const f = formatApiError(err, '更新绑定策略失败');
      showToast(`${f.title}：${f.message}`, 'error');
    } finally {
      setSavingBindingId(null);
    }
  };

  const handleRunRefine = async (presetAction?: RefDocRefinePresetAction) => {
    if (!doc || refining) return;
    const effectiveMode: RefDocRefineMode = doc.can_edit ? refineMode : 'ask';
    const effectiveScope: RefDocRefineScopeType =
      presetAction === 'refresh_summary' ? 'full' : refineScope;

    if (effectiveScope === 'section' && !selectedSection) {
      setRefineError('请先在左侧大纲或中栏选择一个目标章节');
      return;
    }
    if (effectiveScope === 'selection' && !selectionRange) {
      setRefineError('请先在中栏正文中划词选中目标片段');
      return;
    }
    if (!presetAction && !refinePrompt.trim()) {
      setRefineError('请输入精修指令或点击上方快捷动作');
      return;
    }

    setRefining(true);
    setRefineError('');
    setRefineBudgetHint('');
    try {
      const resp = await refineRefDoc(doc.id, {
        scope_type: effectiveScope,
        mode: effectiveMode,
        preset_action: presetAction,
        prompt: refinePrompt.trim(),
        section_id: effectiveScope === 'section' ? selectedSection?.section_id : undefined,
        selection: effectiveScope === 'selection' ? (selectionRange ?? undefined) : undefined,
        draft_content_md: doc.can_edit ? draftContent : undefined,
      });

      const turnLabel =
        effectiveScope === 'selection' && selectionRange
          ? `L${selectionRange.start_line}-L${selectionRange.end_line}`
          : effectiveScope === 'section' && selectedSection
            ? selectedSection.title
            : '全文';

      const newTurn: RefDocMemoryTurn = {
        id: `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`,
        createdAt: new Date().toISOString(),
        scopeType: effectiveScope,
        mode: effectiveMode,
        presetAction,
        prompt: refinePrompt.trim() || `[快捷动作: ${presetAction}]`,
        sectionLabel: turnLabel,
        selection: effectiveScope === 'selection' ? (selectionRange ?? undefined) : undefined,
        response: resp,
        disposition: effectiveMode === 'ask' ? 'adopted' : 'pending',
      };
      setTurns((prev) => [newTurn, ...prev]);
      if (!presetAction) {
        setRefinePrompt('');
      }
    } catch (err: unknown) {
      const f = formatApiError(err, 'AI 精修请求失败');
      if (f.code === 'TOKEN_BUDGET_EXCEEDED') {
        setRefineBudgetHint(f.message);
      } else {
        setRefineError(`${f.title}：${f.message}`);
      }
    } finally {
      setRefining(false);
    }
  };

  const handleAdoptAndSave = async (
    turnId: string,
    options: { syncSummaryAndTerms: boolean; redactAndSave: boolean },
  ) => {
    if (!doc) return;
    const turn = turns.find((t) => t.id === turnId);
    if (!turn) return;
    const resp = turn.response;
    const nextContent = mergeRefDocRefineProposal(draftContent, resp);

    const payload: UpdateRefDocPayload = {
      expected_version: doc.current_version,
      content_md: nextContent,
      redact_and_save: options.redactAndSave,
      ...(options.syncSummaryAndTerms && resp.suggested_summary_md
        ? { summary_md: resp.suggested_summary_md }
        : {}),
      ...(options.syncSummaryAndTerms &&
      resp.suggested_declared_terms &&
      resp.suggested_declared_terms.length > 0
        ? { declared_terms: resp.suggested_declared_terms }
        : {}),
    };

    const updated = await executeSavePayload(payload, {
      successMessage: '已采纳提案并保存发布为新版本',
    });
    if (updated) {
      setTurns((prev) =>
        prev.map((t) => (t.id === turnId ? { ...t, disposition: 'adopted' } : t)),
      );
    }
  };

  const handleAdoptSummaryOnly = async (turnId: string) => {
    if (!doc) return;
    const turn = turns.find((t) => t.id === turnId);
    if (!turn) return;
    const resp = turn.response;
    const payload: UpdateRefDocPayload = {
      expected_version: doc.current_version,
      ...(resp.suggested_summary_md ? { summary_md: resp.suggested_summary_md } : {}),
      ...(resp.suggested_declared_terms && resp.suggested_declared_terms.length > 0
        ? { declared_terms: resp.suggested_declared_terms }
        : {}),
    };
    const updated = await executeSavePayload(payload, {
      successMessage: `已更新摘要与领域术语（正文版本保持 v${doc.current_version} 不变）`,
      preserveDraftAfterSummary: true,
    });
    if (updated) {
      setTurns((prev) =>
        prev.map((t) => (t.id === turnId ? { ...t, disposition: 'adopted' } : t)),
      );
    }
  };

  const handleApplyToDraft = (turnId: string) => {
    const turn = turns.find((t) => t.id === turnId);
    if (!turn) return;
    const merged = mergeRefDocRefineProposal(draftContent, turn.response);
    setDraftContent(merged);
    if (turn.response.suggested_summary_md) {
      setDraftSummary(turn.response.suggested_summary_md);
    }
    if (
      turn.response.suggested_declared_terms &&
      turn.response.suggested_declared_terms.length > 0
    ) {
      setDraftTermsRaw(turn.response.suggested_declared_terms.join(', '));
    }
    setTurns((prev) =>
      prev.map((t) => (t.id === turnId ? { ...t, disposition: 'applied_to_draft' } : t)),
    );
    showToast('已回填至中栏本地草稿，可继续微调后保存发布', 'info');
  };

  const handleRestoreVersion = async (versionNo: number) => {
    if (!doc || !doc.can_edit || restoringVersion) return;
    const ok = await confirm({
      title: `恢复 v${versionNo} 为最新版本？`,
      content: `采用不可变前滚语义：历史版本 v1..v${doc.current_version} 将完整保留，系统会以 v${versionNo} 的正文内容发布生成新的 v${doc.current_version + 1}。`,
      confirmText: `恢复并发布 v${doc.current_version + 1}`,
      cancelText: '取消',
      type: 'warning',
    });
    if (!ok) return;

    setRestoringVersion(true);
    try {
      const targetDetail = await getRefDocDetail(doc.id, versionNo);
      await executeSavePayload(
        {
          expected_version: doc.current_version,
          content_md: targetDetail.content_md,
        },
        {
          successMessage: `已从 v${versionNo} 不可变前滚恢复并发布为新版`,
        },
      );
    } catch (err: unknown) {
      const f = formatApiError(err, '恢复历史版本失败');
      showToast(`${f.title}：${f.message}`, 'error');
    } finally {
      setRestoringVersion(false);
    }
  };

  const handleSectionMouseUp = (
    sec: RefDocDisplaySection,
    e: React.MouseEvent<HTMLDivElement>,
  ) => {
    if (editingSectionId === sec.section_id || fullEditMode) return;
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) {
      setSelPopover(null);
      return;
    }
    const quoteText = sel.toString().trim();
    if (!quoteText) {
      setSelPopover(null);
      return;
    }
    const container = e.currentTarget;
    const range = sel.getRangeAt(0);
    const commonNode =
      range.commonAncestorContainer.nodeType === Node.ELEMENT_NODE
        ? (range.commonAncestorContainer as Element)
        : range.commonAncestorContainer.parentElement;
    if (!commonNode || !container.contains(commonNode)) {
      setSelPopover(null);
      return;
    }

    const blockEl = commonNode.closest<HTMLElement>('.code-archdoc-md-block');
    const relStart = blockEl?.dataset.lineStart
      ? Number.parseInt(blockEl.dataset.lineStart, 10)
      : undefined;
    const relEnd = blockEl?.dataset.lineEnd
      ? Number.parseInt(blockEl.dataset.lineEnd, 10)
      : undefined;

    const absLines = computeAbsoluteSelectionLines(
      sec.start_line,
      relStart,
      relEnd,
      sec.end_line,
    );

    const rect = range.getBoundingClientRect();
    const bodyRect = container.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.left - bodyRect.left, bodyRect.width - 280));
    const top = Math.max(4, rect.top - bodyRect.top - 42);

    setSelPopover({
      section: sec,
      selection: {
        start_line: absLines.startLine,
        end_line: absLines.endLine,
        quote_text: quoteText.slice(0, 500),
      },
      position: { left, top },
    });
  };

  if (loading) {
    return (
      <div className="code-archdoc-loading">
        <Loader2 size={20} className="code-archdoc-spin" />
        <span>正在加载架构参考文档精修工作台...</span>
      </div>
    );
  }

  if (loadError || !doc) {
    return (
      <div className="code-archdoc-studio">
        <div className="code-archdoc-studio-topbar">
          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={() => navigate(backTarget.path)}
          >
            <ArrowLeft size={14} />
            <span>返回{backTarget.label}</span>
          </button>
        </div>
        <EmptyState
          title="无法打开参考文档工作台"
          description={loadError || '目标参考文档不存在或无权访问'}
        />
      </div>
    );
  }

  const gridClass = [
    'code-archdoc-studio-grid',
    drawerOpen ? 'code-archdoc-studio-grid--with-drawer' : '',
    drawerOpen && wideDrawer ? 'code-archdoc-studio-grid--drawer-wide' : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div
      className={`code-archdoc-studio ${isEmbedded ? 'code-archdoc-studio--embedded' : ''}`}
    >
      {/* 顶部工作台工具栏 */}
      <header className="code-archdoc-studio-topbar">
        <div className="code-archdoc-studio-topbar-left">
          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={() => navigate(backTarget.path)}
            title={`返回${backTarget.label}`}
          >
            <ArrowLeft size={14} />
            <span>返回{backTarget.label}</span>
          </button>

          <div className="code-archdoc-studio-breadcrumb">
            <BookOpen size={15} />
            <span className="code-archdoc-studio-doc-name">{doc.title}</span>
            <span className="code-badge code-badge--muted">
              {formatRefDocKindLabel(doc.doc_kind)}
            </span>
            <span className="code-badge code-badge--info">{doc.owner_scope_label}</span>
            <span className="code-archdoc-mono">v{doc.current_version}</span>
            <span className="code-badge code-badge--muted">
              {formatRefDocTokenBadge(draftEstimatedTokens)}
            </span>
            {doc.status === 'archived' && <StatusTag status="warning" text="已归档" size="sm" />}
            {!doc.can_edit && <StatusTag status="neutral" text="只读浏览" size="sm" />}
            {doc.summary_stale && (
              <span
                className="code-badge code-badge--warning"
                title={formatRefDocSummaryStaleHint(doc.summary_for_version, doc.current_version)}
              >
                <AlertTriangle size={12} />
                <span>摘要滞后</span>
              </span>
            )}
          </div>
        </div>

        <div className="code-archdoc-studio-topbar-right">
          {doc.can_edit && (
            <>
              <input
                ref={fileInputRef}
                type="file"
                accept=".md,.markdown,text/markdown"
                className="code-archdoc-refstudio-hidden-input"
                onChange={handleImportMarkdownFile}
              />
              <button
                type="button"
                className="btn btn-secondary btn-small"
                disabled={saving}
                onClick={() => fileInputRef.current?.click()}
              >
                <Upload size={13} />
                <span>导入 .md</span>
              </button>

              <button
                type="button"
                className={`btn btn-small ${fullEditMode ? 'btn-primary' : 'btn-secondary'}`}
                disabled={saving}
                onClick={() => {
                  setEditingSectionId(null);
                  setFullEditMode((prev) => !prev);
                }}
              >
                <FileCode2 size={13} />
                <span>{fullEditMode ? '退出全文分栏' : '全文分栏编辑'}</span>
              </button>

              <button
                type="button"
                className="btn btn-primary btn-small"
                disabled={saving || !hasAnyDirty}
                onClick={() => void handleSaveAll()}
              >
                {saving ? <Loader2 size={13} className="code-archdoc-spin" /> : <Save size={13} />}
                <span>
                  {hasContentDirty
                    ? `保存发布 v${doc.current_version + 1}`
                    : '保存元信息'}
                </span>
              </button>
            </>
          )}

          <div className="code-archdoc-topbar-divider" />

          <div className="code-archdoc-view-switcher" role="tablist" aria-label="右侧工作台助手">
            <button
              type="button"
              className={`code-archdoc-view-pill ${
                drawerOpen && rightTab === 'refine' ? 'code-archdoc-view-pill--active' : ''
              }`}
              onClick={() => {
                setRightTab('refine');
                setDrawerOpen(true);
              }}
            >
              <Sparkles size={12} />
              <span>AI 精修提案</span>
              {turns.filter((t) => t.disposition === 'pending').length > 0 && (
                <span className="code-archdoc-view-pill-badge">
                  {turns.filter((t) => t.disposition === 'pending').length}
                </span>
              )}
            </button>
            <button
              type="button"
              className={`code-archdoc-view-pill ${
                drawerOpen && rightTab === 'versions' ? 'code-archdoc-view-pill--active' : ''
              }`}
              onClick={() => {
                setRightTab('versions');
                setDrawerOpen(true);
              }}
            >
              <History size={12} />
              <span>版本历史 Diff</span>
            </button>
          </div>

          <button
            type="button"
            className="btn btn-text btn-small code-archdoc-topbar-icon-btn"
            onClick={() => setDrawerOpen((prev) => !prev)}
            title={drawerOpen ? '收起右侧助手' : '展开右侧助手'}
            aria-label={drawerOpen ? '收起右侧助手' : '展开右侧助手'}
          >
            {drawerOpen ? <PanelRightClose size={15} /> : <PanelRightOpen size={15} />}
          </button>
        </div>
      </header>

      {/* 未保存草稿状态提示条 */}
      {doc.can_edit && hasAnyDirty && (
        <div className="code-archdoc-refstudio-draft-banner">
          <div className="code-archdoc-refstudio-draft-banner-left">
            <AlertTriangle size={14} />
            <span>
              <strong>未保存草稿 · 含本地改动</strong>
              {hasContentDirty ? '（正文已修改，保存后将原子发布新版本）' : '（仅元信息修改，版本号不变）'}
            </span>
          </div>
          <div className="code-archdoc-refstudio-draft-banner-actions">
            <button
              type="button"
              className="btn btn-secondary btn-small"
              disabled={saving}
              onClick={() => void handleDiscardDraft()}
            >
              <RotateCcw size={12} />
              <span>放弃改动</span>
            </button>
            <button
              type="button"
              className="btn btn-primary btn-small"
              disabled={saving}
              onClick={() => void handleSaveAll()}
            >
              <Check size={12} />
              <span>立即保存</span>
            </button>
          </div>
        </div>
      )}

      {/* 三栏主体 */}
      <div className={gridClass}>
        {/* 左栏：动态 TOC + 元信息 + 绑定策略 */}
        <aside className="code-archdoc-studio-left code-archdoc-refstudio-left">
          <section className="code-archdoc-studio-panel-section">
            <div className="code-archdoc-studio-panel-header">
              <span>动态章节大纲 ({displaySections.length})</span>
            </div>
            {displaySections.length === 0 ? (
              <div className="code-archdoc-studio-panel-empty">正文为空，暂未识别出章节</div>
            ) : (
              <ul className="code-archdoc-slot-toc">
                {displaySections.map((sec) => (
                  <li key={sec.section_id}>
                    <button
                      type="button"
                      className={`code-archdoc-slot-toc-item code-archdoc-refstudio-toc-btn ${
                        activeSectionId === sec.section_id
                          ? 'code-archdoc-slot-toc-item--active'
                          : ''
                      }`}
                      onClick={() => scrollToSection(sec.section_id)}
                      title={`${sec.title} (L${sec.start_line}-L${sec.end_line})`}
                    >
                      <span className="code-archdoc-refstudio-toc-title">{sec.title}</span>
                      <span className="code-archdoc-refstudio-toc-meta">
                        L{sec.start_line}-{sec.end_line} ·{' '}
                        {formatRefDocTokenBadge(estimateRefDocTokens(sec.markdown))}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="code-archdoc-studio-panel-section">
            <div className="code-archdoc-studio-panel-header">
              <span>参考文档元信息</span>
            </div>
            <div className="code-archdoc-refstudio-form-stack">
              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">标题</label>
                <input
                  type="text"
                  className="code-input"
                  value={draftTitle}
                  disabled={!doc.can_edit}
                  maxLength={200}
                  onChange={(e) => setDraftTitle(e.target.value)}
                />
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">文档分类</label>
                <select
                  className="code-select"
                  value={draftKind}
                  disabled={!doc.can_edit}
                  onChange={(e) => setDraftKind(e.target.value as RefDocKind)}
                >
                  {DOC_KIND_OPTIONS.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </select>
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">文档说明（≤500 字）</label>
                <textarea
                  rows={2}
                  className="code-textarea"
                  value={draftDesc}
                  disabled={!doc.can_edit}
                  maxLength={500}
                  onChange={(e) => setDraftDesc(e.target.value)}
                />
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">L0 摘要</label>
                {doc.summary_stale && (
                  <div className="code-archdoc-alert code-archdoc-alert--warning">
                    <AlertTriangle size={14} />
                    <span className="code-archdoc-alert-body">
                      {formatRefDocSummaryStaleHint(doc.summary_for_version, doc.current_version)}
                    </span>
                  </div>
                )}
                <textarea
                  rows={3}
                  className="code-textarea"
                  value={draftSummary}
                  disabled={!doc.can_edit}
                  onChange={(e) => setDraftSummary(e.target.value)}
                />
              </div>

              <div className="code-archdoc-field">
                <label className="code-archdoc-field-label">声明领域术语（逗号分隔）</label>
                <input
                  type="text"
                  className="code-input"
                  value={draftTermsRaw}
                  disabled={!doc.can_edit}
                  placeholder="OrderGateway, RiskCenter"
                  onChange={(e) => setDraftTermsRaw(e.target.value)}
                />
              </div>
            </div>
          </section>

          <section className="code-archdoc-studio-panel-section code-archdoc-studio-panel-section--grow">
            <div className="code-archdoc-studio-panel-header">
              <span>消费绑定与继承 ({doc.bindings.length})</span>
            </div>
            <div className="code-archdoc-refstudio-binding-list">
              {doc.bindings.map((b) => {
                const bDraft = bindingDrafts[b.id] ?? {
                  inherit_to_children: b.inherit_to_children,
                  doc_type_slugs: b.doc_type_slugs ?? [],
                };
                const isSystem = b.scope_level === 'system';
                const canManage = Boolean(b.can_manage_binding);
                return (
                  <div key={b.id} className="code-archdoc-refstudio-binding-card">
                    <div className="code-archdoc-refstudio-binding-head">
                      <span className="flex-center gap-xs">
                        <Link2 size={12} />
                        <strong>{b.scope_label}</strong>
                      </span>
                      <div className="flex-center gap-xs">
                        <span className="code-badge code-badge--muted">
                          {formatRefDocScopeLabel(b.scope_level)}
                        </span>
                        {b.is_home && <span className="code-badge code-badge--primary">主归属</span>}
                      </div>
                    </div>

                    <label className="code-archdoc-radio-label">
                      <input
                        type="checkbox"
                        checked={bDraft.inherit_to_children}
                        disabled={!canManage || isSystem}
                        onChange={(e) =>
                          setBindingDrafts((prev) => ({
                            ...prev,
                            [b.id]: { ...bDraft, inherit_to_children: e.target.checked },
                          }))
                        }
                      />
                      <span>{isSystem ? '全系统强制向下继承' : '向下继承至子层级'}</span>
                    </label>

                    {docTypes.length > 0 && (
                      <div className="code-archdoc-refstudio-doctype-grid">
                        {docTypes.map((dt) => {
                          const checked = bDraft.doc_type_slugs.includes(dt.slug);
                          return (
                            <label key={dt.slug} className="code-archdoc-refstudio-doctype-chip">
                              <input
                                type="checkbox"
                                checked={checked}
                                disabled={!canManage}
                                onChange={(e) => {
                                  const next = e.target.checked
                                    ? [...bDraft.doc_type_slugs, dt.slug]
                                    : bDraft.doc_type_slugs.filter((s) => s !== dt.slug);
                                  setBindingDrafts((prev) => ({
                                    ...prev,
                                    [b.id]: { ...bDraft, doc_type_slugs: next },
                                  }));
                                }}
                              />
                              <span>{dt.short_name || dt.display_name}</span>
                            </label>
                          );
                        })}
                      </div>
                    )}

                    {canManage && (
                      <button
                        type="button"
                        className="btn btn-secondary btn-small"
                        disabled={savingBindingId === b.id}
                        onClick={() => void handleSaveBinding(b)}
                      >
                        {savingBindingId === b.id ? (
                          <Loader2 size={12} className="code-archdoc-spin" />
                        ) : (
                          <Check size={12} />
                        )}
                        <span>保存绑定策略</span>
                      </button>
                    )}
                  </div>
                );
              })}
            </div>
          </section>
        </aside>

        {/* 中栏：分节画布 / 分栏实时编辑 / 划词交互 */}
        <main className="code-archdoc-refstudio-canvas-scroll">
          {fullEditMode ? (
            <section className="code-card code-archdoc-section-card">
              <div className="code-archdoc-section-header">
                <div className="code-archdoc-section-title-group">
                  <h2 className="code-archdoc-section-title">全文分栏编辑</h2>
                  <span className="code-badge code-badge--info">
                    {formatRefDocTokenBadge(draftEstimatedTokens)}
                  </span>
                </div>
              </div>
              <RefDocSplitEditor
                initialMarkdown={draftContent}
                title={`全文 v${doc.current_version} 草稿`}
                saveLabel="更新本地草稿"
                rows={24}
                onApply={(nextMD) => {
                  setDraftContent(nextMD.replace(/\r\n/g, '\n'));
                  setFullEditMode(false);
                  showToast('已更新本地全文草稿，请点击顶部保存发布', 'info');
                }}
                onCancel={() => setFullEditMode(false)}
              />
            </section>
          ) : displaySections.length === 0 ? (
            <EmptyState
              title="参考文档正文为空"
              description={doc.can_edit ? '点击右上角「全文分栏编辑」或「导入 .md」开始编写' : '暂无正文内容'}
            />
          ) : (
            displaySections.map((sec) => {
              const isEditingThis = editingSectionId === sec.section_id;
              return (
                <article
                  key={sec.section_id}
                  id={`refdoc-sec-${sec.section_id}`}
                  ref={(el) => {
                    sectionRefs.current[sec.section_id] = el;
                  }}
                  className="code-card code-archdoc-section-card code-archdoc-refstudio-section-card"
                >
                  <div className="code-archdoc-section-header">
                    <div className="code-archdoc-section-title-group">
                      <h2 className="code-archdoc-section-title">{sec.title}</h2>
                      <code className="code-archdoc-mono">{sec.section_id}</code>
                      <span className="code-badge code-badge--muted">
                        L{sec.start_line}-L{sec.end_line}
                      </span>
                      {sec.is_preamble && <span className="code-badge code-badge--info">导言</span>}
                      {sec.is_chunk && <span className="code-badge code-badge--muted">自动分段</span>}
                    </div>

                    <div className="flex-center gap-xs">
                      {doc.can_edit && !isEditingThis && (
                        <button
                          type="button"
                          className="btn btn-secondary btn-small"
                          onClick={() => {
                            setActiveSectionId(sec.section_id);
                            setEditingSectionId(sec.section_id);
                          }}
                        >
                          <Edit3 size={13} />
                          <span>编辑本节</span>
                        </button>
                      )}
                      <button
                        type="button"
                        className="btn btn-secondary btn-small"
                        onClick={() => {
                          setActiveSectionId(sec.section_id);
                          setRefineScope('section');
                          setRefineMode(doc.can_edit ? 'revise' : 'ask');
                          setRightTab('refine');
                          setDrawerOpen(true);
                        }}
                      >
                        <Sparkles size={13} />
                        <span>{doc.can_edit ? 'AI 精修本节' : '问 AI 本节'}</span>
                      </button>
                    </div>
                  </div>

                  {isEditingThis ? (
                    <RefDocSplitEditor
                      initialMarkdown={sec.markdown}
                      title={`${sec.section_id} (L${sec.start_line}-L${sec.end_line})`}
                      saveLabel="应用本节至草稿"
                      onApply={(nextFrag) => {
                        const nextFull = replaceLinesInDraft(
                          draftContent,
                          sec.start_line,
                          sec.end_line,
                          nextFrag,
                        );
                        setDraftContent(nextFull);
                        setEditingSectionId(null);
                        showToast(`已将章节「${sec.title}」写入本地草稿`, 'info');
                      }}
                      onCancel={() => setEditingSectionId(null)}
                    />
                  ) : (
                    <div
                      className="code-archdoc-refstudio-section-body"
                      onMouseUp={(e) => handleSectionMouseUp(sec, e)}
                    >
                      <SectionMarkdownRenderer markdown={sec.markdown} />

                      {selPopover && selPopover.section.section_id === sec.section_id && (
                        <SelectionReviewPopover
                          visible
                          position={selPopover.position}
                          quote={selPopover.selection.quote_text || ''}
                          canWrite={doc.can_edit}
                          showCommentButton={false}
                          canAsk
                          onQuickRefine={() => {
                            setActiveSectionId(sec.section_id);
                            setSelectionRange(selPopover.selection);
                            setRefineScope('selection');
                            setRefineMode('revise');
                            setRightTab('refine');
                            setDrawerOpen(true);
                            setSelPopover(null);
                          }}
                          onAskAI={() => {
                            setActiveSectionId(sec.section_id);
                            setSelectionRange(selPopover.selection);
                            setRefineScope('selection');
                            setRefineMode('ask');
                            setRightTab('refine');
                            setDrawerOpen(true);
                            setSelPopover(null);
                          }}
                          onClose={() => setSelPopover(null)}
                        />
                      )}
                    </div>
                  )}
                </article>
              );
            })
          )}
        </main>

        {/* 右栏：AI 精修提案 | 版本历史与 Diff */}
        {drawerOpen && (
          <aside className="code-archdoc-studio-right">
            <div className="code-archdoc-drawer-header">
              <div className="code-archdoc-drawer-title">
                {rightTab === 'refine' ? (
                  <>
                    <Sparkles size={14} />
                    <span>AI 精修提案（内存态）</span>
                  </>
                ) : (
                  <>
                    <History size={14} />
                    <span>版本历史与 Diff</span>
                  </>
                )}
              </div>
              <div className="flex-center gap-xs">
                <button
                  type="button"
                  className="btn btn-text btn-small code-archdoc-topbar-icon-btn"
                  onClick={() => setWideDrawer((prev) => !prev)}
                  title={wideDrawer ? '恢复默认宽度 (440px)' : '加宽右侧面板 (680px)'}
                >
                  {wideDrawer ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
                </button>
                <button
                  type="button"
                  className="btn btn-text btn-small code-archdoc-topbar-icon-btn"
                  onClick={() => setDrawerOpen(false)}
                  aria-label="关闭右侧面板"
                >
                  <X size={14} />
                </button>
              </div>
            </div>

            <div className="code-archdoc-drawer-body">
              {rightTab === 'refine' ? (
                <RefDocRefinePanel
                  canEdit={doc.can_edit}
                  scopeType={refineScope}
                  mode={doc.can_edit ? refineMode : 'ask'}
                  selectedSection={selectedSection}
                  selection={selectionRange}
                  prompt={refinePrompt}
                  submitting={refining}
                  savingAdoption={saving}
                  errorText={refineError}
                  budgetHint={refineBudgetHint}
                  turns={turns}
                  onChangeScopeType={setRefineScope}
                  onChangeMode={setRefineMode}
                  onChangePrompt={setRefinePrompt}
                  onClearSelection={() => {
                    setSelectionRange(null);
                    if (refineScope === 'selection') {
                      setRefineScope(selectedSection ? 'section' : 'full');
                    }
                  }}
                  onRunRefine={handleRunRefine}
                  onAdoptAndSave={handleAdoptAndSave}
                  onAdoptSummaryOnly={handleAdoptSummaryOnly}
                  onApplyToDraft={handleApplyToDraft}
                  onDismissTurn={(turnId) =>
                    setTurns((prev) =>
                      prev.map((t) => (t.id === turnId ? { ...t, disposition: 'dismissed' } : t)),
                    )
                  }
                  onClearTurns={() => setTurns([])}
                />
              ) : (
                <RefDocVersionHistoryPanel
                  docId={doc.id}
                  currentVersion={doc.current_version}
                  canEdit={doc.can_edit}
                  restoring={restoringVersion || saving}
                  onRestoreVersion={handleRestoreVersion}
                />
              )}
            </div>
          </aside>
        )}
      </div>

      {/* 409 VERSION_CONFLICT 合并冲突弹窗 */}
      <RefDocConflictMergeModal
        open={conflictModal.open}
        localVersion={doc.current_version}
        serverVersion={conflictModal.serverVersion}
        serverContent={conflictModal.serverContent}
        localDraftContent={draftContent}
        loadingServer={conflictModal.loadingServer}
        submittingOverwrite={saving}
        onOverwriteWithLatest={async () => {
          if (!conflictModal.pendingPayload) return;
          const retryPayload: UpdateRefDocPayload = {
            ...conflictModal.pendingPayload,
            expected_version: conflictModal.serverVersion,
          };
          setConflictModal((prev) => ({ ...prev, open: false }));
          await executeSavePayload(retryPayload, {
            successMessage: `已基于最新版本号覆盖发布为 v${conflictModal.serverVersion + 1}`,
          });
        }}
        onDiscardLocalAndLoadLatest={() => {
          setConflictModal((prev) => ({ ...prev, open: false }));
          void fetchDoc();
        }}
        onClose={() => setConflictModal((prev) => ({ ...prev, open: false }))}
      />
    </div>
  );
};

export default RefDocStudioPage;
