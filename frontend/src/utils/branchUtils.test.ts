import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import type { RepoBranchItemVO } from '../types/index.ts';
import {
  buildBranchSwitchConfirmContent,
  formatBranchBadgeLabel,
  sortAndFilterBranches,
} from './branchUtils.ts';

describe('分支切换工具函数 (branchUtils)', () => {
  const sampleBranches: RepoBranchItemVO[] = [
    {
      name: 'release/v2.0',
      commit_sha: 'aaaa1111bbbb2222',
      is_head: false,
      is_default: false,
      is_current: false,
    },
    {
      name: 'main',
      commit_sha: 'cccc3333dddd4444',
      is_head: true,
      is_default: true,
      is_current: false,
    },
    {
      name: 'feat/arch-switch',
      commit_sha: 'eeee5555ffff6666',
      is_head: false,
      is_default: false,
      is_current: true,
    },
    {
      name: 'dev',
      commit_sha: '1111222233334444',
      is_head: false,
      is_default: false,
      is_current: false,
    },
  ];

  it('sortAndFilterBranches 将当前分支置顶、默认分支次之，其余按字典序排列', () => {
    const sorted = sortAndFilterBranches(sampleBranches, '');
    assert.deepEqual(
      sorted.map((item) => item.name),
      ['feat/arch-switch', 'main', 'dev', 'release/v2.0'],
    );
  });

  it('sortAndFilterBranches 支持大小写不敏感关键字过滤且不修改原数组', () => {
    const before = sampleBranches.map((item) => item.name);
    const filtered = sortAndFilterBranches(sampleBranches, '  REL ');
    assert.deepEqual(
      filtered.map((item) => item.name),
      ['release/v2.0'],
    );
    assert.deepEqual(
      sampleBranches.map((item) => item.name),
      before,
    );
  });

  it('formatBranchBadgeLabel 正确区分默认与自定义跟踪分支', () => {
    const customBadge = formatBranchBadgeLabel(true, 'main');
    assert.equal(customBadge.label, '自定义');
    assert.match(customBadge.title, /main/);

    const defaultBadge = formatBranchBadgeLabel(false, 'main');
    assert.equal(defaultBadge.label, '默认');
    assert.match(defaultBadge.title, /main/);
  });

  it('buildBranchSwitchConfirmContent 区分切换自定义分支与恢复默认分支文案', () => {
    const switchMsg = buildBranchSwitchConfirmContent({
      repoName: 'code-archdoc',
      currentBranch: 'main',
      targetBranch: 'feat/v2',
      defaultBranch: 'main',
    });
    assert.equal(switchMsg.title, '确认切换仓库跟踪分支');
    assert.match(switchMsg.content, /main 切换为 feat\/v2/);
    assert.match(switchMsg.content, /无损保留/);

    const resetMsg = buildBranchSwitchConfirmContent({
      repoName: 'code-archdoc',
      currentBranch: 'feat/v2',
      targetBranch: 'main',
      defaultBranch: 'main',
    });
    assert.equal(resetMsg.title, '确认恢复仓库默认分支');
    assert.match(resetMsg.content, /恢复为默认分支 main/);

    const resetEmptyMsg = buildBranchSwitchConfirmContent({
      repoName: 'code-archdoc',
      currentBranch: 'feat/v2',
      targetBranch: '',
      defaultBranch: 'main',
    });
    assert.equal(resetEmptyMsg.title, '确认恢复仓库默认分支');
    assert.match(resetEmptyMsg.content, /默认分支 main/);
  });
});
