import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import type { CanonicalFacetName, SlotDefinition } from '../types/domain.ts';
import {
  CANONICAL_FACET_NAMES,
  compileFromBlueprint,
  computeCanonicalFacetsUnion,
  decompileToBlueprint,
  detectUnconfirmedMissingSlotIds,
  formatBlueprintTemplatePreview,
  parseDSLToBlueprint,
  serializeBlueprintToDSL,
  shouldDefaultAutoFacetsUnion,
  type DocBlueprint,
} from './docTypeBlueprint.ts';

const BUILTIN_SLUGS = [
  'system-architecture',
  'data-and-contract',
  'ops-and-config',
  'code-walkthrough',
  'agents-context',
] as const;

describe('DocType 蓝图双模编解码与 DSL v1 纯函数 (T-ADR05-01 ~ T-ADR05-08)', () => {
  it('T-ADR05-01: 5 类内置 DocType 反编译与编译保持 100% 一致与连续往返幂等', () => {
    const builtinRoot = path.resolve(process.cwd(), '../pkg/doctype/builtin');

    for (const slug of BUILTIN_SLUGS) {
      const metaRaw = fs.readFileSync(path.join(builtinRoot, slug, 'meta.json'), 'utf-8');
      const templateRaw = fs.readFileSync(path.join(builtinRoot, slug, 'template.md'), 'utf-8');
      const parsedMeta = JSON.parse(metaRaw) as { slots: SlotDefinition[] };

      const { blueprint, diagnostics } = decompileToBlueprint(templateRaw, parsedMeta.slots);
      assert.equal(
        diagnostics.filter((d) => d.severity === 'error').length,
        0,
        `${slug} 反编译不应有 error 诊断`,
      );
      assert.equal(
        diagnostics.filter((d) => d.severity === 'warning').length,
        0,
        `${slug} 反编译不应有 warning 诊断`,
      );

      assert.ok(blueprint.blocks.length >= 2, `${slug} 至少包含主标题 static 与槽位块`);
      assert.equal(blueprint.blocks[0].kind, 'static', `${slug} 首块应为 H1 静态块`);
      const slotBlocks = blueprint.blocks.filter((b) => b.kind === 'slot');
      assert.equal(slotBlocks.length, parsedMeta.slots.length, `${slug} 槽位数量应一致`);
      for (const sb of slotBlocks) {
        assert.equal(sb.emitHeading, true, `${slug}/${sb.slot.slot_id} 应为 emitHeading=true`);
        assert.equal(sb.headingLevel, 2, `${slug}/${sb.slot.slot_id} 应为 H2 标题`);
      }

      const compiled1 = compileFromBlueprint(blueprint);
      assert.equal(compiled1.templateMD, templateRaw.replace(/\r\n/g, '\n'));
      assert.deepEqual(compiled1.slots, parsedMeta.slots);

      // 连续二次往返幂等
      const round2 = decompileToBlueprint(compiled1.templateMD, compiled1.slots);
      const compiled2 = compileFromBlueprint(round2.blueprint);
      assert.equal(compiled2.templateMD, compiled1.templateMD);
      assert.deepEqual(compiled2.slots, compiled1.slots);
    }
  });

  it('T-ADR05-02: 复杂自由排版 template.md（导语/免责声明/H3/无标题内嵌槽位/分叉标题）无损往返', () => {
    const complexTemplate = [
      '# 自定义架构评审模板',
      '',
      '本文档面向安全与架构联合评审会使用。',
      '',
      '### 1.1 接口面与鉴权边界',
      '<!-- slot:api_surface -->',
      '',
      '> 免责声明：以下段落由系统自动内嵌生成，请勿手工删除标记。',
      '<!-- slot:inline_disclaimer_target -->',
      '',
      '## 2. 数据库模型（模板旧标题）',
      '<!-- slot:storage_models -->',
      '',
    ].join('\n');

    const slots: SlotDefinition[] = [
      {
        slot_id: 'api_surface',
        title: '1.1 接口面与鉴权边界',
        ownership: 'auto',
        required: true,
        ir_facets: ['http_routes', 'idl_contracts'],
        token_budget: 8000,
        min_chars: 40,
        instruction_hint: '梳理接口',
      },
      {
        slot_id: 'inline_disclaimer_target',
        title: '内嵌风险清单',
        ownership: 'hybrid',
        required: true,
        ir_facets: ['config_keys'],
        token_budget: 6000,
        min_chars: 20,
        instruction_hint: '',
      },
      {
        slot_id: 'storage_models',
        title: '2. 核心数据实体与存储模型',
        ownership: 'auto',
        required: true,
        ir_facets: ['data_models'],
        token_budget: 8000,
        min_chars: 40,
        instruction_hint: '',
      },
    ];

    const { blueprint, diagnostics } = decompileToBlueprint(complexTemplate, slots);
    assert.equal(diagnostics.length, 0);

    const slotBlocks = blueprint.blocks.filter((b) => b.kind === 'slot');
    assert.equal(slotBlocks.length, 3);

    // H3 绑定槽位
    assert.equal(slotBlocks[0].emitHeading, true);
    assert.equal(slotBlocks[0].headingLevel, 3);
    assert.equal(slotBlocks[0].divergedTemplateHeading, undefined);

    // 自由内嵌槽位：紧邻上一非空行为引用文本，非 2~4 级标题 -> emitHeading=false，且前置静态文案保留
    assert.equal(slotBlocks[1].emitHeading, false);
    assert.equal(slotBlocks[1].headingLevel, 2);

    // 标题分叉槽位：记录 divergedTemplateHeading，且保留 slot.title 作为主值
    assert.equal(slotBlocks[2].emitHeading, true);
    assert.equal(slotBlocks[2].headingLevel, 2);
    assert.equal(slotBlocks[2].divergedTemplateHeading, '2. 数据库模型（模板旧标题）');
    assert.equal(slotBlocks[2].slot.title, '2. 核心数据实体与存储模型');

    // 验证前置多段导语和免责声明静态块均 100% 保留
    const staticBlocks = blueprint.blocks.filter((b) => b.kind === 'static');
    assert.equal(staticBlocks.length, 2);
    assert.match(staticBlocks[0].markdown, /本文档面向安全与架构联合评审会使用。/);
    assert.match(staticBlocks[1].markdown, /> 免责声明：以下段落由系统自动内嵌生成/);

    // 编译时 slot 2 的标题会统一对齐为 slot.title
    const compiled = compileFromBlueprint(blueprint);
    assert.ok(compiled.templateMD.includes('### 1.1 接口面与鉴权边界\n<!-- slot:api_surface -->'));
    assert.ok(compiled.templateMD.includes('> 免责声明：以下段落由系统自动内嵌生成，请勿手工删除标记。\n\n<!-- slot:inline_disclaimer_target -->'));
    assert.ok(compiled.templateMD.includes('## 2. 核心数据实体与存储模型\n<!-- slot:storage_models -->'));
  });

  it('T-ADR05-03: 蓝图重排槽位顺序并修改 title 后编译，slots[] 与 templateMD 100% 同步', () => {
    const bp: DocBlueprint = {
      blocks: [
        { kind: 'static', key: 'st_1', markdown: '# 同步测试文档' },
        {
          kind: 'slot',
          key: 'sl_b',
          emitHeading: true,
          headingLevel: 2,
          slot: {
            slot_id: 'second_first',
            title: '1. 重排后置顶章节（新标题）',
            ownership: 'hybrid',
            required: true,
            ir_facets: ['symbols'],
            token_budget: 10000,
            min_chars: 40,
            instruction_hint: '先讲核心链路',
          },
        },
        {
          kind: 'slot',
          key: 'sl_a',
          emitHeading: true,
          headingLevel: 3,
          slot: {
            slot_id: 'first_second',
            title: '1.1 次要章节',
            ownership: 'auto',
            required: false,
            ir_facets: ['tree'],
            token_budget: 6000,
            min_chars: 20,
            instruction_hint: '',
          },
        },
      ],
    };

    const { templateMD, slots } = compileFromBlueprint(bp);
    assert.deepEqual(
      slots.map((s) => s.slot_id),
      ['second_first', 'first_second'],
    );
    assert.equal(slots[0].title, '1. 重排后置顶章节（新标题）');
    const idx1 = templateMD.indexOf('## 1. 重排后置顶章节（新标题）\n<!-- slot:second_first -->');
    const idx2 = templateMD.indexOf('### 1.1 次要章节\n<!-- slot:first_second -->');
    assert.ok(idx1 > 0 && idx2 > idx1, 'templateMD 中标题与标记出现顺序必须严格一致');
  });

  it('T-ADR05-04: parseDSLToBlueprint 正确解析完整语法、别名、多行列表、hint:|、4反引号与普通围栏透传', () => {
    const dsl = [
      '# 参数化 DSL 完整示例',
      '',
      '```mermaid',
      'flowchart LR',
      '  A["示例伪代码 ```slot fake_slot"] --> B',
      '```',
      '',
      '````markdown',
      '普通 markdown 示例中的槽位演示不应该被解析：',
      '```slot not_a_real_slot',
      'ir_facets: [tree]',
      '```',
      '````',
      '',
      '## 1. 完整字段与别名写法',
      '',
      '```slot full_alias_slot',
      'ownership: hybrid',
      'required: false',
      'facets:',
      '  - tree',
      '  - deps',
      'budget: 12000',
      'min_chars: 80',
      'source_tools: true',
      'tool_rounds: 5',
      'instruction_hint: |',
      '  第一行写作提示',
      '  第二行含缩进提示',
      '```',
      '',
      '### 2. 极简缺省写法',
      '',
      '```slot minimal_slot',
      'ir_facets: [http_routes, idl_contracts, config_keys]',
      'hint: 梳理全部外部入口。',
      '```',
      '',
      '固定内嵌声明段落：',
      '',
      '````slot embedded_backtick_slot',
      'title: 内嵌代码块槽位',
      'ir_facets: [build]',
      'hint: |',
      '  示例输出需包含：',
      '  ```bash',
      '  make test',
      '  ```',
      '````',
    ].join('\n');

    const { blueprint, diagnostics } = parseDSLToBlueprint(dsl);
    assert.equal(
      diagnostics.filter((d) => d.severity === 'error').length,
      0,
      `不应报错，实际诊断: ${JSON.stringify(diagnostics)}`,
    );
    assert.ok(blueprint);

    const slotBlocks = blueprint.blocks.filter((b) => b.kind === 'slot');
    assert.equal(slotBlocks.length, 3, '仅解析 3 个真实 slot 块，普通围栏内的示例必须透传忽略');

    // 1. 别名与完整字段
    const s1 = slotBlocks[0];
    assert.equal(s1.emitHeading, true);
    assert.equal(s1.headingLevel, 2);
    assert.equal(s1.slot.slot_id, 'full_alias_slot');
    assert.equal(s1.slot.title, '1. 完整字段与别名写法');
    assert.equal(s1.slot.ownership, 'hybrid');
    assert.equal(s1.slot.required, false);
    assert.deepEqual(s1.slot.ir_facets, ['tree', 'deps']);
    assert.equal(s1.slot.token_budget, 12000);
    assert.equal(s1.slot.min_chars, 80);
    assert.equal(s1.slot.enable_source_tools, true);
    assert.equal(s1.slot.max_tool_rounds, 5);
    assert.equal(s1.slot.instruction_hint, '第一行写作提示\n第二行含缩进提示');

    // 2. 极简缺省值（ADR-05 表 4.3）
    const s2 = slotBlocks[1];
    assert.equal(s2.emitHeading, true);
    assert.equal(s2.headingLevel, 3);
    assert.equal(s2.slot.title, '2. 极简缺省写法');
    assert.equal(s2.slot.ownership, 'auto');
    assert.equal(s2.slot.required, true);
    assert.equal(s2.slot.token_budget, 8000);
    assert.equal(s2.slot.min_chars, 40);
    assert.equal(s2.slot.enable_source_tools, false);
    assert.equal(s2.slot.max_tool_rounds, 0);
    assert.equal(s2.slot.instruction_hint, '梳理全部外部入口。');

    // 3. 4 反引号 + 无标题内嵌
    const s3 = slotBlocks[2];
    assert.equal(s3.emitHeading, false);
    assert.equal(s3.slot.title, '内嵌代码块槽位');
    assert.match(s3.slot.instruction_hint ?? '', /```bash\nmake test\n```/);

    // 普通代码围栏仍完整保存在 static 块中
    const staticText = blueprint.blocks
      .filter((b) => b.kind === 'static')
      .map((b) => b.markdown)
      .join('\n\n');
    assert.ok(staticText.includes('```slot not_a_real_slot'));
    assert.ok(staticText.includes('```mermaid'));
  });

  it('T-ADR05-05: serializeBlueprintToDSL 规范化输出与连续往返字节级幂等', () => {
    const bp: DocBlueprint = {
      blocks: [
        { kind: 'static', key: 's0', markdown: '# 幂等规范化文档' },
        {
          kind: 'slot',
          key: 's1',
          emitHeading: true,
          headingLevel: 2,
          slot: {
            slot_id: 'runtime_concurrency',
            title: '1. 并发模型',
            ownership: 'auto',
            required: true,
            ir_facets: ['runtime_primitives', 'symbols'],
            token_budget: 10000,
            min_chars: 40,
            enable_source_tools: true,
            max_tool_rounds: 4,
            instruction_hint: '第一行\n```cpp\nstd::mutex mu;\n```\n超过八十字符验证自动多行块输出及外层反引号升级防逃逸保护机制是否正常工作。',
          },
        },
        { kind: 'static', key: 's2', markdown: '> 固定说明' },
        {
          kind: 'slot',
          key: 's3',
          emitHeading: false,
          headingLevel: 2,
          slot: {
            slot_id: 'embedded_note',
            title: '内嵌说明槽位',
            ownership: 'locked',
            required: false,
            ir_facets: ['tree'],
            token_budget: 4000,
            min_chars: 20,
            enable_source_tools: false,
            max_tool_rounds: 0,
            instruction_hint: '单行简短提示',
          },
        },
      ],
    };

    const dsl1 = serializeBlueprintToDSL(bp);
    // emitHeading=true 不输出冗余 title:
    assert.ok(!dsl1.includes('title: 1. 并发模型'));
    // emitHeading=false 显式输出 title:
    assert.ok(dsl1.includes('title: 内嵌说明槽位'));
    // hint 含 ``` 3 反引号时，外层围栏自动升级为 ```` (4 反引号)
    assert.ok(dsl1.includes('````slot runtime_concurrency'));
    assert.ok(dsl1.includes('hint: |'));
    // enable_source_tools=false 省略 max_tool_rounds
    assert.ok(dsl1.includes('enable_source_tools: false'));
    const embeddedSection = dsl1.slice(dsl1.indexOf('```slot embedded_note'));
    assert.ok(!embeddedSection.includes('max_tool_rounds:'));

    const parsed1 = parseDSLToBlueprint(dsl1);
    assert.equal(parsed1.diagnostics.filter((d) => d.severity === 'error').length, 0);
    assert.ok(parsed1.blueprint);
    const dsl2 = serializeBlueprintToDSL(parsed1.blueprint);
    assert.equal(dsl2, dsl1, 'serialize(parse(serialize(bp))) 必须字节级完全相等');
  });

  it('T-ADR05-06: 非法 DSL 输入产生精确行号与 Levenshtein 纠错建议诊断', () => {
    // 1. 未知键 token_budegt + 非法分片 symbol + module_context + 越界数值
    const badDsl1 = [
      '# 非法输入测试',
      '',
      '## 1. 测试章节',
      '```slot bad_slot_one',
      'token_budegt: 8000',
      'ir_facets: [symbol, module_context]',
      'token_budget: 100',
      'max_tool_rounds: 9',
      '```',
    ].join('\n');
    const r1 = parseDSLToBlueprint(badDsl1);
    assert.equal(r1.blueprint, null);
    const err1 = r1.diagnostics.filter((d) => d.severity === 'error');
    assert.ok(err1.some((d) => d.line === 5 && d.message.includes('token_budegt')));
    assert.ok(
      err1.some((d) => d.line === 6 && d.message.includes('symbol') && d.message.includes('symbols')),
      '应给出 symbols Levenshtein 纠错建议',
    );
    assert.ok(err1.some((d) => d.line === 6 && d.message.includes('module_context')));
    assert.ok(err1.some((d) => d.line === 7 && d.message.includes('500')));
    assert.ok(err1.some((d) => d.line === 8 && d.message.includes('6')));

    // 2. 非法 slot_id、重复 slot_id、无标题槽位缺 title、hint 超 1000 字
    const longHint = 'a'.repeat(1005);
    const badDsl2 = [
      '```slot Invalid-ID',
      'title: 非法ID',
      'ir_facets: [tree]',
      '```',
      '',
      '## A',
      '```slot dup_id',
      'ir_facets: [tree]',
      '```',
      '',
      '## B',
      '```slot dup_id',
      'ir_facets: [tree]',
      '```',
      '',
      '纯正文无标题',
      '```slot no_title_slot',
      'ir_facets: [tree]',
      `hint: ${longHint}`,
      '```',
    ].join('\n');
    const r2 = parseDSLToBlueprint(badDsl2);
    assert.equal(r2.blueprint, null);
    const err2 = r2.diagnostics.filter((d) => d.severity === 'error');
    assert.ok(err2.some((d) => d.line === 1));
    assert.ok(err2.some((d) => d.line === 12 && d.message.includes('dup_id')));
    assert.ok(err2.some((d) => d.line === 17 && d.message.includes('title')));
    assert.ok(err2.some((d) => d.line === 19 && d.message.includes('1000')));

    // 3. 未闭合 slot 围栏
    const unclosedDsl = [
      '## 1. 未闭合',
      '```slot unclosed_slot',
      'ir_facets: [tree]',
    ].join('\n');
    const r3 = parseDSLToBlueprint(unclosedDsl);
    assert.equal(r3.blueprint, null);
    assert.ok(r3.diagnostics.some((d) => d.severity === 'error' && d.line === 2));
  });

  it('T-ADR05-07: 顶层 ir_facets 自动并集计算与存量手动模式判定', () => {
    const slots: SlotDefinition[] = [
      {
        slot_id: 's1',
        title: '1',
        ownership: 'auto',
        required: true,
        ir_facets: ['symbols', 'tree', 'module_context'],
        token_budget: 8000,
        min_chars: 40,
      },
      {
        slot_id: 's2',
        title: '2',
        ownership: 'auto',
        required: true,
        ir_facets: ['deps', 'tree', 'http_routes'],
        token_budget: 8000,
        min_chars: 40,
      },
    ];

    const union = computeCanonicalFacetsUnion(slots);
    assert.deepEqual(union, ['tree', 'deps', 'symbols', 'http_routes'] as CanonicalFacetName[]);
    // 确保按 CANONICAL_FACET_NAMES 标准顺序排序且不含 module_context
    assert.ok(CANONICAL_FACET_NAMES.length === 9);
    assert.equal(shouldDefaultAutoFacetsUnion(['http_routes', 'tree', 'deps', 'symbols'], slots), true);
    assert.equal(shouldDefaultAutoFacetsUnion([], slots), true);
    assert.equal(shouldDefaultAutoFacetsUnion(undefined, slots), true);
    // 若磁盘 meta.ir_facets 与槽位并集不一致且非空，自动判定为手动模式（返回 false）
    assert.equal(shouldDefaultAutoFacetsUnion(['tree', 'deps'], slots), false);
  });

  it('T-ADR05-08: 跨模式 persistedSlotIds 变更差集检测与回改清零', () => {
    const persisted = new Set(['overview_boundaries', 'runtime_concurrency', 'core_request_flows']);
    const slotsRenamed: SlotDefinition[] = [
      {
        slot_id: 'overview_boundaries',
        title: '1',
        ownership: 'auto',
        required: true,
        ir_facets: ['tree'],
        token_budget: 8000,
        min_chars: 40,
      },
      {
        slot_id: 'runtime_concurrency_v2',
        title: '2',
        ownership: 'auto',
        required: true,
        ir_facets: ['symbols'],
        token_budget: 8000,
        min_chars: 40,
      },
    ];

    assert.deepEqual(
      detectUnconfirmedMissingSlotIds(persisted, slotsRenamed),
      ['runtime_concurrency', 'core_request_flows'],
    );
    assert.deepEqual(
      detectUnconfirmedMissingSlotIds(persisted, slotsRenamed, new Set(['core_request_flows'])),
      ['runtime_concurrency'],
    );

    // 改回原 slot_id 后差集自动清零
    const slotsRestored: SlotDefinition[] = [
      ...slotsRenamed.slice(0, 1),
      { ...slotsRenamed[1], slot_id: 'runtime_concurrency' },
      { ...slotsRenamed[0], slot_id: 'core_request_flows' },
    ];
    assert.deepEqual(detectUnconfirmedMissingSlotIds(persisted, slotsRestored), []);
  });

  it('formatBlueprintTemplatePreview: 支持按开关附带章节写作指引 (hint) 且不污染编译产物', () => {
    const bp: DocBlueprint = {
      blocks: [
        {
          kind: 'static',
          key: 'st_title',
          markdown: '\n# 系统架构与核心链路\n\n',
        },
        {
          kind: 'slot',
          key: 'sl_single',
          emitHeading: true,
          headingLevel: 2,
          slot: {
            slot_id: 'overview_boundaries',
            title: '1. 系统定位与模块分层边界',
            ownership: 'auto',
            required: true,
            ir_facets: ['tree', 'deps'],
            token_budget: 8000,
            min_chars: 40,
            instruction_hint: '  结合目录树与依赖说明系统边界。  ',
          },
        },
        {
          kind: 'slot',
          key: 'sl_multi',
          emitHeading: true,
          headingLevel: 3,
          slot: {
            slot_id: 'runtime_concurrency',
            title: '2. 运行时并发模型',
            ownership: 'auto',
            required: true,
            ir_facets: ['runtime_primitives'],
            token_budget: 10000,
            min_chars: 40,
            instruction_hint: '第一行：梳理线程池。\r\n\r\n第二行：核实锁粒度与原子操作。',
          },
        },
        {
          kind: 'slot',
          key: 'sl_blank_hint',
          emitHeading: true,
          headingLevel: 2,
          slot: {
            slot_id: 'empty_hint_slot',
            title: '3. 空白指引章节',
            ownership: 'auto',
            required: false,
            ir_facets: ['tree'],
            token_budget: 4000,
            min_chars: 0,
            instruction_hint: '   \n\t  ',
          },
        },
        {
          kind: 'slot',
          key: 'sl_inline',
          emitHeading: false,
          headingLevel: 2,
          slot: {
            slot_id: 'inline_note',
            title: '内嵌补充',
            ownership: 'hybrid',
            required: true,
            ir_facets: ['config_keys'],
            token_budget: 4000,
            min_chars: 20,
            instruction_hint: '列出关键运行配置默认值。',
          },
        },
      ],
    };

    const bpSnapshot = structuredClone(bp);
    const compiled = compileFromBlueprint(bp);

    // 省略 options 或 includeHints=false 时，与 compileFromBlueprint(bp).templateMD 字节级一致
    assert.equal(formatBlueprintTemplatePreview(bp), compiled.templateMD);
    assert.equal(formatBlueprintTemplatePreview(bp, { includeHints: false }), compiled.templateMD);
    assert.ok(!compiled.templateMD.includes('[写作指引]'));

    // includeHints=true 时在预览文本中展示各章节写作指引
    const withHints = formatBlueprintTemplatePreview(bp, { includeHints: true });
    const expected = [
      '# 系统架构与核心链路',
      '',
      '## 1. 系统定位与模块分层边界',
      '<!-- slot:overview_boundaries -->',
      '> [写作指引] 结合目录树与依赖说明系统边界。',
      '',
      '### 2. 运行时并发模型',
      '<!-- slot:runtime_concurrency -->',
      '> [写作指引]',
      '> 第一行：梳理线程池。',
      '>',
      '> 第二行：核实锁粒度与原子操作。',
      '',
      '## 3. 空白指引章节',
      '<!-- slot:empty_hint_slot -->',
      '',
      '<!-- slot:inline_note -->',
      '> [写作指引] 列出关键运行配置默认值。',
      '',
    ].join('\n');

    assert.equal(withHints, expected);

    // 空蓝图返回空串
    assert.equal(formatBlueprintTemplatePreview({ blocks: [] }, { includeHints: true }), '');
    assert.equal(formatBlueprintTemplatePreview({ blocks: [] }), '');

    // 原 compileFromBlueprint 结果与蓝图对象均未被修改
    assert.ok(!compileFromBlueprint(bp).templateMD.includes('[写作指引]'));
    assert.deepEqual(bp, bpSnapshot);
  });
});
