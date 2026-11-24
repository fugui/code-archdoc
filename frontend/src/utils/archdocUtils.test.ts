import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import {
  DEFAULT_ARCHDOC_PAGE_SIZE,
  buildModulePath,
  buildOverviewDrilldownPath,
  buildStudioPath,
  computeRepoFilterFlags,
  formatCoverageRate,
  parseArchTreeFilterParams,
  parseDriftFilterParams,
  parseEvidenceDeepLinkParams,
  parseEvidenceHref,
  parseLineRange,
  parseRepoFilterParams,
  parseTaskFilterParams,
  resolveSafeStudioBackTarget,
  serializeArchTreeFilterParams,
  serializeDriftFilterParams,
  serializeRepoFilterParams,
  serializeTaskFilterParams,
} from './urlQuery.ts';
import {
  buildArchTreeMicroPills,
  collectArchTreeExpandableIds,
  computeDefaultExpandedArchTreeIds,
  flattenArchTreeRows,
} from './archTreeUtils.ts';
import { ArchdocApiError, formatApiError, formatTaskErrorCode } from './errorMessages.ts';
import { isSafeLinkHref, parseMarkdownBlocks } from './markdown.ts';
import {
  MODULE_STRATEGY_OPTIONS,
  formatModuleSourceLabel,
  isBatchTaskKind,
  normalizeModuleStrategy,
} from './moduleUtils.ts';
import {
  formatArchElementTypeLabel,
  getDocTypeCategory,
  getDocTypeShortName,
  groupDocTypesByCategory,
  summarizeScopeDocs,
} from './docTypeUtils.ts';
import { parseChatSSEBlock } from './sseParser.ts';
import {
  buildExcludedRefDocIds,
  computeIncludedRefDocStats,
  formatArchNodeRefDocBadgeText,
  formatRefCitationBadgeLabel,
  formatRefDocKindLabel,
  formatRefDocScopeLabel,
  formatRefDocSummaryStaleHint,
  formatRefDocTokenBadge,
  groupEffectiveRefDocsByScope,
} from './refDocUtils.ts';

describe('URL Search Params 分页与筛选序列化/反序列化 (Verification Checklist §3.2.1)', () => {
  it('默认回退 page=1、pageSize=25 且正确解析全部筛选参数', () => {
    const empty = parseRepoFilterParams(new URLSearchParams());
    assert.equal(empty.page, 1);
    assert.equal(empty.pageSize, DEFAULT_ARCHDOC_PAGE_SIZE);
    assert.equal(empty.search, '');
    assert.equal(empty.departmentId, '');
    assert.equal(empty.docType, '');
    assert.equal(empty.status, '');
    assert.equal(empty.scopeType, '');
    assert.equal(empty.view, 'repo');
    assert.equal(empty.subsystemId, '');
    assert.equal(empty.archBinding, '');

    const populated = parseRepoFilterParams(
      new URLSearchParams(
        'page=3&pageSize=50&search=order&department_id=10&doc_type=system-architecture&status=approved&scope_type=subdir&view=arch&subsystem_id=7&arch_binding=bound',
      ),
    );
    assert.deepEqual(populated, {
      page: 3,
      pageSize: 50,
      search: 'order',
      departmentId: '10',
      docType: 'system-architecture',
      status: 'approved',
      scopeType: 'subdir',
      view: 'arch',
      subsystemId: '7',
      archBinding: 'bound',
    });
  });

  it('非法页码、非白名单 pageSize 与非法枚举自动安全兜底', () => {
    const parsed = parseRepoFilterParams(
      new URLSearchParams(
        'page=-4&pageSize=999&department_id=abc&status=invalid&scope_type=unknown&view=grid&subsystem_id=0&arch_binding=none',
      ),
    );
    assert.equal(parsed.page, 1);
    assert.equal(parsed.pageSize, 25);
    assert.equal(parsed.departmentId, '');
    assert.equal(parsed.status, '');
    assert.equal(parsed.scopeType, '');
    assert.equal(parsed.view, 'repo');
    assert.equal(parsed.subsystemId, '');
    assert.equal(parsed.archBinding, '');
  });

  it('序列化支持增量更新筛选并保持 URL 双向同步往返一致', () => {
    const initial = new URLSearchParams('page=4&pageSize=100&search=pay');
    const updated = serializeRepoFilterParams(
      {
        page: 1,
        departmentId: '12',
        status: 'draft',
        docType: 'data-and-contract',
        view: 'arch',
        subsystemId: '9',
        archBinding: 'unbound',
      },
      initial,
    );
    assert.equal(updated.get('page'), '1');
    assert.equal(updated.get('pageSize'), '100');
    assert.equal(updated.get('search'), 'pay');
    assert.equal(updated.get('department_id'), '12');
    assert.equal(updated.get('status'), 'draft');
    assert.equal(updated.get('doc_type'), 'data-and-contract');
    assert.equal(updated.get('view'), 'arch');
    assert.equal(updated.get('subsystem_id'), '9');
    assert.equal(updated.get('arch_binding'), 'unbound');

    const roundTrip = parseRepoFilterParams(updated);
    assert.equal(roundTrip.page, 1);
    assert.equal(roundTrip.pageSize, 100);
    assert.equal(roundTrip.search, 'pay');
    assert.equal(roundTrip.departmentId, '12');
    assert.equal(roundTrip.status, 'draft');
    assert.equal(roundTrip.docType, 'data-and-contract');
    assert.equal(roundTrip.view, 'arch');
    assert.equal(roundTrip.subsystemId, '9');
    assert.equal(roundTrip.archBinding, 'unbound');
  });

  it('生成 Studio 与模块路由路径兼顾独立态与门户嵌入态', () => {
    assert.equal(buildModulePath(false, '/arch-tree'), '/arch-tree');
    assert.equal(buildModulePath(true, 'tasks'), '/archdoc/tasks');
    assert.equal(buildStudioPath(false, 101, { docId: 42 }), '/studio/101/docs/42');
    assert.equal(buildStudioPath(true, 101, { docId: 42 }), '/archdoc/studio/101/docs/42');
    assert.equal(buildStudioPath(false, 101, { subdirectory: 'svc/order' }), '/studio/101?subdir=svc%2Forder');
  });

  it('正确解析并双向序列化架构文档全景 (/arch-tree) 筛选参数', () => {
    const empty = parseArchTreeFilterParams(new URLSearchParams());
    assert.deepEqual(empty, {
      search: '',
      subsystemId: '',
      docType: '',
      status: '',
    });

    const qs = serializeArchTreeFilterParams({
      search: 'gateway',
      subsystemId: '5',
      docType: 'system-architecture',
      status: 'drifted',
    });
    assert.deepEqual(parseArchTreeFilterParams(qs), {
      search: 'gateway',
      subsystemId: '5',
      docType: 'system-architecture',
      status: 'drifted',
    });

    const invalid = parseArchTreeFilterParams(
      new URLSearchParams('subsystem_id=abc&status=invalid'),
    );
    assert.equal(invalid.subsystemId, '');
    assert.equal(invalid.status, '');
  });

  it('正确解析并双向序列化反推任务中心 (/tasks) 分页与筛选参数', () => {
    const empty = parseTaskFilterParams(new URLSearchParams());
    assert.deepEqual(empty, {
      page: 1,
      pageSize: 25,
      repoId: '',
      status: '',
      taskKind: '',
      engine: '',
      mine: false,
      includeChildren: false,
    });

    const qs = serializeTaskFilterParams({
      page: 2,
      pageSize: 50,
      repoId: '18',
      status: 'active',
      taskKind: 'batch',
      engine: 'opencode',
      mine: true,
      includeChildren: true,
    });
    assert.deepEqual(parseTaskFilterParams(qs), {
      page: 2,
      pageSize: 50,
      repoId: '18',
      status: 'active',
      taskKind: 'batch',
      engine: 'opencode',
      mine: true,
      includeChildren: true,
    });

    const invalid = parseTaskFilterParams(
      new URLSearchParams('page=0&pageSize=12&repo_id=xyz&status=bad&task_kind=bad&engine=invalid'),
    );
    assert.equal(invalid.page, 1);
    assert.equal(invalid.pageSize, 25);
    assert.equal(invalid.repoId, '');
    assert.equal(invalid.status, '');
    assert.equal(invalid.taskKind, '');
    assert.equal(invalid.engine, '');
  });
});

describe('深链 /docs/:docId/evidence 参数解析与非法行号兜底 (Verification Checklist §3.2.2)', () => {
  it('正常解析 commit/file/symbol/lines=142-188 与单行 lines=42', () => {
    const params = parseEvidenceDeepLinkParams(
      new URLSearchParams(
        'commit=a1b2c3d4e5f67890&file=src/order/order_service.cpp&symbol=order::OrderService::Pay&lines=142-188',
      ),
    );
    assert.deepEqual(params, {
      commit: 'a1b2c3d4e5f67890',
      file: 'src/order/order_service.cpp',
      symbol: 'order::OrderService::Pay',
      lineStart: 142,
      lineEnd: 188,
      scope: undefined,
    });

    assert.deepEqual(parseLineRange('42'), { lineStart: 42, lineEnd: 42 });
    assert.deepEqual(parseLineRange('L188-L142'), { lineStart: 142, lineEnd: 188 });
  });

  it('非法行号（负数、字母、零、超长乱码）与非法 commit SHA 安全兜底为 undefined', () => {
    const bad = parseEvidenceDeepLinkParams(
      new URLSearchParams('commit=not-a-sha!&file=src/main.cpp&lines=-5-abc'),
    );
    assert.equal(bad.commit, undefined);
    assert.equal(bad.file, 'src/main.cpp');
    assert.equal(bad.lineStart, undefined);
    assert.equal(bad.lineEnd, undefined);

    assert.equal(parseLineRange('0-10'), undefined);
    assert.equal(parseLineRange('abc'), undefined);
    assert.equal(parseLineRange(''), undefined);
  });

  it('Markdown 内部证据深链 URL 解析正确识别 docId 与查询参数', () => {
    const parsed = parseEvidenceHref(
      '/archdoc/docs/42/evidence?commit=abcdef123456&file=proto/order.proto&symbol=CreateOrder&lines=12-28',
    );
    assert.ok(parsed);
    assert.equal(parsed.docId, 42);
    assert.equal(parsed.commit, 'abcdef123456');
    assert.equal(parsed.file, 'proto/order.proto');
    assert.equal(parsed.symbol, 'CreateOrder');
    assert.equal(parsed.lineStart, 12);
    assert.equal(parsed.lineEnd, 28);

    assert.equal(parseEvidenceHref('https://example.com/docs/42/evidence'), null);
  });
});

describe('API 语义错误码映射与友好提示渲染 (Verification Checklist §3.2.3)', () => {
  it('409 OPTIMISTIC_LOCK_CONFLICT 返回冲突标题并提取服务端最新版本号', () => {
    const err = new ArchdocApiError(409, {
      error: '版本冲突',
      code: 'OPTIMISTIC_LOCK_CONFLICT',
      details: { current_revision_no: 7 },
    });
    const formatted = formatApiError(err);
    assert.equal(formatted.code, 'OPTIMISTIC_LOCK_CONFLICT');
    assert.equal(formatted.currentRevisionNo, 7);
    assert.match(formatted.message, /v7/);
  });

  it('422 VERIFIER_BLOCKED 摘要展示明文凭据阻断数量并保留 verifierReport', () => {
    const err = new ArchdocApiError(422, {
      error: 'Verifier blocked',
      code: 'VERIFIER_BLOCKED',
      details: {
        verifier_report: {
          passed: false,
          can_approve: false,
          blocker_count: 2,
          error_count: 0,
          warning_count: 1,
          slot_coverage_rate: 1,
          missing_required_slots: [],
          unverified_refs: [],
          mermaid_issues: [],
          secret_findings: [
            {
              slot_id: 'ops',
              rule_id: 'token_prefix',
              matched_mask: 'sk-****1234',
              line_offset: 8,
              severity: 'blocker',
            },
          ],
        },
      },
    });
    const formatted = formatApiError(err);
    assert.equal(formatted.code, 'VERIFIER_BLOCKED');
    assert.match(formatted.title, /Verifier/);
    assert.match(formatted.message, /2 项阻断性安全风险/);
    assert.equal(formatted.verifierReport?.blocker_count, 2);
  });

  it('403 PATH_ESCAPE 与 TOKEN_BUDGET_EXCEEDED 输出安全与熔断建议文案', () => {
    const escapeErr = formatApiError(
      new ArchdocApiError(403, { error: 'path escape', code: 'PATH_ESCAPE' }),
    );
    assert.equal(escapeErr.code, 'PATH_ESCAPE');
    assert.match(escapeErr.message, /沙箱边界/);

    const budgetErr = formatTaskErrorCode('TOKEN_BUDGET_EXCEEDED', '');
    assert.match(budgetErr.title, /Token 预算上限/);
    assert.match(budgetErr.message, /\.archdocignore/);
  });
});

describe('安全 Markdown AST 解析器与 XSS 链接拦截', () => {
  it('拦截 javascript:/data: 伪协议链接，同时保留安全相对路径与 Mermaid 代码块', () => {
    assert.equal(isSafeLinkHref('javascript:alert(1)'), false);
    assert.equal(isSafeLinkHref('data:text/html,<script>alert(1)</script>'), false);
    assert.equal(isSafeLinkHref('/archdoc/docs/42/evidence?file=a.cpp'), true);
    assert.equal(isSafeLinkHref('https://example.com'), true);

    const blocks = parseMarkdownBlocks([
      '## 1. 架构概览',
      '',
      '查看 [恶意脚本](javascript:alert(1)) 与 [源码证据](/archdoc/docs/42/evidence?file=a.cpp&lines=1-5)。',
      '',
      '```mermaid',
      'flowchart LR',
      '  A --> B',
      '```',
    ].join('\n'));

    assert.equal(blocks.length, 3);
    assert.equal(blocks[0].type, 'heading');
    assert.equal(blocks[1].type, 'paragraph');
    if (blocks[1].type === 'paragraph') {
      const links = blocks[1].children.filter((n) => n.type === 'link');
      assert.equal(links.length, 1);
      if (links[0].type === 'link') {
        assert.equal(links[0].href, '/archdoc/docs/42/evidence?file=a.cpp&lines=1-5');
      }
    }
    assert.equal(blocks[2].type, 'mermaid');
  });
});

describe('SSE Chat 局部精修帧解析器 (T-M5-05)', () => {
  it('正确解析 meta / delta / diff_proposal / verifier_preview / done / error 帧并忽略非法帧', () => {
    const meta = parseChatSSEBlock(
      'event: meta\ndata: {"session_id":12,"message_id":88,"slot_id":"core_request_flows","base_revision_no":4,"model_name":"DeepSeek-V3"}',
    );
    assert.ok(meta && meta.event === 'meta');
    assert.equal(meta.data.message_id, 88);

    const toolEv = parseChatSSEBlock(
      'event: tool_call\ndata: {"round":1,"tool_name":"read_file_lines","summary":"read_file_lines(src/order.cpp:1-40)","is_error":false,"duration_ms":3}',
    );
    assert.ok(toolEv && toolEv.event === 'tool_call');
    assert.equal(toolEv.data.tool_name, 'read_file_lines');
    assert.equal(toolEv.data.round, 1);

    const delta = parseChatSSEBlock('event: delta\ndata: {"text":"补充重试逻辑"}');
    assert.ok(delta && delta.event === 'delta');
    assert.equal(delta.data.text, '补充重试逻辑');

    const preview = parseChatSSEBlock(
      'event: verifier_preview\ndata: {"can_approve":true,"blocker_count":0,"error_count":0,"warning_count":1,"issues":[{"kind":"unverified_ref","severity":"warning","message":"未验证符号"}]}',
    );
    assert.ok(preview && preview.event === 'verifier_preview');
    assert.equal(preview.data.warning_count, 1);

    assert.equal(parseChatSSEBlock(': keepalive comment'), null);
    assert.equal(parseChatSSEBlock('event: unknown\ndata: {}'), null);
    assert.equal(parseChatSSEBlock('event: delta\ndata: not-json'), null);
  });

  it('T-ADR06-13: 解析新增 plan / stage / proposal_item 等 10 类 SSE 事件帧', () => {
    const planEv = parseChatSSEBlock(
      'event: plan\ndata: {"target_slots":[{"slot_id":"runtime_concurrency","title":"并发模型","comment_ids":[1,2],"reason":"命中局部批注"}],"skipped_locked_slots":[{"slot_id":"adr_constraints_debt","title":"ADR","comment_ids":[3],"advice":"已锁定保留原文"}]}',
    );
    assert.ok(planEv && planEv.event === 'plan');
    assert.equal(planEv.data.target_slots.length, 1);
    assert.equal(planEv.data.skipped_locked_slots[0].slot_id, 'adr_constraints_debt');

    const stageEv = parseChatSSEBlock(
      'event: stage\ndata: {"stage":"refining_slot","slot_id":"runtime_concurrency","index":1,"total":2}',
    );
    assert.ok(stageEv && stageEv.event === 'stage');
    assert.equal(stageEv.data.stage, 'refining_slot');

    const itemEv = parseChatSSEBlock(
      'event: proposal_item\ndata: {"item_id":99,"message_id":88,"slot_id":"runtime_concurrency","base_revision_no":4,"before_md":"old","after_md":"new","unified_diff":"@@ -1 +1 @@","cited_anchors":[],"addressed_comment_ids":[1,2],"status":"pending"}',
    );
    assert.ok(itemEv && itemEv.event === 'proposal_item');
    assert.equal(itemEv.data.item_id, 99);
    assert.deepEqual(itemEv.data.addressed_comment_ids, [1, 2]);
  });
});

describe('双维漂移雷达 URL Search Params 分页与筛选同步 (T-M6-05)', () => {
  it('默认回退 page=1、pageSize=25、driftType=all 并支持完整 round-trip 序列化', () => {
    const empty = parseDriftFilterParams(new URLSearchParams());
    assert.deepEqual(empty, {
      page: 1,
      pageSize: DEFAULT_ARCHDOC_PAGE_SIZE,
      search: '',
      departmentId: '',
      driftType: 'all',
      docType: '',
    });

    const params = serializeDriftFilterParams({
      page: 2,
      pageSize: 50,
      search: 'order',
      departmentId: '12',
      driftType: 'both',
      docType: 'system-architecture',
    });
    const parsed = parseDriftFilterParams(params);
    assert.deepEqual(parsed, {
      page: 2,
      pageSize: 50,
      search: 'order',
      departmentId: '12',
      driftType: 'both',
      docType: 'system-architecture',
    });

    const invalid = parseDriftFilterParams(
      new URLSearchParams('page=-1&pageSize=33&department_id=abc&drift_type=invalid'),
    );
    assert.equal(invalid.page, 1);
    assert.equal(invalid.pageSize, 25);
    assert.equal(invalid.departmentId, '');
    assert.equal(invalid.driftType, 'all');
  });
});

describe('ADR-04 模块发现策略、来源标签与批量任务契约对齐', () => {
  it('策略选项严格对齐后端枚举且非法别名安全兜底为 merged', () => {
    assert.deepEqual(
      MODULE_STRATEGY_OPTIONS.map((opt) => opt.value),
      ['merged', 'top_level_dirs', 'arch_elements'],
    );
    assert.equal(normalizeModuleStrategy('merged'), 'merged');
    assert.equal(normalizeModuleStrategy('top_level_dirs'), 'top_level_dirs');
    assert.equal(normalizeModuleStrategy('arch_elements'), 'arch_elements');
    assert.equal(normalizeModuleStrategy('first_level_dirs'), 'merged');
    assert.equal(normalizeModuleStrategy('build_manifests'), 'merged');
    assert.equal(normalizeModuleStrategy(undefined), 'merged');
  });

  it('来源标签正确映射 discovered / arch_element / orphan 并识别 batch 任务枚举', () => {
    assert.equal(formatModuleSourceLabel('discovered'), '一级目录');
    assert.equal(formatModuleSourceLabel('arch_element'), '架构元素');
    assert.equal(formatModuleSourceLabel('orphan'), '孤立范围');
    assert.equal(formatModuleSourceLabel('unknown'), '');

    assert.equal(isBatchTaskKind('batch'), true);
    assert.equal(isBatchTaskKind('batch_generate'), false);
    assert.equal(isBatchTaskKind('generate'), false);
  });
});

describe('DocType 短名分组、方案 A1 覆盖汇总与架构元素类型标签', () => {
  it('正确解析 short_name/category 回退并按预设顺序分组', () => {
    assert.equal(getDocTypeShortName({ short_name: '系统架构', display_name: '系统架构与核心链路' }), '系统架构');
    assert.equal(getDocTypeShortName({ short_name: '', display_name: '自定义长标题' }), '自定义长标题');
    assert.equal(getDocTypeCategory({ category: '架构设计' }), '架构设计');
    assert.equal(getDocTypeCategory({ category: '' }), '其他文档');

    const groups = groupDocTypesByCategory([
      { slug: 'misc', category: '', display_name: '杂项' },
      { slug: 'custom', category: '专项评审', display_name: '专项' },
      { slug: 'ops', category: '运维交付', display_name: '运维' },
      { slug: 'arch', category: '架构设计', display_name: '架构' },
    ]);
    assert.deepEqual(
      groups.map((g) => g.category),
      ['架构设计', '运维交付', '专项评审', '其他文档'],
    );
  });

  it('summarizeScopeDocs 精确汇总已发布/草稿/漂移/阻断/待生成数量', () => {
    const docTypes = [
      {
        id: 1,
        slug: 'system-architecture',
        display_name: '系统架构与核心链路',
        short_name: '系统架构',
        category: '架构设计',
        description: '',
        is_builtin: true,
        is_active: true,
        preferred_model: 'DeepSeek-V3',
        timeout_seconds: 300,
        ir_facets: [],
        slot_count: 5,
        latest_revision_id: 1,
        latest_revision_no: 1,
        latest_aggregate_hash: 'h1',
        published_doc_count: 1,
        updated_at: '',
      },
      {
        id: 2,
        slug: 'code-walkthrough',
        display_name: '代码导读与改码指南',
        short_name: '代码导读',
        category: '研发指南',
        description: '',
        is_builtin: true,
        is_active: true,
        preferred_model: '',
        timeout_seconds: 240,
        ir_facets: [],
        slot_count: 3,
        latest_revision_id: 2,
        latest_revision_no: 1,
        latest_aggregate_hash: 'h2',
        published_doc_count: 0,
        updated_at: '',
      },
      {
        id: 3,
        slug: 'ops-and-config',
        display_name: '配置、部署与研发上手',
        short_name: '运维与配置',
        category: '运维交付',
        description: '',
        is_builtin: true,
        is_active: true,
        preferred_model: '',
        timeout_seconds: 240,
        ir_facets: [],
        slot_count: 4,
        latest_revision_id: 3,
        latest_revision_no: 1,
        latest_aggregate_hash: 'h3',
        published_doc_count: 0,
        updated_at: '',
      },
    ];

    const summary = summarizeScopeDocs(
      {
        doc_statuses: {
          'system-architecture': {
            document_id: 11,
            status: 'approved',
            current_revision_no: 2,
            published_revision_no: 2,
            has_code_drift: true,
            has_spec_drift: false,
            affected_slot_count: 1,
            verifier_passed: true,
            can_approve: true,
            updated_at: '',
          },
          'code-walkthrough': {
            document_id: 12,
            status: 'draft',
            current_revision_no: 1,
            published_revision_no: 0,
            has_code_drift: false,
            has_spec_drift: false,
            affected_slot_count: 0,
            verifier_passed: false,
            can_approve: false,
            updated_at: '',
          },
        },
      },
      docTypes,
    );

    assert.equal(summary.totalTypes, 3);
    assert.equal(summary.generatedCount, 2);
    assert.equal(summary.approvedCount, 1);
    assert.equal(summary.draftCount, 1);
    assert.equal(summary.driftedCount, 1);
    assert.equal(summary.blockedCount, 1);
    assert.equal(summary.missingCount, 1);
    assert.equal(summary.missingItems[0].slug, 'ops-and-config');
    assert.equal(formatArchElementTypeLabel('subsystem'), '子系统');
    assert.equal(formatArchElementTypeLabel('group'), '功能组');
    assert.equal(formatArchElementTypeLabel('module'), '功能模块');
  });
});

describe('全局概览大盘覆盖率计算与下钻路由拼装', () => {
  it('formatCoverageRate 正确处理正常比例、分母为零、负数及超出上限边界', () => {
    assert.equal(formatCoverageRate(0, 0), '0.0%');
    assert.equal(formatCoverageRate(3, 0), '0.0%');
    assert.equal(formatCoverageRate(-1, 10), '0.0%');
    assert.equal(formatCoverageRate(1, 10), '10.0%');
    assert.equal(formatCoverageRate(2, 3), '66.7%');
    assert.equal(formatCoverageRate(12, 10), '100.0%');
  });

  it('buildOverviewDrilldownPath 在独立态与门户嵌入态下正确拼接筛选参数并过滤空值', () => {
    assert.equal(buildOverviewDrilldownPath(false, '/repos'), '/repos');
    assert.equal(buildOverviewDrilldownPath(true, '/arch-tree'), '/archdoc/arch-tree');
    assert.equal(
      buildOverviewDrilldownPath(false, '/repos', { status: 'drifted', empty: '   ' }),
      '/repos?status=drifted',
    );
    assert.equal(
      buildOverviewDrilldownPath(true, '/tasks', { status: 'failed' }),
      '/archdoc/tasks?status=failed',
    );
    assert.equal(
      buildOverviewDrilldownPath(true, '/drift', { drift_type: 'code' }),
      '/archdoc/drift?drift_type=code',
    );
  });
});

describe('ADR-09 架构文档全景树表扁平化、默认展开阈值、筛选标志与 Studio 安全返回 (T-ADR09-08 ~ T-ADR09-11)', () => {
  const sampleTree = [
    {
      id: 1,
      identifier: 'RND_AI',
      name_cn: 'AI辅助研发子系统',
      name_en: 'AI R&D',
      type: 'subsystem',
      parent_id: null,
      owner_id: 10,
      owner_name: '管理员',
      repo_id: null,
      subdirectory: '',
      description: '',
      stats: {
        module_count: 3,
        bound_module_count: 2,
        covered_module_count: 1,
        approved_doc_count: 1,
        draft_doc_count: 1,
        drifted_doc_count: 0,
        blocked_doc_count: 0,
      },
      scopes: [],
      children: [
        {
          id: 2,
          identifier: 'BENCH',
          name_cn: '门户功能组',
          name_en: 'Bench',
          type: 'group',
          parent_id: 1,
          owner_id: 10,
          owner_name: '管理员',
          repo_id: null,
          subdirectory: '',
          description: '',
          stats: {
            module_count: 2,
            bound_module_count: 1,
            covered_module_count: 1,
            approved_doc_count: 1,
            draft_doc_count: 0,
            drifted_doc_count: 0,
            blocked_doc_count: 0,
          },
          scopes: [],
          children: [
            {
              id: 3,
              identifier: 'BENCH_FRONT',
              name_cn: '门户前端',
              name_en: 'Bench Front',
              type: 'module',
              parent_id: 2,
              owner_id: 10,
              owner_name: '管理员',
              repo_id: 101,
              subdirectory: 'frontend',
              description: '',
              binding_status: 'bound' as const,
              scope: null,
              stats: {
                module_count: 1,
                bound_module_count: 1,
                covered_module_count: 1,
                approved_doc_count: 1,
                draft_doc_count: 0,
                drifted_doc_count: 0,
                blocked_doc_count: 0,
              },
              scopes: [],
              children: [],
            },
            {
              id: 4,
              identifier: 'BENCH_AUTH',
              name_cn: '权限中心',
              name_en: 'Bench Auth',
              type: 'module',
              parent_id: 2,
              owner_id: 11,
              owner_name: '张三',
              repo_id: null,
              subdirectory: '',
              description: '',
              binding_status: 'unbound' as const,
              scope: null,
              stats: {
                module_count: 1,
                bound_module_count: 0,
                covered_module_count: 0,
                approved_doc_count: 0,
                draft_doc_count: 0,
                drifted_doc_count: 0,
                blocked_doc_count: 0,
              },
              scopes: [],
              children: [],
            },
          ],
        },
        {
          id: 5,
          identifier: 'DIRECT_MOD',
          name_cn: '直挂子系统模块',
          name_en: 'Direct Mod',
          type: 'module',
          parent_id: 1,
          owner_id: 10,
          owner_name: '管理员',
          repo_id: 102,
          subdirectory: '',
          description: '',
          binding_status: 'repo_inactive' as const,
          scope: null,
          stats: {
            module_count: 1,
            bound_module_count: 0,
            covered_module_count: 0,
            approved_doc_count: 0,
            draft_doc_count: 0,
            drifted_doc_count: 0,
            blocked_doc_count: 0,
          },
          scopes: [],
          children: [],
        },
      ],
    },
  ];

  it('T-ADR09-08: 树表扁平化、跳过功能组直挂层级、rowKey 与默认展开阈值(50)', () => {
    const expandableAll = collectArchTreeExpandableIds(sampleTree);
    assert.deepEqual(expandableAll, [1, 2]);
    assert.deepEqual(collectArchTreeExpandableIds(sampleTree, 1), [1]);

    const collapsed = flattenArchTreeRows(sampleTree, new Set());
    assert.equal(collapsed.length, 1);
    assert.equal(collapsed[0].rowKey, 'node:1');
    assert.equal(collapsed[0].depth, 0);
    assert.equal(collapsed[0].hasChildren, true);
    assert.equal(collapsed[0].isExpanded, false);

    const expandedSubsysOnly = flattenArchTreeRows(sampleTree, new Set([1]));
    assert.deepEqual(
      expandedSubsysOnly.map((r) => ({ key: r.rowKey, depth: r.depth, hasChildren: r.hasChildren })),
      [
        { key: 'node:1', depth: 0, hasChildren: true },
        { key: 'node:2', depth: 1, hasChildren: true },
        { key: 'module:5', depth: 1, hasChildren: false },
      ],
    );

    const fullyExpanded = flattenArchTreeRows(sampleTree, new Set([1, 2]));
    assert.deepEqual(
      fullyExpanded.map((r) => ({ key: r.rowKey, depth: r.depth })),
      [
        { key: 'node:1', depth: 0 },
        { key: 'node:2', depth: 1 },
        { key: 'module:3', depth: 2 },
        { key: 'module:4', depth: 2 },
        { key: 'module:5', depth: 1 },
      ],
    );

    assert.deepEqual(computeDefaultExpandedArchTreeIds(sampleTree, 50, false), [1, 2]);
    assert.deepEqual(computeDefaultExpandedArchTreeIds(sampleTree, 51, false), [1]);
    assert.deepEqual(computeDefaultExpandedArchTreeIds(sampleTree, 51, true), [1, 2]);
  });

  it('T-ADR09-09: /repos URL 序列化保留 arch_binding=all、解析 unbound_repo 且防筛选陷阱', () => {
    const allParams = serializeRepoFilterParams({ archBinding: 'all' });
    assert.equal(allParams.get('arch_binding'), 'all');
    assert.equal(parseRepoFilterParams(allParams).archBinding, 'all');

    const defaultParams = serializeRepoFilterParams({ archBinding: '' });
    assert.equal(defaultParams.get('arch_binding'), null);
    assert.equal(parseRepoFilterParams(defaultParams).archBinding, '');

    const unboundRepoParam = serializeRepoFilterParams({ archBinding: 'unbound_repo' });
    assert.equal(unboundRepoParam.get('arch_binding'), null);
    assert.equal(
      parseRepoFilterParams(new URLSearchParams('arch_binding=unbound_repo')).archBinding,
      'unbound_repo',
    );

    assert.deepEqual(computeRepoFilterFlags(parseRepoFilterParams(new URLSearchParams())), {
      advancedFilterCount: 0,
      hasActiveFilters: false,
    });
    assert.deepEqual(
      computeRepoFilterFlags(parseRepoFilterParams(new URLSearchParams('arch_binding=all'))),
      { advancedFilterCount: 0, hasActiveFilters: false },
    );
    assert.deepEqual(
      computeRepoFilterFlags(parseRepoFilterParams(new URLSearchParams('arch_binding=unbound_repo'))),
      { advancedFilterCount: 0, hasActiveFilters: false },
    );
    assert.deepEqual(
      computeRepoFilterFlags(
        parseRepoFilterParams(new URLSearchParams('arch_binding=bound&scope_type=subdir')),
      ),
      { advancedFilterCount: 2, hasActiveFilters: true },
    );
    assert.deepEqual(
      computeRepoFilterFlags(parseRepoFilterParams(new URLSearchParams('search=order'))),
      { advancedFilterCount: 0, hasActiveFilters: true },
    );
  });

  it('T-ADR09-10: Studio 来源安全返回白名单校验（拦截开放重定向与 XSS 协议）', () => {
    assert.deepEqual(resolveSafeStudioBackTarget(null, false), {
      path: '/arch-tree',
      label: '架构文档全景',
    });
    assert.deepEqual(resolveSafeStudioBackTarget(undefined, true), {
      path: '/archdoc/arch-tree',
      label: '架构文档全景',
    });

    assert.deepEqual(
      resolveSafeStudioBackTarget(
        { from: '/repos?page=2&arch_binding=all', fromLabel: '未关联代码仓' },
        false,
      ),
      { path: '/repos?page=2&arch_binding=all', label: '未关联代码仓' },
    );
    assert.deepEqual(
      resolveSafeStudioBackTarget({ from: '/archdoc/tasks?status=failed' }, true),
      { path: '/archdoc/tasks?status=failed', label: '反推任务中心' },
    );
    assert.deepEqual(
      resolveSafeStudioBackTarget({ from: '/drift?drift_type=code' }, false),
      { path: '/drift?drift_type=code', label: '双维漂移雷达' },
    );

    for (const evil of [
      'https://evil.com/repos',
      'http://evil.com',
      '//evil.com/arch-tree',
      'javascript:alert(1)',
      '/admin/llm',
      '/repos#fragment',
      '',
    ]) {
      assert.deepEqual(resolveSafeStudioBackTarget({ from: evil, fromLabel: '恶意' }, false), {
        path: '/arch-tree',
        label: '架构文档全景',
      });
      assert.deepEqual(resolveSafeStudioBackTarget({ from: evil }, true), {
        path: '/archdoc/arch-tree',
        label: '架构文档全景',
      });
    }
  });

  it('T-ADR09-11: buildArchTreeMicroPills 输出与点击切换及 /arch-tree 概览下钻', () => {
    let toggled: string | null = null;
    const pills = buildArchTreeMicroPills(
      {
        total_modules: 12,
        bound_modules: 10,
        covered_modules: 7,
        approved_docs: 15,
        draft_docs: 4,
        drifted_docs: 3,
        unassigned_repo_count: 2,
      },
      'approved',
      (next) => {
        toggled = next;
      },
    );
    assert.deepEqual(
      pills.map((p) => ({ key: p.key, label: p.label, value: p.value, active: Boolean(p.active) })),
      [
        { key: 'total_modules', label: '功能模块', value: 12, active: false },
        { key: 'covered_modules', label: '已覆盖', value: 7, active: false },
        { key: 'approved_docs', label: '已发布', value: 15, active: true },
        { key: 'drifted_docs', label: '漂移', value: 3, active: false },
      ],
    );
    pills[2].onClick?.();
    assert.equal(toggled, '');
    pills[3].onClick?.();
    assert.equal(toggled, 'drifted');

    assert.equal(
      buildOverviewDrilldownPath(false, '/arch-tree', { status: 'approved' }),
      '/arch-tree?status=approved',
    );
    assert.equal(
      buildOverviewDrilldownPath(true, '/arch-tree', { status: 'drifted' }),
      '/archdoc/arch-tree?status=drifted',
    );
  });
});

describe('架构参考文档纯函数工具 (T-ADR10-15)', () => {
  it('按作用域层级分组、剔除 ID 计算、摘要滞后文案与引用徽章', () => {
    const sampleDocs = [
      {
        doc_id: 10,
        binding_id: 1,
        title: '全系统架构规范',
        doc_kind: 'arch_design' as const,
        current_version: 2,
        summary_md: '摘要',
        summary_stale: true,
        toc: [],
        estimated_tokens: 1500,
        is_home: true,
        is_inherited: true,
        effective_scope_level: 'system' as const,
        source_scope_label: '全系统',
        inherit_to_children: true,
        doc_type_slugs: [],
        can_edit: true,
        can_unbind: false,
      },
      {
        doc_id: 20,
        binding_id: 2,
        title: '订单模块边界',
        doc_kind: 'interface_spec' as const,
        current_version: 1,
        summary_md: '摘要',
        summary_stale: false,
        toc: [],
        estimated_tokens: 450,
        is_home: true,
        is_inherited: false,
        effective_scope_level: 'module' as const,
        source_scope_label: '订单模块',
        inherit_to_children: true,
        doc_type_slugs: ['system-architecture'],
        can_edit: true,
        can_unbind: false,
      },
      {
        doc_id: 30,
        binding_id: 3,
        title: '支付仓库基线',
        doc_kind: 'adr_constraint' as const,
        current_version: 3,
        summary_md: '摘要',
        summary_stale: false,
        toc: [],
        estimated_tokens: 800,
        is_home: false,
        is_inherited: true,
        effective_scope_level: 'repo' as const,
        source_scope_label: 'pay-svc',
        inherit_to_children: true,
        doc_type_slugs: [],
        can_edit: false,
        can_unbind: false,
      },
    ];

    const grouped = groupEffectiveRefDocsByScope(sampleDocs);
    assert.deepEqual(
      grouped.map((g) => ({ level: g.level, label: g.label, docIds: g.items.map((x) => x.doc_id) })),
      [
        { level: 'module', label: '功能模块', docIds: [20] },
        { level: 'repo', label: '代码仓', docIds: [30] },
        { level: 'system', label: '全系统', docIds: [10] },
      ],
    );

    const checked = new Set<number>([10, 20]);
    assert.deepEqual(buildExcludedRefDocIds(sampleDocs, checked), [30]);
    assert.deepEqual(computeIncludedRefDocStats(sampleDocs, checked), {
      includedCount: 2,
      includedTokens: 1950,
      totalCount: 3,
    });

    assert.equal(formatRefDocKindLabel('domain_glossary'), '领域术语');
    assert.equal(formatRefDocScopeLabel('subsystem'), '子系统');
    assert.equal(formatRefDocTokenBadge(1500), '1.5k T');
    assert.equal(formatRefDocTokenBadge(450), '450 T');
    assert.equal(
      formatRefDocSummaryStaleHint(1, 3),
      '摘要滞后于当前正文（摘要基于 v1，当前正文 v3）',
    );
    assert.equal(formatRefDocSummaryStaleHint(3, 3), '');
    assert.equal(
      formatArchNodeRefDocBadgeText({ direct_ref_doc_count: 2, inherited_ref_doc_count: 3 }),
      '参考 2+3',
    );
    assert.equal(
      formatRefCitationBadgeLabel({
        citation_key: 'ref_doc:10@v2#sec-1',
        doc_id: 10,
        version_no: 2,
        section_id: 'sec-1',
        section_title: '边界说明',
        doc_title: '全系统规范',
        scope_level: 'system',
        stale: true,
      }),
      '全系统规范 v2 · 边界说明 (已过期)',
    );
  });

  it('新增 7 个参考文档 API 错误码友好映射', () => {
    for (const code of [
      'HOME_BINDING_PROTECTED',
      'VERSION_CONFLICT',
      'ALREADY_BOUND',
      'PAYLOAD_TOO_LARGE',
      'INLINE_BASE64_IMAGE',
      'SECRET_DETECTED',
      'SECRET_CONFIRM_REQUIRED',
    ] as const) {
      const formatted = formatApiError(new ArchdocApiError(422, { code }));
      assert.equal(formatted.code, code);
      assert.ok(formatted.title.length > 0 && formatted.message.length > 0);
    }
  });
});
