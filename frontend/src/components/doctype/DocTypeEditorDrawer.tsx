import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Code2,
  Eye,
  FileText,
  History,
  Layers,
  Loader2,
  Maximize2,
  Minimize2,
  Plus,
  Save,
  Settings2,
  XCircle,
} from 'lucide-react';
import { Drawer, useConfirm, useToast } from '@code/common';
import type { DrawerWidthPreset } from '@code/common';
import {
  createDocType,
  getDocTypeDetail,
  listDocTypeRevisions,
  listLLMConfigs,
  updateDocType,
} from '../../api/admin';
import type {
  CanonicalFacetName,
  ContextInputName,
  DocTypeDetailVO,
  DocTypeMeta,
  DocTypeRevisionSummaryVO,
  FacetName,
  LLMConfigVO,
  ModuleDigestSourceConfig,
  ScopeProfileConfig,
  SlotDefinition,
  VerifyPolicyConfig,
} from '../../types';
import {
  compileFromBlueprint,
  computeCanonicalFacetsUnion,
  createDefaultBlueprint,
  decompileToBlueprint,
  detectUnconfirmedMissingSlotIds,
  nextBlueprintKey,
  parseDSLToBlueprint,
  serializeBlueprintToDSL,
  shouldDefaultAutoFacetsUnion,
  type BlueprintDiagnostic,
  type DocBlueprint,
} from '../../utils/docTypeBlueprint';
import { formatApiError } from '../../utils/errorMessages';
import { formatDateTime } from '../../utils/urlQuery';
import BlueprintSourceEditor from './BlueprintSourceEditor';
import BlueprintVisualEditor from './BlueprintVisualEditor';
import DocTypePolicyPanel from './DocTypePolicyPanel';

export interface DocTypeEditorDrawerProps {
  open: boolean;
  /** null 表示新建模式，非空字符串表示编辑该 slug */
  editingSlug: string | null;
  onClose: () => void;
  onSaved: () => void;
}

type EditorMainTab = 'blueprint' | 'policy' | 'revisions';
type BlueprintViewMode = 'visual' | 'source' | 'preview';

const SLUG_REGEX = /^[a-z0-9][a-z0-9_-]{1,62}$/;
const SLOT_ID_REGEX = /^[a-z][a-z0-9_]{1,48}$/;

const DEFAULT_GENERATION_PROMPT =
  '你是资深企业级软件架构师。请严格基于 L1 Code IR 事实撰写清晰、准确、零幻觉的技术设计文档。';

const DEFAULT_VERIFY_POLICY: VerifyPolicyConfig = {
  strict_symbol_check: false,
  require_mermaid: false,
  max_mermaid_nodes: 35,
  max_mermaid_edges: 60,
  block_on_secret_leak: true,
};

const DEFAULT_SCOPE_PROFILE: ScopeProfileConfig = {
  max_depth: 12,
  include_tests: false,
  include_legacy_docs: false,
  include_globs: [],
  exclude_globs: [],
};

const DEFAULT_MODULE_DIGEST_SOURCE: ModuleDigestSourceConfig = {
  doc_type_slug: '',
  slot_id: 'overview_summary',
  max_chars: 600,
};

export const DocTypeEditorDrawer: React.FC<DocTypeEditorDrawerProps> = ({
  open,
  editingSlug,
  onClose,
  onSaved,
}) => {
  const { showToast } = useToast();
  const confirm = useConfirm();
  const isCreate = editingSlug === null;

  const [loading, setLoading] = useState<boolean>(false);
  const [saving, setSaving] = useState<boolean>(false);
  const [drawerWidth, setDrawerWidth] = useState<DrawerWidthPreset>('xl');
  const [mainTab, setMainTab] = useState<EditorMainTab>('blueprint');
  const [blueprintMode, setBlueprintMode] = useState<BlueprintViewMode>('visual');

  // 全局指令面板折叠状态
  const [genPromptOpen, setGenPromptOpen] = useState<boolean>(true);
  const [refinePromptOpen, setRefinePromptOpen] = useState<boolean>(false);

  // 基础信息与运行参数
  const [slug, setSlug] = useState<string>('');
  const [displayName, setDisplayName] = useState<string>('');
  const [shortName, setShortName] = useState<string>('');
  const [category, setCategory] = useState<string>('');
  const [description, setDescription] = useState<string>('');
  const [preferredModel, setPreferredModel] = useState<string>('');
  const [llmConfigs, setLLMConfigs] = useState<LLMConfigVO[]>([]);
  const [timeoutSeconds, setTimeoutSeconds] = useState<number>(300);
  const [maxOutputTokens, setMaxOutputTokens] = useState<string>('');
  const [isActive, setIsActive] = useState<boolean>(true);

  // 顶层 ir_facets 与高级治理策略
  const [autoAggregateFacets, setAutoAggregateFacets] = useState<boolean>(true);
  const [manualTopFacets, setManualTopFacets] = useState<FacetName[]>(['tree', 'deps']);
  const [repoIRFacets, setRepoIRFacets] = useState<CanonicalFacetName[]>([]);
  const [contextInputs, setContextInputs] = useState<ContextInputName[]>([]);
  const [moduleDigestSource, setModuleDigestSource] = useState<ModuleDigestSourceConfig>(
    DEFAULT_MODULE_DIGEST_SOURCE,
  );
  const [verifyPolicy, setVerifyPolicy] = useState<VerifyPolicyConfig>(DEFAULT_VERIFY_POLICY);
  const [scopeProfile, setScopeProfile] = useState<ScopeProfileConfig>(DEFAULT_SCOPE_PROFILE);

  // 统一蓝图 IR 与参数化源码状态
  const [blueprint, setBlueprint] = useState<DocBlueprint>(() => createDefaultBlueprint(''));
  const [sourceDraft, setSourceDraft] = useState<string>('');
  const [sourceDiagnostics, setSourceDiagnostics] = useState<BlueprintDiagnostic[]>([]);
  const [decompileWarnings, setDecompileWarnings] = useState<BlueprintDiagnostic[]>([]);
  const [genPromptMD, setGenPromptMD] = useState<string>(DEFAULT_GENERATION_PROMPT);
  const [refinePromptMD, setRefinePromptMD] = useState<string>('');

  // 编辑态原数据、历史修订与跨模式防误改确认集合
  const [loadedDetail, setLoadedDetail] = useState<DocTypeDetailVO | null>(null);
  const [revisions, setRevisions] = useState<DocTypeRevisionSummaryVO[]>([]);
  const [loadingRevs, setLoadingRevs] = useState<boolean>(false);
  const [confirmedSlotChanges, setConfirmedSlotChanges] = useState<Set<string>>(new Set());

  const leadH1TouchedRef = useRef<boolean>(false);

  const persistedSlotIds = useMemo(() => {
    if (!loadedDetail) return new Set<string>();
    return new Set(loadedDetail.slots.map((s) => s.slot_id));
  }, [loadedDetail]);

  const compiledArtifacts = useMemo(() => compileFromBlueprint(blueprint), [blueprint]);
  const currentSlots = compiledArtifacts.slots;

  const autoUnionFacets = useMemo(
    () => computeCanonicalFacetsUnion(currentSlots),
    [currentSlots],
  );

  const totalBudget = useMemo(
    () => currentSlots.reduce((sum, sl) => sum + (sl.token_budget || 0), 0),
    [currentSlots],
  );

  const hasSourceErrors = useMemo(
    () =>
      blueprintMode === 'source' &&
      sourceDiagnostics.some((d) => d.severity === 'error'),
    [blueprintMode, sourceDiagnostics],
  );

  const unconfirmedMissingIds = useMemo(
    () =>
      detectUnconfirmedMissingSlotIds(
        persistedSlotIds,
        currentSlots,
        confirmedSlotChanges,
      ),
    [persistedSlotIds, currentSlots, confirmedSlotChanges],
  );

  const currentSlotIdSet = useMemo(
    () => new Set(currentSlots.map((s) => s.slot_id).filter(Boolean)),
    [currentSlots],
  );

  // 抽屉打开时拉取最新平等模型池列表用于指定可选模型
  useEffect(() => {
    if (!open) return;
    let active = true;
    void listLLMConfigs()
      .then((items) => {
        if (active) setLLMConfigs(items);
      })
      .catch(() => {
        if (active) setLLMConfigs([]);
      });
    return () => {
      active = false;
    };
  }, [open]);

  // 初始化加载或重置新建状态
  useEffect(() => {
    if (!open) return;

    setMainTab(isCreate ? 'policy' : 'blueprint');
    setBlueprintMode('visual');
    setGenPromptOpen(true);
    setRefinePromptOpen(false);
    setConfirmedSlotChanges(new Set());
    setSourceDiagnostics([]);
    setDecompileWarnings([]);
    leadH1TouchedRef.current = false;

    if (isCreate) {
      const initialBp = createDefaultBlueprint('');
      setLoadedDetail(null);
      setRevisions([]);
      setSlug('');
      setDisplayName('');
      setShortName('');
      setCategory('');
      setDescription('');
      setPreferredModel('');
      setTimeoutSeconds(300);
      setMaxOutputTokens('');
      setIsActive(true);
      setAutoAggregateFacets(true);
      setManualTopFacets(['tree', 'deps']);
      setRepoIRFacets([]);
      setContextInputs([]);
      setModuleDigestSource({ ...DEFAULT_MODULE_DIGEST_SOURCE });
      setVerifyPolicy({ ...DEFAULT_VERIFY_POLICY });
      setScopeProfile({ ...DEFAULT_SCOPE_PROFILE, include_globs: [], exclude_globs: [] });
      setBlueprint(initialBp);
      setSourceDraft(serializeBlueprintToDSL(initialBp));
      setGenPromptMD(DEFAULT_GENERATION_PROMPT);
      setRefinePromptMD('');
      return;
    }

    let active = true;
    setLoading(true);
    void getDocTypeDetail(editingSlug)
      .then((detail) => {
        if (!active) return;
        setLoadedDetail(detail);
        setSlug(detail.slug);
        setDisplayName(detail.display_name);
        setShortName(detail.short_name || '');
        setCategory(detail.category || '');
        setDescription(detail.description || '');
        setPreferredModel(detail.preferred_model || '');
        setTimeoutSeconds(detail.timeout_seconds || 300);

        const meta = detail.meta ?? {};
        const rawMaxOut = meta.max_output_tokens;
        setMaxOutputTokens(
          typeof rawMaxOut === 'number' && rawMaxOut > 0 ? String(rawMaxOut) : '',
        );
        setIsActive(detail.is_active);

        const loadedSlots = Array.isArray(detail.slots)
          ? detail.slots.map((s) => ({ ...s, ir_facets: [...(s.ir_facets || [])] }))
          : [];
        const { blueprint: decompiledBp, diagnostics } = decompileToBlueprint(
          detail.template_md || '',
          loadedSlots,
        );
        setBlueprint(decompiledBp);
        setSourceDraft(serializeBlueprintToDSL(decompiledBp));
        setDecompileWarnings(diagnostics);

        const rawTopFacets = Array.isArray(detail.ir_facets) ? detail.ir_facets : [];
        const shouldAuto = shouldDefaultAutoFacetsUnion(rawTopFacets, loadedSlots);
        setAutoAggregateFacets(shouldAuto);
        setManualTopFacets(
          rawTopFacets.length > 0 ? [...rawTopFacets] : computeCanonicalFacetsUnion(loadedSlots),
        );

        setRepoIRFacets(
          Array.isArray(meta.repo_ir_facets) ? [...meta.repo_ir_facets] : [],
        );
        setContextInputs(
          Array.isArray(meta.context_inputs) ? [...meta.context_inputs] : [],
        );
        if (meta.module_digest_source) {
          setModuleDigestSource({
            doc_type_slug: meta.module_digest_source.doc_type_slug || detail.slug,
            slot_id: meta.module_digest_source.slot_id || loadedSlots[0]?.slot_id || '',
            max_chars: meta.module_digest_source.max_chars ?? 600,
            ...(meta.module_digest_source.fallback_order
              ? { fallback_order: [...meta.module_digest_source.fallback_order] }
              : {}),
          });
        } else {
          setModuleDigestSource({
            doc_type_slug: detail.slug,
            slot_id: loadedSlots[0]?.slot_id || 'overview_summary',
            max_chars: 600,
          });
        }

        setVerifyPolicy({
          strict_symbol_check: Boolean(meta.verify_policy?.strict_symbol_check),
          require_mermaid: Boolean(meta.verify_policy?.require_mermaid),
          allowed_mermaid_types: meta.verify_policy?.allowed_mermaid_types
            ? [...meta.verify_policy.allowed_mermaid_types]
            : undefined,
          max_mermaid_nodes: meta.verify_policy?.max_mermaid_nodes ?? 35,
          max_mermaid_edges: meta.verify_policy?.max_mermaid_edges ?? 60,
          block_on_secret_leak: meta.verify_policy?.block_on_secret_leak !== false,
        });

        setScopeProfile({
          max_depth: meta.scope_profile?.max_depth ?? 12,
          include_tests: Boolean(meta.scope_profile?.include_tests),
          include_legacy_docs: Boolean(meta.scope_profile?.include_legacy_docs),
          include_globs: Array.isArray(meta.scope_profile?.include_globs)
            ? [...meta.scope_profile.include_globs]
            : [],
          exclude_globs: Array.isArray(meta.scope_profile?.exclude_globs)
            ? [...meta.scope_profile.exclude_globs]
            : [],
        });

        setGenPromptMD(detail.generation_prompt_md || '');
        setRefinePromptMD(detail.refine_prompt_md || '');
      })
      .catch((err: unknown) => {
        if (!active) return;
        showToast(formatApiError(err, '加载 DocType 详情失败').message, 'error');
        onClose();
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [open, editingSlug, isCreate, showToast, onClose]);

  // 加载修订历史
  useEffect(() => {
    if (!open || isCreate || mainTab !== 'revisions' || !editingSlug) return;
    let active = true;
    setLoadingRevs(true);
    void listDocTypeRevisions(editingSlug)
      .then((items) => {
        if (active) setRevisions(items);
      })
      .catch((err: unknown) => {
        if (active) showToast(formatApiError(err, '加载修订历史失败').message, 'error');
      })
      .finally(() => {
        if (active) setLoadingRevs(false);
      });
    return () => {
      active = false;
    };
  }, [open, isCreate, mainTab, editingSlug, showToast]);

  // 200ms 防抖解析参数化源码 DSL v1
  useEffect(() => {
    if (blueprintMode !== 'source') return;
    const timer = window.setTimeout(() => {
      const parsed = parseDSLToBlueprint(sourceDraft);
      setSourceDiagnostics(parsed.diagnostics);
      if (parsed.blueprint) {
        setBlueprint(parsed.blueprint);
      }
    }, 200);
    return () => window.clearTimeout(timer);
  }, [sourceDraft, blueprintMode]);

  const handleDisplayNameChange = (nextName: string) => {
    setDisplayName(nextName);
    if (isCreate && !leadH1TouchedRef.current && blueprintMode !== 'source') {
      setBlueprint((prev) => {
        const first = prev.blocks[0];
        if (!first || first.kind !== 'static' || !first.markdown.trimStart().startsWith('# ')) {
          return prev;
        }
        const nextTitleLine = `# ${nextName.trim() || '新文档类型'}`;
        const lines = first.markdown.split('\n');
        lines[0] = nextTitleLine;
        const nextBlocks = [...prev.blocks];
        nextBlocks[0] = { ...first, markdown: lines.join('\n') };
        return { blocks: nextBlocks };
      });
    }
  };

  const confirmMissingPersistedSlots = useCallback(
    async (missingIds: string[]): Promise<boolean> => {
      if (missingIds.length === 0) return true;
      const ok = await confirm({
        title: `确认变更或移除 ${missingIds.length} 个既有槽位标识？`,
        content: `检测到以下已落盘槽位在当前蓝图中缺失或被重命名：${missingIds.join('、')}。这会导致已发布文档触发 Spec Drift 或将锁定章节移入「孤立章节待处置区」。确认继续吗？`,
        confirmText: '确认风险，继续操作',
        cancelText: '返回检查',
        type: 'warning',
      });
      if (!ok) return false;
      setConfirmedSlotChanges((prev) => {
        const next = new Set(prev);
        for (const id of missingIds) {
          next.add(id);
        }
        return next;
      });
      return true;
    },
    [confirm],
  );

  const handleSwitchBlueprintMode = async (targetMode: BlueprintViewMode) => {
    if (targetMode === blueprintMode) return;

    if (blueprintMode === 'source') {
      const parsed = parseDSLToBlueprint(sourceDraft);
      setSourceDiagnostics(parsed.diagnostics);
      if (!parsed.blueprint) {
        showToast('参数化源码存在语法错误，请先修复后再切换视图', 'error');
        return;
      }
      const nextSlots = compileFromBlueprint(parsed.blueprint).slots;
      const missingIds = detectUnconfirmedMissingSlotIds(
        persistedSlotIds,
        nextSlots,
        confirmedSlotChanges,
      );
      if (missingIds.length > 0) {
        const ok = await confirmMissingPersistedSlots(missingIds);
        if (!ok) return;
      }
      setBlueprint(parsed.blueprint);
    }

    if (targetMode === 'source') {
      setSourceDraft(serializeBlueprintToDSL(blueprint));
      setSourceDiagnostics([]);
    }
    setBlueprintMode(targetMode);
  };

  const handleFormatSource = () => {
    const parsed = parseDSLToBlueprint(sourceDraft);
    setSourceDiagnostics(parsed.diagnostics);
    if (!parsed.blueprint) {
      showToast('源码存在语法错误，无法格式化', 'error');
      return;
    }
    const formatted = serializeBlueprintToDSL(parsed.blueprint);
    setBlueprint(parsed.blueprint);
    setSourceDraft(formatted);
    showToast('已规范化 DSL 源码格式', 'success');
  };

  const handleAddVisualSlot = () => {
    let idx = currentSlots.length + 1;
    let candidate = `custom_section_${idx}`;
    while (currentSlotIdSet.has(candidate)) {
      idx += 1;
      candidate = `custom_section_${idx}`;
    }
    setBlueprint((prev) => ({
      blocks: [
        ...prev.blocks,
        {
          kind: 'slot',
          key: nextBlueprintKey('slot'),
          emitHeading: true,
          headingLevel: 2,
          slot: {
            slot_id: candidate,
            title: `${idx}. 新章节标题`,
            ownership: 'auto',
            required: true,
            ir_facets: ['tree', 'deps'],
            token_budget: 8000,
            min_chars: 40,
            instruction_hint: '',
          },
        },
      ],
    }));
  };

  const handleAddStaticBlock = () => {
    setBlueprint((prev) => ({
      blocks: [
        ...prev.blocks,
        {
          kind: 'static',
          key: nextBlueprintKey('static'),
          markdown: '> 固定说明段落（原文保留于 template.md，不经 AI 覆盖）',
        },
      ],
    }));
  };

  const validateBeforeSave = (
    effectiveSlots: SlotDefinition[],
    effectiveTemplateMD: string,
    effectiveTopFacets: FacetName[],
  ): string | null => {
    const trimmedSlug = slug.trim();
    if (isCreate && !SLUG_REGEX.test(trimmedSlug)) {
      return 'slug 格式非法：仅允许小写字母、数字、中划线和下划线，长度 2~63 位且以字母或数字开头';
    }
    if (!displayName.trim()) {
      return '展示名称 (display_name) 不能为空';
    }
    if (Array.from(shortName.trim()).length > 32) {
      return '短标题 (short_name) 长度不能超过 32 个字符';
    }
    if (Array.from(category.trim()).length > 32) {
      return '文档分组 (category) 长度不能超过 32 个字符';
    }
    if (effectiveTopFacets.length === 0) {
      return '请至少订阅 1 个 Code IR 分片（在任意章节槽位勾选分片，或切换为手动模式勾选）';
    }
    if (effectiveSlots.length === 0) {
      return '至少需要定义 1 个章节槽位';
    }
    const seen = new Set<string>();
    for (const sl of effectiveSlots) {
      if (!SLOT_ID_REGEX.test(sl.slot_id)) {
        return `槽位标识 "${sl.slot_id}" 非法：须匹配 ^[a-z][a-z0-9_]{1,48}$`;
      }
      if (seen.has(sl.slot_id)) {
        return `存在重复的槽位标识 "${sl.slot_id}"`;
      }
      seen.add(sl.slot_id);
      if (!sl.title.trim()) {
        return `槽位 "${sl.slot_id}" 缺少章节标题`;
      }
      if (!effectiveTemplateMD.includes(`<!-- slot:${sl.slot_id} -->`)) {
        return `编译产物缺少槽位标记 <!-- slot:${sl.slot_id} -->`;
      }
    }
    return null;
  };

  const handleSave = async () => {
    let effectiveBlueprint = blueprint;

    if (blueprintMode === 'source') {
      const parsed = parseDSLToBlueprint(sourceDraft);
      setSourceDiagnostics(parsed.diagnostics);
      if (!parsed.blueprint) {
        showToast('参数化源码存在语法错误，请先修复后再保存', 'error');
        return;
      }
      effectiveBlueprint = parsed.blueprint;
      setBlueprint(parsed.blueprint);
    }

    const compiled = compileFromBlueprint(effectiveBlueprint);
    const missingIds = detectUnconfirmedMissingSlotIds(
      persistedSlotIds,
      compiled.slots,
      confirmedSlotChanges,
    );
    if (missingIds.length > 0) {
      const ok = await confirmMissingPersistedSlots(missingIds);
      if (!ok) return;
    }

    const effectiveTopFacets: FacetName[] = autoAggregateFacets
      ? computeCanonicalFacetsUnion(compiled.slots)
      : manualTopFacets;

    const errMsg = validateBeforeSave(compiled.slots, compiled.templateMD, effectiveTopFacets);
    if (errMsg) {
      showToast(errMsg, 'error');
      return;
    }

    const parsedMaxOut = maxOutputTokens.trim()
      ? Number.parseInt(maxOutputTokens.trim(), 10)
      : undefined;

    const cleanScopeProfile: ScopeProfileConfig = {
      max_depth: scopeProfile.max_depth || 12,
      include_tests: scopeProfile.include_tests,
      include_legacy_docs: Boolean(scopeProfile.include_legacy_docs),
      ...(scopeProfile.include_globs && scopeProfile.include_globs.length > 0
        ? { include_globs: scopeProfile.include_globs }
        : {}),
      ...(scopeProfile.exclude_globs && scopeProfile.exclude_globs.length > 0
        ? { exclude_globs: scopeProfile.exclude_globs }
        : {}),
    };

    const hasModuleDigests = contextInputs.includes('module_digests');
    const validDigestSource =
      hasModuleDigests &&
      moduleDigestSource.doc_type_slug.trim() &&
      moduleDigestSource.slot_id.trim()
        ? {
            doc_type_slug: moduleDigestSource.doc_type_slug.trim(),
            slot_id: moduleDigestSource.slot_id.trim(),
            max_chars: moduleDigestSource.max_chars || 600,
            ...(moduleDigestSource.fallback_order
              ? { fallback_order: moduleDigestSource.fallback_order }
              : {}),
          }
        : undefined;

    setSaving(true);
    try {
      if (isCreate) {
        await createDocType({
          slug: slug.trim(),
          display_name: displayName.trim(),
          ...(shortName.trim() ? { short_name: shortName.trim() } : {}),
          ...(category.trim() ? { category: category.trim() } : {}),
          description: description.trim(),
          is_active: isActive,
          ...(preferredModel.trim() ? { preferred_model: preferredModel.trim() } : {}),
          timeout_seconds: timeoutSeconds,
          ...(parsedMaxOut && parsedMaxOut > 0 ? { max_output_tokens: parsedMaxOut } : {}),
          ir_facets: effectiveTopFacets,
          ...(repoIRFacets.length > 0 ? { repo_ir_facets: repoIRFacets } : {}),
          ...(contextInputs.length > 0 ? { context_inputs: contextInputs } : {}),
          ...(validDigestSource ? { module_digest_source: validDigestSource } : {}),
          scope_profile: cleanScopeProfile,
          verify_policy: verifyPolicy,
          slots: compiled.slots,
          template_md: compiled.templateMD,
          generation_prompt_md: genPromptMD,
          refine_prompt_md: refinePromptMD,
        });
        showToast(`文档类型 "${slug.trim()}" 已创建并原子落盘`, 'success');
      } else {
        // 以原 meta 为基底合并更新，无损保留未知扩展字段 (ExtraFields)
        const baseMeta: DocTypeMeta = {
          ...(loadedDetail?.meta ?? {}),
          name: editingSlug,
          display_name: displayName.trim(),
          description: description.trim(),
          is_active: isActive,
          timeout_seconds: timeoutSeconds,
          ir_facets: effectiveTopFacets,
          scope_profile: cleanScopeProfile,
          verify_policy: verifyPolicy,
          slots: compiled.slots,
        };
        delete (baseMeta as unknown as Record<string, unknown>).model_tier;
        if (preferredModel.trim()) {
          baseMeta.preferred_model = preferredModel.trim();
        } else {
          delete baseMeta.preferred_model;
        }

        if (shortName.trim()) {
          baseMeta.short_name = shortName.trim();
        } else {
          delete baseMeta.short_name;
        }
        if (category.trim()) {
          baseMeta.category = category.trim();
        } else {
          delete baseMeta.category;
        }
        if (parsedMaxOut && parsedMaxOut > 0) {
          baseMeta.max_output_tokens = parsedMaxOut;
        } else {
          delete baseMeta.max_output_tokens;
        }
        if (repoIRFacets.length > 0) {
          baseMeta.repo_ir_facets = repoIRFacets;
        } else {
          delete baseMeta.repo_ir_facets;
        }
        if (contextInputs.length > 0) {
          baseMeta.context_inputs = contextInputs;
        } else {
          delete baseMeta.context_inputs;
        }
        if (validDigestSource) {
          baseMeta.module_digest_source = validDigestSource;
        } else {
          delete baseMeta.module_digest_source;
        }

        const updated = await updateDocType(editingSlug, {
          meta: baseMeta,
          template_md: compiled.templateMD,
          generation_prompt_md: genPromptMD,
          refine_prompt_md: refinePromptMD,
        });
        showToast(
          updated.revision_created
            ? `已保存并生成新修订 Rev v${updated.latest_revision_no}，后台已触发 Spec Drift 重算`
            : '内容无实质变更，保持当前修订版本',
          'success',
        );
      }
      onSaved();
      onClose();
    } catch (err: unknown) {
      showToast(formatApiError(err, '保存文档类型失败').message, 'error');
    } finally {
      setSaving(false);
    }
  };

  const refineCharCount = Array.from(refinePromptMD.trim()).length;

  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={drawerWidth}
      destroyOnClose
      title={isCreate ? '新建文档类型 (DocType 蓝图编排)' : `编辑文档类型：${editingSlug}`}
      subtitle={
        isCreate
          ? '蓝图融合编排：提交后自动编译 template.md + slots[] 并原子落盘 4 份目录资产'
          : `当前修订 Rev v${loadedDetail?.latest_revision_no ?? 1} · 目录 SSOT 原子落盘并自动保留未知扩展字段`
      }
      extra={
        <button
          type="button"
          className="btn btn-outline btn-small"
          onClick={() => setDrawerWidth((prev) => (prev === 'xl' ? 'full' : 'xl'))}
          title={drawerWidth === 'xl' ? '切换至全屏宽屏编排' : '恢复标准抽屉宽度'}
        >
          {drawerWidth === 'xl' ? <Maximize2 size={13} /> : <Minimize2 size={13} />}
          <span>{drawerWidth === 'xl' ? '宽屏编排' : '标准宽度'}</span>
        </button>
      }
      footer={
        <div className="code-archdoc-drawer-footer">
          <div className="code-archdoc-drawer-footer-status">
            {hasSourceErrors ? (
              <span className="code-badge code-badge--danger">
                <XCircle size={12} />
                <span>参数化源码存在语法错误，已锁定保存</span>
              </span>
            ) : unconfirmedMissingIds.length > 0 ? (
              <span className="code-badge code-badge--danger">
                <AlertTriangle size={12} />
                <span>
                  源码中修改或移除了 {unconfirmedMissingIds.length} 个既有 slot_id：
                  {unconfirmedMissingIds.join('、')}
                </span>
              </span>
            ) : (
              <span className="code-badge code-badge--success">
                <CheckCircle2 size={12} />
                <span>
                  蓝图与模板校验通过 ({currentSlots.length} 个槽位 · 预算 {totalBudget.toLocaleString()})
                </span>
              </span>
            )}
          </div>
          <div className="code-archdoc-drawer-footer-actions">
            <button type="button" className="btn btn-secondary" onClick={onClose} disabled={saving}>
              取消
            </button>
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => void handleSave()}
              disabled={saving || loading || hasSourceErrors}
            >
              {saving ? <Loader2 size={15} className="code-archdoc-spin" /> : <Save size={15} />}
              <span>{saving ? '保存落盘中...' : isCreate ? '创建并落盘' : '保存并同步修订'}</span>
            </button>
          </div>
        </div>
      }
    >
      {loading ? (
        <div className="code-archdoc-loading">
          <Loader2 size={18} className="code-archdoc-spin" />
          <span>正在加载文档类型资产...</span>
        </div>
      ) : (
        <div className="code-archdoc-editor-body">
          {/* 顶部身份摘要条（在蓝图或修订历史页签下快速总览身份并支持直跳基础信息） */}
          {mainTab !== 'policy' && (
            <div className="code-archdoc-blueprint-identity-bar">
              <div className="code-archdoc-blueprint-identity-meta">
                <code className="code-archdoc-mono">{slug || '(未填写 slug)'}</code>
                <span className="code-archdoc-doctype-name">
                  {displayName || '(未填写展示名称)'}
                </span>
                <span
                  className={`code-badge ${
                    preferredModel ? 'code-badge--primary' : 'code-badge--muted'
                  }`}
                >
                  {preferredModel || '默认模型'}
                </span>
                {shortName && <span className="code-badge code-badge--muted">{shortName}</span>}
              </div>
              <button
                type="button"
                className="btn btn-text btn-small"
                onClick={() => setMainTab('policy')}
              >
                <Settings2 size={13} />
                <span>修改基础信息与策略</span>
              </button>
            </div>
          )}

          {/* 3 Tab 主导航 */}
          <div className="code-archdoc-editor-tabs" role="tablist">
            <button
              type="button"
              role="tab"
              aria-selected={mainTab === 'blueprint'}
              className={`code-archdoc-editor-tab ${
                mainTab === 'blueprint' ? 'code-archdoc-editor-tab--active' : ''
              }`}
              onClick={() => setMainTab('blueprint')}
            >
              <Layers size={14} />
              <span>文档蓝图 ({currentSlots.length})</span>
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mainTab === 'policy'}
              className={`code-archdoc-editor-tab ${
                mainTab === 'policy' ? 'code-archdoc-editor-tab--active' : ''
              }`}
              onClick={() => setMainTab('policy')}
            >
              <Settings2 size={14} />
              <span>基础信息与策略</span>
            </button>
            {!isCreate && (
              <button
                type="button"
                role="tab"
                aria-selected={mainTab === 'revisions'}
                className={`code-archdoc-editor-tab ${
                  mainTab === 'revisions' ? 'code-archdoc-editor-tab--active' : ''
                }`}
                onClick={() => setMainTab('revisions')}
              >
                <History size={14} />
                <span>修订历史</span>
              </button>
            )}
          </div>

          {/* Tab 1: 文档蓝图（全局指令 + 可视化 / 参数化源码 / 预览 template.md） */}
          {mainTab === 'blueprint' && (
            <div className="code-archdoc-blueprint-workspace">
              {decompileWarnings.length > 0 && (
                <div className="code-archdoc-blueprint-diag-list">
                  {decompileWarnings.map((w, i) => (
                    <div
                      key={`${w.code}-${i}`}
                      className="code-archdoc-blueprint-diag-item code-archdoc-blueprint-diag-item--warning"
                    >
                      <AlertTriangle size={14} />
                      <span>{w.message}</span>
                    </div>
                  ))}
                </div>
              )}

              {/* 顶部全局指令资产折叠区 */}
              <div className="code-archdoc-blueprint-prompts-panel">
                <div className="code-archdoc-blueprint-prompt-box">
                  <button
                    type="button"
                    className="code-archdoc-blueprint-accordion-trigger"
                    onClick={() => setGenPromptOpen((prev) => !prev)}
                    aria-expanded={genPromptOpen}
                  >
                    <span className="code-archdoc-blueprint-accordion-title">
                      {genPromptOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                      <span>全局生成指令 (generation_prompt.md)</span>
                    </span>
                    <span className="code-badge code-badge--muted">
                      {Array.from(genPromptMD.trim()).length} 字
                    </span>
                  </button>
                  {genPromptOpen && (
                    <textarea
                      className="code-archdoc-code-editor code-archdoc-blueprint-prompt-editor"
                      rows={4}
                      spellCheck={false}
                      value={genPromptMD}
                      onChange={(e) => setGenPromptMD(e.target.value)}
                      placeholder="系统级生成指令与风格约束（与下方各槽位 instruction_hint 协同注入 L2）..."
                    />
                  )}
                </div>

                <div className="code-archdoc-blueprint-prompt-box">
                  <button
                    type="button"
                    className="code-archdoc-blueprint-accordion-trigger"
                    onClick={() => setRefinePromptOpen((prev) => !prev)}
                    aria-expanded={refinePromptOpen}
                  >
                    <span className="code-archdoc-blueprint-accordion-title">
                      {refinePromptOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                      <span>Chat 局部精修附加约束 (refine_prompt.md，可选)</span>
                    </span>
                    <span
                      className={`code-badge ${
                        refineCharCount > 0 ? 'code-badge--primary' : 'code-badge--muted'
                      }`}
                    >
                      {refineCharCount > 0 ? `已配置 ${refineCharCount} 字` : '未配置'}
                    </span>
                  </button>
                  {refinePromptOpen && (
                    <textarea
                      className="code-archdoc-code-editor code-archdoc-blueprint-prompt-editor"
                      rows={3}
                      spellCheck={false}
                      value={refinePromptMD}
                      onChange={(e) => setRefinePromptMD(e.target.value)}
                      placeholder="Canvas Studio Chat 局部精修附加约束（可选）..."
                    />
                  )}
                </div>
              </div>

              {/* 蓝图编排工具栏 */}
              <div className="code-archdoc-blueprint-toolbar">
                <div className="code-archdoc-blueprint-mode-tabs" role="tablist">
                  <button
                    type="button"
                    role="tab"
                    aria-selected={blueprintMode === 'visual'}
                    className={`code-archdoc-subtab ${
                      blueprintMode === 'visual' ? 'code-archdoc-subtab--active' : ''
                    }`}
                    onClick={() => void handleSwitchBlueprintMode('visual')}
                  >
                    <Layers size={13} />
                    <span>可视化编排</span>
                  </button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={blueprintMode === 'source'}
                    className={`code-archdoc-subtab ${
                      blueprintMode === 'source' ? 'code-archdoc-subtab--active' : ''
                    }`}
                    onClick={() => void handleSwitchBlueprintMode('source')}
                  >
                    <Code2 size={13} />
                    <span>参数化源码</span>
                  </button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={blueprintMode === 'preview'}
                    className={`code-archdoc-subtab ${
                      blueprintMode === 'preview' ? 'code-archdoc-subtab--active' : ''
                    }`}
                    onClick={() => void handleSwitchBlueprintMode('preview')}
                  >
                    <Eye size={13} />
                    <span>预览 template.md</span>
                  </button>
                </div>

                <div className="code-archdoc-blueprint-metrics">
                  <span>{currentSlots.length} 个槽位</span>
                  <span>·</span>
                  <span>总预算 {totalBudget.toLocaleString()}</span>
                  <span>·</span>
                  <span>去重分片 {autoUnionFacets.length} 项</span>
                </div>

                {blueprintMode === 'visual' && (
                  <div className="code-archdoc-blueprint-toolbar-actions">
                    <button
                      type="button"
                      className="btn btn-outline btn-small"
                      onClick={handleAddStaticBlock}
                    >
                      <FileText size={13} />
                      <span>插入固定文本块</span>
                    </button>
                    <button
                      type="button"
                      className="btn btn-primary btn-small"
                      onClick={handleAddVisualSlot}
                    >
                      <Plus size={13} />
                      <span>新增章节槽位</span>
                    </button>
                  </div>
                )}
              </div>

              {/* 蓝图三视图 */}
              {blueprintMode === 'visual' && (
                <BlueprintVisualEditor
                  blueprint={blueprint}
                  persistedSlotIds={persistedSlotIds}
                  onChange={setBlueprint}
                  onStaticEdited={(blockKey) => {
                    if (blueprint.blocks[0]?.key === blockKey) {
                      leadH1TouchedRef.current = true;
                    }
                  }}
                  onConfirmSlotChange={(slotId) => {
                    setConfirmedSlotChanges((prev) => {
                      const next = new Set(prev);
                      next.add(slotId);
                      return next;
                    });
                  }}
                />
              )}

              {blueprintMode === 'source' && (
                <BlueprintSourceEditor
                  source={sourceDraft}
                  diagnostics={sourceDiagnostics}
                  existingSlotIds={currentSlotIdSet}
                  onChange={setSourceDraft}
                  onFormat={handleFormatSource}
                />
              )}

              {blueprintMode === 'preview' && (
                <div className="code-archdoc-blueprint-preview-wrap">
                  <div className="code-archdoc-field-hint">
                    只读预览：由当前蓝图实时编译生成，保存时将按此精确骨架写入磁盘{' '}
                    <code>template.md</code> 并同步 <code>meta.slots[]</code>。
                  </div>
                  <pre className="code-archdoc-blueprint-preview-pre">
                    {compiledArtifacts.templateMD || '(空模板)'}
                  </pre>
                </div>
              )}
            </div>
          )}

          {/* Tab 2: 基础信息与策略 */}
          {mainTab === 'policy' && (
            <DocTypePolicyPanel
              isCreate={isCreate}
              slug={slug}
              displayName={displayName}
              shortName={shortName}
              category={category}
              description={description}
              preferredModel={preferredModel}
              llmConfigs={llmConfigs}
              timeoutSeconds={timeoutSeconds}
              maxOutputTokens={maxOutputTokens}
              isActive={isActive}
              autoUnionFacets={autoUnionFacets}
              autoAggregateFacets={autoAggregateFacets}
              manualTopFacets={manualTopFacets}
              repoIRFacets={repoIRFacets}
              contextInputs={contextInputs}
              moduleDigestSource={moduleDigestSource}
              verifyPolicy={verifyPolicy}
              scopeProfile={scopeProfile}
              onSlugChange={setSlug}
              onDisplayNameChange={handleDisplayNameChange}
              onShortNameChange={setShortName}
              onCategoryChange={setCategory}
              onDescriptionChange={setDescription}
              onPreferredModelChange={setPreferredModel}
              onTimeoutSecondsChange={setTimeoutSeconds}
              onMaxOutputTokensChange={setMaxOutputTokens}
              onIsActiveChange={setIsActive}
              onAutoAggregateFacetsChange={(auto) => {
                setAutoAggregateFacets(auto);
                if (!auto && manualTopFacets.length === 0) {
                  setManualTopFacets(autoUnionFacets.length > 0 ? [...autoUnionFacets] : ['tree']);
                }
              }}
              onManualTopFacetsChange={setManualTopFacets}
              onAlignTopFacetsWithUnion={() => setManualTopFacets([...autoUnionFacets])}
              onRepoIRFacetsChange={setRepoIRFacets}
              onContextInputsChange={setContextInputs}
              onModuleDigestSourceChange={setModuleDigestSource}
              onVerifyPolicyChange={setVerifyPolicy}
              onScopeProfileChange={setScopeProfile}
              onGoToBlueprint={() => setMainTab('blueprint')}
            />
          )}

          {/* Tab 3: 修订历史 */}
          {mainTab === 'revisions' && (
            <div className="code-archdoc-revisions-panel">
              {loadingRevs ? (
                <div className="code-archdoc-loading">
                  <Loader2 size={16} className="code-archdoc-spin" />
                  <span>正在加载修订快照历史...</span>
                </div>
              ) : revisions.length === 0 ? (
                <div className="code-archdoc-slots-empty">暂无历史修订记录。</div>
              ) : (
                <table className="code-archdoc-table">
                  <thead>
                    <tr>
                      <th>修订号</th>
                      <th>聚合哈希 (aggregate_hash)</th>
                      <th>指令哈希</th>
                      <th>槽位数</th>
                      <th>创建时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    {revisions.map((rev) => (
                      <tr key={rev.id}>
                        <td>
                          <span className="code-badge code-badge--primary">
                            Rev v{rev.revision_no}
                          </span>
                        </td>
                        <td>
                          <code className="code-archdoc-mono" title={rev.aggregate_hash}>
                            {rev.aggregate_hash.slice(0, 16)}
                          </code>
                        </td>
                        <td>
                          <code className="code-archdoc-mono" title={rev.prompt_hash}>
                            {rev.prompt_hash.slice(0, 12)}
                          </code>
                        </td>
                        <td>{rev.slots_snapshot.length}</td>
                        <td className="text-secondary">{formatDateTime(rev.created_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}
        </div>
      )}
    </Drawer>
  );
};

export default DocTypeEditorDrawer;
