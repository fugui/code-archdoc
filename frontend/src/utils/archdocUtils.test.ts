import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import {
  DEFAULT_ARCHDOC_PAGE_SIZE,
  buildStudioPath,
  parseDriftFilterParams,
  parseEvidenceDeepLinkParams,
  parseEvidenceHref,
  parseLineRange,
  parseRepoFilterParams,
  serializeDriftFilterParams,
  serializeRepoFilterParams,
} from './urlQuery.ts';
import { ArchdocApiError, formatApiError, formatTaskErrorCode } from './errorMessages.ts';
import { isSafeLinkHref, parseMarkdownBlocks } from './markdown.ts';
import { parseChatSSEBlock } from './sseParser.ts';

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

    const populated = parseRepoFilterParams(
      new URLSearchParams('page=3&pageSize=50&search=order&department_id=10&doc_type=system-architecture&status=approved&scope_type=subdir'),
    );
    assert.deepEqual(populated, {
      page: 3,
      pageSize: 50,
      search: 'order',
      departmentId: '10',
      docType: 'system-architecture',
      status: 'approved',
      scopeType: 'subdir',
    });
  });

  it('非法页码、非白名单 pageSize 与非法枚举自动安全兜底', () => {
    const parsed = parseRepoFilterParams(
      new URLSearchParams('page=-4&pageSize=999&department_id=abc&status=invalid&scope_type=unknown'),
    );
    assert.equal(parsed.page, 1);
    assert.equal(parsed.pageSize, 25);
    assert.equal(parsed.departmentId, '');
    assert.equal(parsed.status, '');
    assert.equal(parsed.scopeType, '');
  });

  it('序列化支持增量更新筛选并保持 URL 双向同步往返一致', () => {
    const initial = new URLSearchParams('page=4&pageSize=100&search=pay');
    const updated = serializeRepoFilterParams(
      { page: 1, departmentId: '12', status: 'draft', docType: 'data-and-contract' },
      initial,
    );
    assert.equal(updated.get('page'), '1');
    assert.equal(updated.get('pageSize'), '100');
    assert.equal(updated.get('search'), 'pay');
    assert.equal(updated.get('department_id'), '12');
    assert.equal(updated.get('status'), 'draft');
    assert.equal(updated.get('doc_type'), 'data-and-contract');

    const roundTrip = parseRepoFilterParams(updated);
    assert.equal(roundTrip.page, 1);
    assert.equal(roundTrip.pageSize, 100);
    assert.equal(roundTrip.search, 'pay');
    assert.equal(roundTrip.departmentId, '12');
    assert.equal(roundTrip.status, 'draft');
    assert.equal(roundTrip.docType, 'data-and-contract');
  });

  it('生成 Studio 路径兼顾独立态与门户嵌入态', () => {
    assert.equal(buildStudioPath(false, 101, { docId: 42 }), '/studio/101/docs/42');
    assert.equal(buildStudioPath(true, 101, { docId: 42 }), '/archdoc/studio/101/docs/42');
    assert.equal(buildStudioPath(false, 101, { subdirectory: 'svc/order' }), '/studio/101?subdir=svc%2Forder');
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
      'event: meta\ndata: {"session_id":12,"message_id":88,"slot_id":"core_request_flows","base_revision_no":4,"model_tier":"flagship"}',
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
