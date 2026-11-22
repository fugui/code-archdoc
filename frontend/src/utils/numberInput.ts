/** PostgreSQL 32 位有符号整型上限，用于防止前端传入超大整数触发数据库溢出 */
export const MAX_PG_INT = 2147483647;

export interface IntRangeOptions {
  label: string;
  min?: number;
  max?: number;
}

export type IntValidationResult =
  | { ok: true; value: number }
  | { ok: false; error: string };

export type OptionalIntValidationResult =
  | { ok: true; value?: number }
  | { ok: false; error: string };

/**
 * 严格解析十进制整数字符串：拒绝小数、科学计数法、非数字后缀及超出安全整数范围的值。
 */
export function parseStrictInt(raw: string): number | null {
  const trimmed = raw.trim();
  if (!/^-?\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  if (!Number.isSafeInteger(parsed)) return null;
  return parsed === 0 ? 0 : parsed;
}

/**
 * 校验已有数值是否满足整数与闭区间 [min, max] 约束。
 */
export function validateIntValue(value: number, opts: IntRangeOptions): string | null {
  if (!Number.isInteger(value)) {
    return `${opts.label}必须为整数`;
  }
  const hasBoundedRange =
    opts.min !== undefined && opts.max !== undefined && opts.max < MAX_PG_INT;
  if (hasBoundedRange && (value < opts.min! || value > opts.max!)) {
    return `${opts.label}须在 ${opts.min} ~ ${opts.max} 之间`;
  }
  if (opts.min !== undefined && value < opts.min) {
    return `${opts.label}不能小于 ${opts.min}`;
  }
  if (opts.max !== undefined && value > opts.max) {
    return `${opts.label}不能大于 ${opts.max}`;
  }
  return null;
}

/**
 * 校验必填整数输入框的字符串值并返回解析后的安全整数。
 */
export function validateRequiredInt(raw: string, opts: IntRangeOptions): IntValidationResult {
  if (raw.trim() === '') {
    return { ok: false, error: `${opts.label}不能为空` };
  }
  const parsed = parseStrictInt(raw);
  if (parsed === null) {
    return { ok: false, error: `${opts.label}必须为整数` };
  }
  const err = validateIntValue(parsed, opts);
  if (err) {
    return { ok: false, error: err };
  }
  return { ok: true, value: parsed };
}

/**
 * 校验可选整数输入框的字符串值：留空返回 { ok: true, value: undefined }。
 */
export function validateOptionalInt(
  raw: string,
  opts: IntRangeOptions,
): OptionalIntValidationResult {
  if (raw.trim() === '') {
    return { ok: true, value: undefined };
  }
  return validateRequiredInt(raw, opts);
}

/**
 * 校验 HTTP/HTTPS Base URL 合法性并禁止内嵌 userinfo 凭据（与后端 validateProbeBaseURL 对齐）。
 */
export function validateHttpBaseUrl(raw: string, label: string): string | null {
  const trimmed = raw.trim();
  if (!trimmed) {
    return `${label}不能为空`;
  }
  try {
    const parsed = new URL(trimmed);
    const validScheme = parsed.protocol === 'http:' || parsed.protocol === 'https:';
    if (!validScheme || !parsed.hostname || parsed.username !== '' || parsed.password !== '') {
      return `${label}必须为合法的 http/https 地址且不可内嵌凭据`;
    }
    return null;
  } catch {
    return `${label}必须为合法的 http/https 地址且不可内嵌凭据`;
  }
}
