import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import {
  MAX_PG_INT,
  parseStrictInt,
  validateHttpBaseUrl,
  validateIntValue,
  validateOptionalInt,
  validateRequiredInt,
} from './numberInput.ts';

describe('整数解析与表单数值输入校验工具 (numberInput)', () => {
  it('parseStrictInt 正确解析整数并拒绝小数、科学计数法及超安全整数', () => {
    assert.equal(parseStrictInt('200000'), 200000);
    assert.equal(parseStrictInt('  2010  '), 2010);
    assert.equal(parseStrictInt('0'), 0);
    assert.equal(parseStrictInt('-0'), 0);
    assert.equal(parseStrictInt('-42'), -42);

    assert.equal(parseStrictInt(''), null);
    assert.equal(parseStrictInt('   '), null);
    assert.equal(parseStrictInt('-'), null);
    assert.equal(parseStrictInt('200000.5'), null);
    assert.equal(parseStrictInt('1e5'), null);
    assert.equal(parseStrictInt('2000abc'), null);
    assert.equal(parseStrictInt('9007199254740992'), null);
  });

  it('回归截图用例：maxTokensPerTask 与 maxFilesPerTask 允许任意合法整数而不受 step 限制', () => {
    const tokenOpts = { label: '单任务 Token 熔断上限', min: 1000, max: MAX_PG_INT };
    assert.deepEqual(validateRequiredInt('200000', tokenOpts), { ok: true, value: 200000 });
    assert.deepEqual(validateRequiredInt('128000', tokenOpts), { ok: true, value: 128000 });
    assert.deepEqual(validateRequiredInt('1000', tokenOpts), { ok: true, value: 1000 });
    assert.deepEqual(validateRequiredInt('500', tokenOpts), {
      ok: false,
      error: '单任务 Token 熔断上限不能小于 1000',
    });
    assert.deepEqual(validateRequiredInt('', tokenOpts), {
      ok: false,
      error: '单任务 Token 熔断上限不能为空',
    });
    assert.deepEqual(validateRequiredInt('200000.5', tokenOpts), {
      ok: false,
      error: '单任务 Token 熔断上限必须为整数',
    });
    assert.deepEqual(validateRequiredInt('2147483648', tokenOpts), {
      ok: false,
      error: `单任务 Token 熔断上限不能大于 ${MAX_PG_INT}`,
    });

    const fileOpts = { label: '单任务有效源码文件数上限', min: 1, max: MAX_PG_INT };
    assert.deepEqual(validateRequiredInt('2000', fileOpts), { ok: true, value: 2000 });
    assert.deepEqual(validateRequiredInt('2010', fileOpts), { ok: true, value: 2010 });
    assert.deepEqual(validateRequiredInt('1', fileOpts), { ok: true, value: 1 });
    assert.deepEqual(validateRequiredInt('0', fileOpts), {
      ok: false,
      error: '单任务有效源码文件数上限不能小于 1',
    });
    assert.deepEqual(validateRequiredInt('-1', fileOpts), {
      ok: false,
      error: '单任务有效源码文件数上限不能小于 1',
    });
    assert.deepEqual(validateRequiredInt('', fileOpts), {
      ok: false,
      error: '单任务有效源码文件数上限不能为空',
    });
  });

  it('有界区间校验正确拦截越界并放行边界值与非整数步长值', () => {
    const timeoutOpts = { label: '单次调用超时（秒）', min: 5, max: 900 };
    assert.deepEqual(validateRequiredInt('5', timeoutOpts), { ok: true, value: 5 });
    assert.deepEqual(validateRequiredInt('900', timeoutOpts), { ok: true, value: 900 });
    assert.deepEqual(validateRequiredInt('4', timeoutOpts), {
      ok: false,
      error: '单次调用超时（秒）须在 5 ~ 900 之间',
    });
    assert.deepEqual(validateRequiredInt('901', timeoutOpts), {
      ok: false,
      error: '单次调用超时（秒）须在 5 ~ 900 之间',
    });

    const retriesOpts = { label: '失败最大重试次数', min: 0, max: 10 };
    assert.deepEqual(validateRequiredInt('0', retriesOpts), { ok: true, value: 0 });
    assert.deepEqual(validateRequiredInt('10', retriesOpts), { ok: true, value: 10 });
    assert.deepEqual(validateRequiredInt('11', retriesOpts), {
      ok: false,
      error: '失败最大重试次数须在 0 ~ 10 之间',
    });

    const budgetOpts = { label: 'Token 预算 (token_budget)', min: 500, max: 64000 };
    assert.equal(validateIntValue(4096, budgetOpts), null);
    assert.equal(validateIntValue(8192, budgetOpts), null);
    assert.equal(validateIntValue(499, budgetOpts), 'Token 预算 (token_budget)须在 500 ~ 64000 之间');
    assert.equal(validateIntValue(8000.5, budgetOpts), 'Token 预算 (token_budget)必须为整数');
  });

  it('validateOptionalInt 支持空串留空并校验非空输入区间', () => {
    const maxOutOpts = { label: '全篇输出 Token 上限', min: 500, max: 64000 };
    assert.deepEqual(validateOptionalInt('', maxOutOpts), { ok: true, value: undefined });
    assert.deepEqual(validateOptionalInt('   ', maxOutOpts), { ok: true, value: undefined });
    assert.deepEqual(validateOptionalInt('4000', maxOutOpts), { ok: true, value: 4000 });
    assert.deepEqual(validateOptionalInt('499', maxOutOpts), {
      ok: false,
      error: '全篇输出 Token 上限须在 500 ~ 64000 之间',
    });
    assert.deepEqual(validateOptionalInt('abc', maxOutOpts), {
      ok: false,
      error: '全篇输出 Token 上限必须为整数',
    });
  });

  it('validateHttpBaseUrl 正确校验协议、主机与禁止内嵌凭据', () => {
    assert.equal(validateHttpBaseUrl('https://api.deepseek.com', 'Base URL'), null);
    assert.equal(validateHttpBaseUrl(' http://192.168.56.10:8000/v1 ', 'Base URL'), null);

    assert.equal(validateHttpBaseUrl('', 'Base URL'), 'Base URL不能为空');
    assert.equal(
      validateHttpBaseUrl('api.deepseek.com', 'Base URL'),
      'Base URL必须为合法的 http/https 地址且不可内嵌凭据',
    );
    assert.equal(
      validateHttpBaseUrl('ftp://192.168.56.10/v1', 'Base URL'),
      'Base URL必须为合法的 http/https 地址且不可内嵌凭据',
    );
    assert.equal(
      validateHttpBaseUrl('https://user:pass@api.deepseek.com', 'Base URL'),
      'Base URL必须为合法的 http/https 地址且不可内嵌凭据',
    );
  });
});
