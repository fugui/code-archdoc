import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { parseMarkdownBlocks } from './markdown.ts';
import {
  buildBlockAnchor,
  buildInlineAnchorFromText,
  extractBlockPlainText,
  locateAnchorInBlocks,
  splitTextWithHighlights,
} from './reviewAnchor.ts';

describe('T-ADR06-01: Markdown AST 行号偏移与 ReviewCommentAnchor 构造', () => {
  it('准确记录标题、段落、嵌套 blockquote、表格、Mermaid 与代码块的 1-based 行号', () => {
    const md = [
      '## 1. 核心流程', // line 1
      '', // line 2
      '调用 `OrderService::Pay` 完成 **核心支付** 扣款。', // line 3
      '', // line 4
      '> 第一层引用说明', // line 5
      '> > 嵌套引用 `retry=3`', // line 6
      '', // line 7
      '| 字段 | 说明 |', // line 8
      '| :--- | :--- |', // line 9
      '| order_id | 订单号 |', // line 10
      '', // line 11
      '```mermaid', // line 12
      'flowchart LR', // line 13
      '  A --> B', // line 14
      '```', // line 15
      '', // line 16
      '```cpp', // line 17
      'int pay() { return 0; }', // line 18
      '```', // line 19
    ].join('\n');

    const blocks = parseMarkdownBlocks(md);
    assert.equal(blocks.length, 6);
    assert.deepEqual(
      { start: blocks[0].startLine, end: blocks[0].endLine },
      { start: 1, end: 1 },
    );
    assert.deepEqual(
      { start: blocks[1].startLine, end: blocks[1].endLine },
      { start: 3, end: 3 },
    );

    const quoteBlock = blocks[2];
    assert.equal(quoteBlock.type, 'blockquote');
    assert.deepEqual({ start: quoteBlock.startLine, end: quoteBlock.endLine }, { start: 5, end: 6 });
    if (quoteBlock.type === 'blockquote') {
      assert.equal(quoteBlock.blocks[0].startLine, 5);
      assert.equal(quoteBlock.blocks[1].type, 'blockquote');
      assert.equal(quoteBlock.blocks[1].startLine, 6);
      assert.equal(quoteBlock.blocks[1].endLine, 6);
    }

    assert.deepEqual({ start: blocks[3].startLine, end: blocks[3].endLine }, { start: 8, end: 10 });
    assert.deepEqual({ start: blocks[4].startLine, end: blocks[4].endLine }, { start: 12, end: 15 });
    assert.deepEqual({ start: blocks[5].startLine, end: blocks[5].endLine }, { start: 17, end: 19 });

    // 跨 `code` 与 **strong** 行内标签捕获
    const paraPlain = extractBlockPlainText(blocks[1]);
    assert.equal(paraPlain, '调用 OrderService::Pay 完成 核心支付 扣款。');
    const quoteTarget = 'OrderService::Pay 完成 核心支付';
    const startOff = paraPlain.indexOf(quoteTarget);
    const anchor = buildInlineAnchorFromText(
      paraPlain,
      startOff,
      startOff + quoteTarget.length,
      1,
      'paragraph',
      blocks[1].startLine,
      blocks[1].endLine,
    );
    assert.equal(anchor.block_index, 1);
    assert.equal(anchor.block_type, 'paragraph');
    assert.equal(anchor.quote, quoteTarget);
    assert.equal(anchor.prefix, '调用 ');
    assert.equal(anchor.suffix, ' 扣款。');
    assert.equal(anchor.source_line_start, 3);

    // 块级锚点生成（Mermaid / 表格）
    const tableAnchor = buildBlockAnchor(blocks[3], 3);
    assert.equal(tableAnchor.block_index, 3);
    assert.equal(tableAnchor.block_type, 'table');
    assert.equal(tableAnchor.quote, '[表格: 字段 / 说明]');

    const mermaidAnchor = buildBlockAnchor(blocks[4], 4);
    assert.equal(mermaidAnchor.block_type, 'mermaid');
    assert.equal(mermaidAnchor.quote, '[Mermaid 图表]');
  });

  it('超过 500 字符的划选 quote 会被安全截断到 500 字符上限', () => {
    const longText = 'A'.repeat(650);
    const anchor = buildInlineAnchorFromText(longText, 0, 620, 0, 'paragraph');
    assert.equal(anchor.quote?.length, 500);
  });
});

describe('T-ADR06-02: 三级锚定降级与跨行内节点偏移区间投影', () => {
  it('Level 1 精确匹配、Level 2 跨块重排容错与 Level 3 detached 降级', () => {
    const mdV1 = [
      '第一段说明整体边界。',
      '',
      '体量最大（1,919 LOC）的模块为 `CanvasStudioPage`，需补充注释。',
    ].join('\n');
    const blocksV1 = parseMarkdownBlocks(mdV1);
    const plainV1 = extractBlockPlainText(blocksV1[1]);
    const q = '1,919 LOC）的模块为 CanvasStudioPage';
    const s = plainV1.indexOf(q);
    const anchor = buildInlineAnchorFromText(plainV1, s, s + q.length, 1, 'paragraph');

    // Level 1: 同块精确命中
    const loc1 = locateAnchorInBlocks(blocksV1, anchor);
    assert.equal(loc1.matchLevel, 'exact');
    assert.equal(loc1.blockIndex, 1);
    assert.equal(loc1.startOffset, s);
    assert.equal(loc1.endOffset, s + q.length);

    // 跨行内节点偏移区间投影（跨越普通 text 与 code 节点）
    const segsText = splitTextWithHighlights('体量最大（1,919 LOC）的模块为 ', 0, [
      { commentId: 10, commentNo: 1, startOffset: s, endOffset: s + q.length },
    ]);
    assert.equal(segsText.length, 2);
    assert.equal(segsText[0].activeComments.length, 0);
    assert.equal(segsText[1].text, '1,919 LOC）的模块为 ');
    assert.equal(segsText[1].activeComments[0].commentNo, 1);
    assert.equal(segsText[1].endedComments.length, 0);

    const codeStart = '体量最大（1,919 LOC）的模块为 '.length;
    const segsCode = splitTextWithHighlights('CanvasStudioPage', codeStart, [
      { commentId: 10, commentNo: 1, startOffset: s, endOffset: s + q.length },
    ]);
    assert.equal(segsCode.length, 1);
    assert.equal(segsCode[0].activeComments[0].commentNo, 1);
    assert.equal(segsCode[0].endedComments[0].commentNo, 1);

    // Level 2: 前部插入 2 个新段落导致 block_index 后移
    const mdV2 = [
      '新增导语段落一。',
      '',
      '新增导语段落二。',
      '',
      '第一段说明整体边界。',
      '',
      '体量最大（1,919 LOC）的模块为 `CanvasStudioPage`，需补充注释。',
    ].join('\n');
    const blocksV2 = parseMarkdownBlocks(mdV2);
    const loc2 = locateAnchorInBlocks(blocksV2, anchor);
    assert.equal(loc2.matchLevel, 'relocated');
    assert.equal(loc2.blockIndex, 3);

    // Level 3: 目标文本被完全删改后返回 detached
    const mdV3 = '本章已被彻底重写为全新内容，不包含旧文本。';
    const blocksV3 = parseMarkdownBlocks(mdV3);
    const loc3 = locateAnchorInBlocks(blocksV3, anchor);
    assert.equal(loc3.matchLevel, 'detached');
    assert.equal(loc3.blockIndex, -1);
  });
});
