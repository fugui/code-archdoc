import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { AlertTriangle, BookOpen, Loader2, Sparkles } from 'lucide-react';
import { EmptyState, useConfirm, useToast } from '@code/common';
import {
  exportDocumentMarkdown,
  getDocument,
  listDocuments,
  publishDocument,
  resolveOrphanSection,
  unpublishDocument,
  updateDocumentSection,
  updateSectionOwnership,
} from '../api/documents';
import { listReviewComments } from '../api/reviewComments';
import {
  getRepoScopes,
  listDocTypes,
  listRepoModules,
  refreshRepoModules,
} from '../api/repos';
import GenerateTaskModal from '../components/GenerateTaskModal';
import LeftDocTreePanel from '../components/studio/LeftDocTreePanel';
import OrphanSectionsZone from '../components/studio/OrphanSectionsZone';
import RightUtilityDrawer, { type RightDrawerTab } from '../components/studio/RightUtilityDrawer';
import type { ReviewDraftContext } from '../components/studio/ReviewBasketPanel';
import SectionCard from '../components/studio/SectionCard';
import StudioTopBar from '../components/studio/StudioTopBar';
import VerifierBanner from '../components/studio/VerifierBanner';
import type { EvidenceInspectRequest } from '../components/studio/EvidenceSourceTab';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  ChatIntentMode,
  DocTypeSummaryVO,
  DocumentDetailVO,
  DocumentNavItemVO,
  EvidenceAnchor,
  OrphanResolveAction,
  RepoModuleVO,
  ReviewCommentAnchor,
  ReviewCommentVO,
  SectionOwnership,
  SectionVO,
} from '../types';
import { formatApiError } from '../utils/errorMessages';
import {
  buildStudioPath,
  parseEvidenceDeepLinkParams,
  shortSha,
  type EvidenceDeepLinkParams,
} from '../utils/urlQuery';
import './CanvasStudioPage.css';

export interface CanvasStudioPageProps {
  /** 是否由 /docs/:docId/evidence 门户深链路由进入 */
  evidenceDeepLink?: boolean;
}

export const CanvasStudioPage: React.FC<CanvasStudioPageProps> = ({
  evidenceDeepLink = false,
}) => {
  const navigate = useNavigate();
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();
  const confirm = useConfirm();

  const { repoId: rawRepoId, docId: rawDocId } = useParams<{
    repoId?: string;
    docId?: string;
  }>();
  const [searchParams] = useSearchParams();

  const paramRepoId = rawRepoId ? Number.parseInt(rawRepoId, 10) : 0;
  const paramDocId = rawDocId ? Number.parseInt(rawDocId, 10) : 0;
  const urlSubdir = (searchParams.get('subdir') ?? '').trim();

  const [docList, setDocList] = useState<DocumentNavItemVO[]>([]);
  const [scopeCanWrite, setScopeCanWrite] = useState<boolean>(false);
  const [doc, setDoc] = useState<DocumentDetailVO | null>(null);
  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [loadError, setLoadError] = useState<{ title: string; message: string } | null>(null);
  const [activeSlotId, setActiveSlotId] = useState<string>('');

  // 右侧辅助抽屉状态（源码证据 / 修订历史 / 审阅与精修）
  const [drawerOpen, setDrawerOpen] = useState<boolean>(evidenceDeepLink);
  const [wideDrawer, setWideDrawer] = useState<boolean>(false);
  const [drawerTab, setDrawerTab] = useState<RightDrawerTab>('evidence');
  const [evidenceRequest, setEvidenceRequest] = useState<EvidenceInspectRequest | null>(null);
  const [chatTargetSlotId, setChatTargetSlotId] = useState<string>('');
  const [chatSubTab, setChatSubTab] = useState<'basket' | 'chat'>('chat');
  const [chatMode, setChatMode] = useState<ChatIntentMode>('revise');
  const [chatInlineAnchor, setChatInlineAnchor] = useState<{
    slotId: string;
    anchor: ReviewCommentAnchor;
    nonce: number;
  } | null>(null);
  const [reviewComments, setReviewComments] = useState<ReviewCommentVO[]>([]);
  const [activeCommentId, setActiveCommentId] = useState<number | null>(null);
  const [reviewDraftContext, setReviewDraftContext] = useState<ReviewDraftContext | null>(null);
  const [hybridPromptSlots, setHybridPromptSlots] = useState<SectionVO[]>([]);

  // 操作态与仓库模块范围上下文
  const [publishing, setPublishing] = useState<boolean>(false);
  const [unpublishing, setUnpublishing] = useState<boolean>(false);
  const [exporting, setExporting] = useState<boolean>(false);
  const [genModalOpen, setGenModalOpen] = useState<boolean>(false);
  const [repoModules, setRepoModules] = useState<RepoModuleVO[]>([]);
  const [loadingModules, setLoadingModules] = useState<boolean>(false);
  const [refreshingModules, setRefreshingModules] = useState<boolean>(false);
  const [repoMeta, setRepoMeta] = useState<{
    repoName: string;
    branch: string;
    canWriteRepo: boolean;
  } | null>(null);

  const effectiveRepoId = doc?.repo_id || paramRepoId;
  const effectiveSubdir = doc ? doc.subdirectory : urlSubdir;

  // 拉取仓库模块索引与元信息（支持整仓 <-> 一级目录自由切换）
  useEffect(() => {
    if (!effectiveRepoId) {
      setRepoModules([]);
      setRepoMeta(null);
      return;
    }
    let active = true;
    setLoadingModules(true);
    void Promise.allSettled([
      listRepoModules(effectiveRepoId),
      getRepoScopes(effectiveRepoId),
    ])
      .then(([modsRes, scopesRes]) => {
        if (!active) return;
        if (modsRes.status === 'fulfilled') {
          setRepoModules(
            (modsRes.value.modules || []).filter((m) => m.status !== 'ignored'),
          );
        }
        if (scopesRes.status === 'fulfilled') {
          const scopesData = scopesRes.value;
          const rootScope = scopesData.scopes.find((s) => s.scope_type === 'repo');
          setRepoMeta({
            repoName: scopesData.repo_name,
            branch: scopesData.branch || 'master',
            canWriteRepo: Boolean(rootScope?.can_write),
          });
        }
      })
      .finally(() => {
        if (active) setLoadingModules(false);
      });
    return () => {
      active = false;
    };
  }, [effectiveRepoId]);

  // 拉取启用中的 DocType（供重新生成弹窗使用）
  useEffect(() => {
    let active = true;
    void listDocTypes(true)
      .then((list) => {
        if (active) setDocTypes(list);
      })
      .catch(() => {
        // 仅用于生成弹窗，失败不阻断画布阅读
      });
    return () => {
      active = false;
    };
  }, []);

  // 入口一：门户深链 /docs/:docId/evidence?commit=...&file=...&symbol=...&lines=142-188
  useEffect(() => {
    if (!evidenceDeepLink || !paramDocId) return;
    const parsed = parseEvidenceDeepLinkParams(searchParams);
    setDrawerOpen(true);
    setDrawerTab('evidence');
    if (parsed.file) {
      setEvidenceRequest({
        docId: paramDocId,
        commit: parsed.commit,
        file: parsed.file,
        symbol: parsed.symbol,
        lineStart: parsed.lineStart,
        lineEnd: parsed.lineEnd,
        scope: parsed.scope,
      });
    }
  }, [evidenceDeepLink, paramDocId, searchParams]);

  // 加载文档与同范围导航树
  const loadStudioData = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    setActiveSlotId('');
    try {
      // 场景 A：已指定 docId（包括 /studio/:repoId/docs/:docId 与 /docs/:docId/evidence 深链）
      if (paramDocId > 0) {
        const detail = await getDocument(paramDocId);
        setDoc(detail);
        setScopeCanWrite(detail.can_write);
        if (detail.sections.length > 0) {
          setActiveSlotId((prev) => prev || detail.sections[0].slot_id);
        }

        const nav = await listDocuments(detail.repo_id, detail.subdirectory, detail.ref_name);
        setDocList(nav.items);
        setScopeCanWrite(nav.can_write);
        return;
      }

      // 场景 B：仅指定 repoId（/studio/:repoId?subdir=...），加载列表后自动打开首篇文档
      if (paramRepoId > 0) {
        const nav = await listDocuments(paramRepoId, urlSubdir);
        setDocList(nav.items);
        setScopeCanWrite(nav.can_write);

        if (nav.items.length > 0) {
          const detail = await getDocument(nav.items[0].id);
          setDoc(detail);
          if (detail.sections.length > 0) {
            setActiveSlotId(detail.sections[0].slot_id);
          }
        } else {
          setDoc(null);
        }
        return;
      }

      setLoadError({
        title: '路由参数缺失',
        message: '未指定有效的代码仓库或文档编号，请从仓库覆盖矩阵重新进入。',
      });
    } catch (err: unknown) {
      const formatted = formatApiError(err, '加载工作台文档失败');
      setLoadError({ title: formatted.title, message: formatted.message });
      setDoc(null);
    } finally {
      setLoading(false);
    }
  }, [paramDocId, paramRepoId, urlSubdir]);

  useEffect(() => {
    void loadStudioData();
  }, [loadStudioData]);

  const loadReviewComments = useCallback(async () => {
    if (!doc || !doc.can_write) {
      setReviewComments([]);
      return;
    }
    try {
      const items = await listReviewComments(doc.id);
      setReviewComments(items);
    } catch {
      // 批注拉取失败不阻断画布核心阅读
    }
  }, [doc]);

  useEffect(() => {
    void loadReviewComments();
  }, [loadReviewComments]);

  const commentsBySlot = useMemo(() => {
    const map = new Map<string, ReviewCommentVO[]>();
    for (const c of reviewComments) {
      if (!c.slot_id) continue;
      const arr = map.get(c.slot_id) ?? [];
      arr.push(c);
      map.set(c.slot_id, arr);
    }
    return map;
  }, [reviewComments]);

  const pendingReviewCount = useMemo(
    () => reviewComments.filter((c) => c.status === 'open').length,
    [reviewComments],
  );

  const regularSections = useMemo(
    () => (doc ? doc.sections.filter((s) => !s.is_orphan) : []),
    [doc],
  );
  const orphanSections = useMemo(
    () => (doc ? doc.sections.filter((s) => s.is_orphan) : []),
    [doc],
  );

  const handleBackToRepos = () => {
    navigate(isEmbedded ? '/archdoc/repos' : '/repos');
  };

  const handleSelectSubdirectory = useCallback(
    (targetSubdir: string) => {
      if (!effectiveRepoId || targetSubdir === effectiveSubdir) return;
      navigate(buildStudioPath(isEmbedded, effectiveRepoId, { subdirectory: targetSubdir }));
    },
    [effectiveRepoId, effectiveSubdir, isEmbedded, navigate],
  );

  const handleRefreshStudioModules = useCallback(async () => {
    if (!effectiveRepoId) return;
    setRefreshingModules(true);
    try {
      const res = await refreshRepoModules(effectiveRepoId);
      setRepoModules((res.modules || []).filter((m) => m.status !== 'ignored'));
      if (res.active_count > 0) {
        showToast(
          `已完成静态扫描（${shortSha(res.commit_sha)}），发现 ${res.active_count} 个一级目录模块`,
          'success',
        );
      } else {
        showToast(
          `已完成静态扫描（${shortSha(res.commit_sha)}），未发现独立一级业务子目录`,
          'info',
        );
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '扫描仓库一级目录失败').message, 'error');
    } finally {
      setRefreshingModules(false);
    }
  }, [effectiveRepoId, showToast]);

  const handleSelectDoc = (targetDocId: number) => {
    if (!effectiveRepoId) return;
    navigate(
      buildStudioPath(isEmbedded, effectiveRepoId, {
        docId: targetDocId,
        subdirectory: effectiveSubdir,
      }),
    );
  };

  const handleSelectSlot = (slotId: string) => {
    setActiveSlotId(slotId);
    const el = document.getElementById(`section-${slotId}`);
    if (el && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  };

  const handleInspectEvidence = (anchor: EvidenceAnchor) => {
    if (!doc) return;
    setEvidenceRequest({
      docId: doc.id,
      commit: anchor.commit_sha || doc.viewing_revision.commit_sha,
      file: anchor.file_path,
      symbol: anchor.fq_symbol,
      lineStart: anchor.line_start,
      lineEnd: anchor.line_end,
      confidence: anchor.confidence,
      scope: anchor.scope === 'repo' ? 'repo' : undefined,
    });
    setDrawerTab('evidence');
    setDrawerOpen(true);
  };

  const handleInspectEvidenceLink = (params: { docId: number } & EvidenceDeepLinkParams) => {
    if (!params.file) return;
    setEvidenceRequest({
      docId: params.docId,
      commit: params.commit || doc?.viewing_revision.commit_sha,
      file: params.file,
      symbol: params.symbol,
      lineStart: params.lineStart,
      lineEnd: params.lineEnd,
      scope: params.scope,
    });
    setDrawerTab('evidence');
    setDrawerOpen(true);
  };

  const handleSaveSection = async (
    slotId: string,
    payload: { markdown: string; ownership: SectionOwnership; changeSummary: string },
  ) => {
    if (!doc) return;
    const updated = await updateDocumentSection(doc.id, slotId, {
      base_revision_no: doc.current_revision_no,
      markdown: payload.markdown,
      ownership: payload.ownership,
      change_summary: payload.changeSummary,
    });
    setDoc(updated);
    showToast(`章节已保存并生成新修订 v${updated.current_revision_no}`, 'success');
    // 同步刷新左侧文档导航状态
    const nav = await listDocuments(updated.repo_id, updated.subdirectory, updated.ref_name);
    setDocList(nav.items);
  };

  const handleChangeOwnership = async (
    slotId: string,
    ownership: SectionOwnership,
    clearDriftReview = false,
  ) => {
    if (!doc) return;
    try {
      const updatedSec = await updateSectionOwnership(doc.id, slotId, {
        ownership,
        clear_drift_review: clearDriftReview,
      });
      setDoc((prev) => {
        if (!prev) return prev;
        return {
          ...prev,
          sections: prev.sections.map((s) => (s.slot_id === slotId ? updatedSec : s)),
        };
      });
      showToast(
        clearDriftReview
          ? '已清除待核对标记'
          : `章节所有权已切换为 ${ownership}`,
        'success',
      );
    } catch (err: unknown) {
      showToast(formatApiError(err, '更新章节所有权失败').message, 'error');
    }
  };

  const handleResolveOrphan = async (
    slotId: string,
    action: OrphanResolveAction,
    targetSlotId?: string,
  ) => {
    if (!doc) return;
    try {
      const updated = await resolveOrphanSection(doc.id, slotId, {
        base_revision_no: doc.current_revision_no,
        action,
        target_slot_id: targetSlotId,
      });
      setDoc(updated);
      showToast(`孤立章节已处置并生成新修订 v${updated.current_revision_no}`, 'success');
    } catch (err: unknown) {
      const formatted = formatApiError(err, '处置孤立章节失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    }
  };

  const handlePublish = async () => {
    if (!doc) return;
    const ok = await confirm({
      title: '确认审批发布定稿文档',
      content: `即将发布《${doc.title}》修订 v${doc.viewing_revision.revision_no} 并原子落盘至发布目录，所有登录成员及文档中心均可只读浏览。是否继续？`,
      confirmText: '确认发布',
      cancelText: '取消',
      type: 'info',
    });
    if (!ok) return;

    setPublishing(true);
    try {
      const res = await publishDocument(doc.id, {
        revision_no: doc.viewing_revision.revision_no,
      });
      showToast(
        `发布成功（v${res.published_revision_no} -> ${res.published_rel_path}）`,
        'success',
      );
      await loadStudioData();
    } catch (err: unknown) {
      const formatted = formatApiError(err, '审批发布失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setPublishing(false);
    }
  };

  const handleUnpublish = async () => {
    if (!doc) return;
    const ok = await confirm({
      title: '确认撤回已发布文档',
      content: `撤回后《${doc.title}》将退回草稿态并从发布目录移除，普通成员及文档中心将不再可见。是否继续？`,
      confirmText: '确认撤回',
      cancelText: '取消',
      type: 'warning',
    });
    if (!ok) return;

    setUnpublishing(true);
    try {
      await unpublishDocument(doc.id);
      showToast('已撤回发布并清理落盘文件', 'success');
      await loadStudioData();
    } catch (err: unknown) {
      showToast(formatApiError(err, '撤回发布失败').message, 'error');
    } finally {
      setUnpublishing(false);
    }
  };

  const handleExportMarkdown = async () => {
    if (!doc) return;
    setExporting(true);
    try {
      const filename = await exportDocumentMarkdown(
        doc.id,
        `${doc.repo_name}-${doc.doc_type_slug}.md`,
      );
      showToast(`已导出文档：${filename}`, 'success');
    } catch (err: unknown) {
      showToast(formatApiError(err, '导出 Markdown 失败').message, 'error');
    } finally {
      setExporting(false);
    }
  };

  const handleOpenChatForReview = (
    slotId: string,
    opts?: {
      subTab?: 'basket' | 'chat';
      mode?: ChatIntentMode;
      anchor?: ReviewCommentAnchor;
    },
  ) => {
    setActiveSlotId(slotId);
    setChatTargetSlotId(slotId);
    if (opts?.subTab) setChatSubTab(opts.subTab);
    if (opts?.mode) setChatMode(opts.mode);
    if (opts?.anchor) {
      setChatInlineAnchor({ slotId, anchor: opts.anchor, nonce: Date.now() });
    }
    setDrawerTab('chat');
    setDrawerOpen(true);
  };

  const handleAnnotateBlock = (slotId: string, anchor: ReviewCommentAnchor) => {
    setReviewDraftContext({
      scopeType: 'inline',
      slotId,
      anchor,
      nonce: Date.now(),
    });
    handleOpenChatForReview(slotId, { subTab: 'basket' });
  };

  const handleSelectReviewComment = (comment: ReviewCommentVO) => {
    setActiveCommentId(comment.id);
    if (comment.slot_id) {
      handleSelectSlot(comment.slot_id);
    }
    setDrawerTab('chat');
    setChatSubTab('basket');
    setDrawerOpen(true);
  };

  const handleSelectionAction = (
    action: 'comment' | 'refine' | 'ask',
    slotId: string,
    anchor: ReviewCommentAnchor,
  ) => {
    if (action === 'comment') {
      handleAnnotateBlock(slotId, anchor);
      return;
    }
    handleOpenChatForReview(slotId, {
      subTab: 'chat',
      mode: action === 'ask' ? 'ask' : 'revise',
      anchor,
    });
  };

  const handleAppliedProposal = (appliedSlots: string[], commentIds: number[]) => {
    if (!doc || appliedSlots.length === 0 || commentIds.length === 0) return;
    const appliedCommentSet = new Set(commentIds);
    const hasSupplement = reviewComments.some(
      (c) => appliedCommentSet.has(c.id) && c.category === 'supplement',
    );
    if (!hasSupplement) return;
    const autoSections = doc.sections.filter(
      (s) => appliedSlots.includes(s.slot_id) && s.ownership === 'auto',
    );
    if (autoSections.length > 0) {
      setHybridPromptSlots(autoSections);
    }
  };

  return (
    <div className={`code-archdoc-studio ${isEmbedded ? 'code-archdoc-studio--embedded' : ''}`}>
      <StudioTopBar
        doc={doc}
        repoId={effectiveRepoId}
        repoName={doc?.repo_name || repoMeta?.repoName}
        subdirectory={effectiveSubdir}
        drawerOpen={drawerOpen}
        pendingReviewCount={pendingReviewCount}
        publishing={publishing}
        unpublishing={unpublishing}
        exporting={exporting}
        onBackToRepos={handleBackToRepos}
        onToggleDrawer={() => setDrawerOpen((prev) => !prev)}
        onOpenHistory={() => {
          setDrawerTab('history');
          setDrawerOpen(true);
        }}
        onOpenReviewBasket={() => {
          setChatSubTab('basket');
          setDrawerTab('chat');
          setDrawerOpen(true);
        }}
        onOpenChatRefine={() => {
          if (activeSlotId) {
            setChatTargetSlotId(activeSlotId);
          }
          setChatSubTab('chat');
          setDrawerTab('chat');
          setDrawerOpen(true);
        }}
        onRegenerate={() => setGenModalOpen(true)}
        onPublish={() => void handlePublish()}
        onUnpublish={() => void handleUnpublish()}
        onExportMarkdown={() => void handleExportMarkdown()}
      />

      <div
        className={`code-archdoc-studio-grid ${
          drawerOpen
            ? `code-archdoc-studio-grid--with-drawer ${
                wideDrawer ? 'code-archdoc-studio-grid--drawer-wide' : ''
              }`
            : ''
        }`}
      >
        <LeftDocTreePanel
          docs={docList}
          activeDocId={doc?.id ?? null}
          sections={doc?.sections ?? []}
          activeSlotId={activeSlotId}
          canWrite={scopeCanWrite}
          repoModules={repoModules}
          activeSubdirectory={effectiveSubdir}
          loadingModules={loadingModules}
          refreshingModules={refreshingModules}
          canManageModules={Boolean(repoMeta?.canWriteRepo)}
          onSelectSubdirectory={handleSelectSubdirectory}
          onRefreshModules={() => void handleRefreshStudioModules()}
          onSelectDoc={handleSelectDoc}
          onSelectSlot={handleSelectSlot}
          onTriggerGenerate={() => setGenModalOpen(true)}
        />

        <main className="code-archdoc-studio-center">
          {loading ? (
            <div className="code-archdoc-loading">
              <Loader2 size={18} className="code-archdoc-spin" />
              <span>正在加载 Canvas Studio 文档画布...</span>
            </div>
          ) : loadError ? (
            <div className="code-archdoc-canvas-inner">
              <EmptyState
                type="error"
                icon={<AlertTriangle size={40} />}
                title={loadError.title}
                description={loadError.message}
                action={
                  <button
                    type="button"
                    className="btn btn-secondary btn-small"
                    onClick={handleBackToRepos}
                  >
                    返回仓库覆盖矩阵
                  </button>
                }
              />
            </div>
          ) : !doc ? (
            <div className="code-archdoc-canvas-inner">
              <EmptyState
                icon={<BookOpen size={40} />}
                title="当前范围尚未生成设计文档"
                description={
                  scopeCanWrite
                    ? '点击下方按钮触发 AI 静态抽取与多槽位设计文档生成。'
                    : '暂无已发布文档，请联系该仓库或子目录负责人生成并审批发布。'
                }
                action={
                  scopeCanWrite ? (
                    <button
                      type="button"
                      className="btn btn-primary"
                      onClick={() => setGenModalOpen(true)}
                    >
                      <Sparkles size={15} />
                      <span>立即触发生成</span>
                    </button>
                  ) : undefined
                }
              />
            </div>
          ) : (
            <div className="code-archdoc-canvas-inner">
              {hybridPromptSlots.length > 0 && doc.can_write && (
                <div className="code-archdoc-review-hybrid-banner">
                  <span>
                    已通过“知识补充”批注应用至「{hybridPromptSlots.map((s) => s.title).join('、')}」，建议将所有权切换为 <strong>人机协同 (hybrid)</strong> 以防止后续重生成覆盖补充内容。
                  </span>
                  <div className="flex-center gap-xs">
                    <button
                      type="button"
                      className="btn btn-primary btn-small"
                      onClick={() => {
                        const targets = hybridPromptSlots;
                        setHybridPromptSlots([]);
                        void Promise.all(
                          targets.map((s) => handleChangeOwnership(s.slot_id, 'hybrid')),
                        );
                      }}
                    >
                      一键切换为 hybrid
                    </button>
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => setHybridPromptSlots([])}
                    >
                      暂不切换
                    </button>
                  </div>
                </div>
              )}

              <VerifierBanner
                report={doc.viewing_revision.verifier_report}
                onJumpToSlot={handleSelectSlot}
              />

              {regularSections.map((section) => (
                <SectionCard
                  key={section.slot_id}
                  section={section}
                  baseRevisionNo={doc.current_revision_no}
                  canWrite={doc.can_write}
                  reviewComments={commentsBySlot.get(section.slot_id) ?? []}
                  activeCommentId={activeCommentId}
                  onSaveSection={handleSaveSection}
                  onChangeOwnership={handleChangeOwnership}
                  onReloadDocument={loadStudioData}
                  onInspectEvidence={handleInspectEvidence}
                  onInspectEvidenceLink={handleInspectEvidenceLink}
                  onOpenChatRefine={(slotId) =>
                    handleOpenChatForReview(slotId, { subTab: 'chat', mode: 'revise' })
                  }
                  onAnnotateBlock={handleAnnotateBlock}
                  onSelectReviewComment={handleSelectReviewComment}
                  onSelectionAction={handleSelectionAction}
                />
              ))}

              <OrphanSectionsZone
                orphanSections={orphanSections}
                regularSections={regularSections}
                canWrite={doc.can_write}
                onResolveOrphan={handleResolveOrphan}
              />
            </div>
          )}
        </main>

        {drawerOpen && (
          <RightUtilityDrawer
            activeTab={drawerTab}
            doc={doc}
            evidenceRequest={evidenceRequest}
            chatTargetSlotId={chatTargetSlotId}
            chatSubTab={chatSubTab}
            chatMode={chatMode}
            chatInlineAnchor={chatInlineAnchor}
            reviewComments={reviewComments}
            activeCommentId={activeCommentId}
            reviewDraftContext={reviewDraftContext}
            wideDrawer={wideDrawer}
            onToggleWide={() => setWideDrawer((w) => !w)}
            onChangeTab={setDrawerTab}
            onChangeChatSubTab={setChatSubTab}
            onClose={() => setDrawerOpen(false)}
            onRolledBack={(updatedDoc) => setDoc(updatedDoc)}
            onReloadDocument={loadStudioData}
            onCommentsChanged={loadReviewComments}
            onSelectCommentOnCanvas={handleSelectReviewComment}
            onClearDraftContext={() => setReviewDraftContext(null)}
            onAppliedProposal={handleAppliedProposal}
          />
        )}
      </div>

      {/* 重新生成 / 新增文档类型弹窗 */}
      <GenerateTaskModal
        open={genModalOpen}
        scope={
          effectiveRepoId > 0
            ? {
                repoId: effectiveRepoId,
                repoName: doc?.repo_name || repoMeta?.repoName || `仓库 #${effectiveRepoId}`,
                subdirectory: effectiveSubdir,
                branch: doc?.ref_name || repoMeta?.branch || 'master',
                canWrite: scopeCanWrite,
              }
            : null
        }
        docTypes={docTypes}
        initialDocTypeSlugs={doc ? [doc.doc_type_slug] : undefined}
        initialMode={doc ? 'regenerate' : 'generate'}
        onClose={() => setGenModalOpen(false)}
        onTaskCompleted={() => void loadStudioData()}
        onOpenStudio={(rId, dId, sub) => {
          navigate(buildStudioPath(isEmbedded, rId, { docId: dId, subdirectory: sub }));
        }}
      />
    </div>
  );
};

export default CanvasStudioPage;
