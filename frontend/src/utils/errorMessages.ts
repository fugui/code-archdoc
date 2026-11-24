import type { ApiErrorBody, VerifierReport } from '../types';

export interface FormattedApiError {
  title: string;
  message: string;
  code: string;
  status: number;
  currentRevisionNo?: number;
  currentVersion?: number;
  verifierReport?: VerifierReport;
  retryAfterSeconds?: number;
}

/**
 * 统一语义化 API 错误类，保留 HTTP 状态码、机器可读 code 与结构化 details。
 */
export class ArchdocApiError extends Error {
  public readonly status: number;
  public readonly code: string;
  public readonly details?: Record<string, unknown>;
  public readonly rawMessage: string;

  constructor(status: number, body: Partial<ApiErrorBody> | null | undefined, fallbackMessage = '请求失败，请稍后重试') {
    const serverMsg = typeof body?.error === 'string' ? body.error.trim() : '';
    super(serverMsg || fallbackMessage);
    this.name = 'ArchdocApiError';
    this.status = status;
    this.code = typeof body?.code === 'string' && body.code.trim() ? body.code.trim() : inferCodeFromStatus(status);
    this.details = body?.details && typeof body.details === 'object' ? body.details : undefined;
    this.rawMessage = serverMsg;
  }
}

function inferCodeFromStatus(status: number): string {
  switch (status) {
    case 400:
      return 'INVALID_PARAM';
    case 401:
      return 'UNAUTHORIZED';
    case 403:
      return 'FORBIDDEN';
    case 404:
      return 'NOT_FOUND';
    case 409:
      return 'CONFLICT';
    case 422:
      return 'VERIFIER_BLOCKED';
    case 429:
      return 'QUEUE_FULL';
    default:
      return status >= 500 ? 'INTERNAL_ERROR' : 'UNKNOWN_ERROR';
  }
}

function extractVerifierReport(details?: Record<string, unknown>): VerifierReport | undefined {
  if (!details) return undefined;
  const candidate = details.verifier_report;
  if (!candidate || typeof candidate !== 'object') return undefined;
  const rec = candidate as Record<string, unknown>;
  if (typeof rec.can_approve === 'boolean' && typeof rec.blocker_count === 'number') {
    return candidate as VerifierReport;
  }
  return undefined;
}

function extractNumberDetail(details: Record<string, unknown> | undefined, key: string): number | undefined {
  if (!details) return undefined;
  const val = details[key];
  return typeof val === 'number' && Number.isFinite(val) ? val : undefined;
}

/**
 * 将后端错误码与任务失败码映射为对研发友好的中文提示与结构化辅助字段。
 */
export function formatApiError(err: unknown, fallback = '操作失败，请稍后重试'): FormattedApiError {
  if (err instanceof ArchdocApiError) {
    const currentRevisionNo = extractNumberDetail(err.details, 'current_revision_no');
    const currentVersion = extractNumberDetail(err.details, 'current_version');
    const verifierReport = extractVerifierReport(err.details);
    const retryAfterSeconds = extractNumberDetail(err.details, 'retry_after_seconds');

    switch (err.code) {
      case 'OPTIMISTIC_LOCK_CONFLICT': {
        const revHint =
          currentRevisionNo !== undefined
            ? `服务端当前已更新至 v${currentRevisionNo}`
            : '服务端文档已产生新修订';
        return {
          title: '版本冲突（乐观锁拦截）',
          message: `${revHint}，当前编辑草稿已过期，请刷新最新内容后再提交保存。`,
          code: err.code,
          status: err.status,
          currentRevisionNo,
        };
      }
      case 'VERIFIER_BLOCKED': {
        const blockerCount = verifierReport?.blocker_count ?? 0;
        const errorCount = verifierReport?.error_count ?? 0;
        const reason =
          blockerCount > 0
            ? `检出 ${blockerCount} 项阻断性安全风险（如明文凭据/私钥泄漏），请先清除敏感内容后重试。`
            : errorCount > 0
              ? `存在 ${errorCount} 项必填槽位或校验错误，请完善后再发布。`
              : err.rawMessage;
        return {
          title: '发布被 Verifier 硬门禁拦截',
          message: reason,
          code: err.code,
          status: err.status,
          verifierReport,
        };
      }
      case 'PATH_ESCAPE':
        return {
          title: '非法路径访问被拦截',
          message: '目标源码路径超出仓库沙箱边界（包含目录穿越或越界符号链接），系统已拒绝读取。',
          code: err.code,
          status: err.status,
        };
      case 'DRAFT_ACCESS_DENIED':
        return {
          title: '无权查看未发布草稿',
          message: '仅该仓库/子目录负责人或管理员可查看未发布草稿与修订历史，普通用户仅可访问已发布版本。',
          code: err.code,
          status: err.status,
        };
      case 'FORBIDDEN':
        return {
          title: '操作权限不足',
          message: err.rawMessage || '您没有当前仓库/子目录的写入或治理权限。',
          code: err.code,
          status: err.status,
        };
      case 'INVALID_SUBDIR':
        return {
          title: '子目录参数非法',
          message: '子目录路径禁止包含 ".."、反斜杠或绝对路径前缀，请修正后重试。',
          code: err.code,
          status: err.status,
        };
      case 'DOC_TYPE_INACTIVE':
        return {
          title: '文档类型已停用',
          message: err.rawMessage || '所选文档类型已停用或不存在，请刷新后重新选择。',
          code: err.code,
          status: err.status,
        };
      case 'ENGINE_UNAVAILABLE':
        return {
          title: '所选生成引擎不可用',
          message:
            err.rawMessage ||
            '所选文档生成引擎当前不可用，请切换至 Native 内置引擎或联系管理员检查 OpenCode 环境。',
          code: err.code,
          status: err.status,
        };
      case 'TOKEN_BUDGET_EXCEEDED':
        return {
          title: '超出单任务 Token 预算上限（已熔断）',
          message:
            err.rawMessage ||
            '预估 Token 消耗超出管理员配置的熔断上限，未发出模型请求。建议按子目录拆分范围或补充 .archdocignore。',
          code: err.code,
          status: err.status,
        };
      case 'BATCH_TOKEN_BUDGET_EXCEEDED':
        return {
          title: '超出批量生成总 Token 预算上限（已熔断）',
          message:
            err.rawMessage ||
            '本次批量任务预估总消耗超出批量熔断上限，未发出任何模型请求。建议分批勾选部分模块或减少文档类型后重试。',
          code: err.code,
          status: err.status,
        };
      case 'MIGRATION_CONFLICT':
        return {
          title: '目标子目录已存在同类型文档（迁移冲突）',
          message:
            err.rawMessage ||
            '目标子目录与源子目录存在同类型文档冲突，请先清理冲突文档或更换目标子目录后重试。',
          code: err.code,
          status: err.status,
        };
      case 'FILE_BUDGET_EXCEEDED':
        return {
          title: '超出单任务文件数上限（已熔断）',
          message:
            err.rawMessage ||
            '有效源码文件数量超出预算上限，请缩小分析子目录或配置 .archdocignore 排除冗余文件。',
          code: err.code,
          status: err.status,
        };
      case 'REMOTE_BRANCH_FETCH_FAILED':
        return {
          title: '获取远端分支列表失败',
          message:
            err.rawMessage ||
            '无法连接远端 Git 仓库获取分支信息，请检查仓库远端地址、认证凭证或网络连通性后重试。',
          code: err.code,
          status: err.status,
        };
      case 'QUEUE_FULL': {
        const waitHint = retryAfterSeconds ? `，建议约 ${retryAfterSeconds} 秒后重试` : '';
        return {
          title: '任务队列已满',
          message: `当前并发生成任务较多${waitHint}。`,
          code: err.code,
          status: err.status,
          retryAfterSeconds,
        };
      }
      case 'UNAUTHORIZED':
        return {
          title: '登录会话已失效',
          message: '您的登录凭证已过期或无效，请重新登录。',
          code: err.code,
          status: err.status,
        };
      case 'NOT_FOUND':
        return {
          title: '目标资源不存在',
          message: err.rawMessage || '请求的仓库、文档、修订或源码文件不存在。',
          code: err.code,
          status: err.status,
        };
      case 'HOME_BINDING_PROTECTED':
        return {
          title: '受保护的主归属绑定',
          message:
            err.rawMessage || '活跃参考文档的唯一主归属绑定受防孤儿保护，请先转移归属作用域或归档后再删除。',
          code: err.code,
          status: err.status,
        };
      case 'VERSION_CONFLICT': {
        const verHint =
          currentVersion !== undefined
            ? `服务端参考文档已更新至 v${currentVersion}`
            : '服务端参考文档已更新至新版本';
        return {
          title: '参考文档版本冲突',
          message: err.rawMessage || `${verHint}，请对比最新内容后再合并保存。`,
          code: err.code,
          status: err.status,
          currentVersion,
        };
      }
      case 'ALREADY_BOUND':
        return {
          title: '作用域重复绑定',
          message: err.rawMessage || '该参考文档已绑定到当前目标作用域，无需重复绑定。',
          code: err.code,
          status: err.status,
        };
      case 'PAYLOAD_TOO_LARGE':
        return {
          title: '文档体积超出上限',
          message: err.rawMessage || '参考文档单篇正文大小超出服务端配置上限（默认 512KB），请精简后重试。',
          code: err.code,
          status: err.status,
        };
      case 'INLINE_BASE64_IMAGE':
        return {
          title: '禁止内联大体积 Base64 图片',
          message: err.rawMessage || '参考文档正文包含超大 Base64 Data URI，请改用外链或精简图表文本。',
          code: err.code,
          status: err.status,
        };
      case 'SECRET_DETECTED':
        return {
          title: '检出高危明文凭据（已硬阻断）',
          message:
            err.rawMessage || '参考文档包含私钥、固定前缀令牌或带密码 URL 等高危明文凭据，请手工彻底脱敏后重新保存。',
          code: err.code,
          status: err.status,
        };
      case 'SECRET_CONFIRM_REQUIRED':
        return {
          title: '检测到疑似敏感片段，请确认处理方式',
          message:
            err.rawMessage || '文本包含疑似密钥赋值或高熵字符串，可点击“自动脱敏并保存”或手动清理后提交。',
          code: err.code,
          status: err.status,
        };
      default:
        return {
          title: '请求执行失败',
          message: err.rawMessage || fallback,
          code: err.code,
          status: err.status,
        };
    }
  }

  if (err instanceof Error && err.message.trim()) {
    return {
      title: '操作失败',
      message: err.message.trim(),
      code: 'UNKNOWN_ERROR',
      status: 0,
    };
  }

  return {
    title: '操作失败',
    message: fallback,
    code: 'UNKNOWN_ERROR',
    status: 0,
  };
}

/**
 * 将异步任务的 error_code 映射为友好中文标题与建议说明（用于 GenerateTaskModal 展示）。
 */
export function formatTaskErrorCode(errorCode?: string, fallbackMessage?: string): { title: string; message: string } {
  const code = (errorCode || '').trim().toUpperCase();
  if (!code) {
    return {
      title: '任务执行失败',
      message: fallbackMessage || '任务执行过程中发生异常，可查看失败槽位并尝试单槽位重试。',
    };
  }
  const formatted = formatApiError(new ArchdocApiError(400, { code, error: fallbackMessage || '' }));
  return {
    title: formatted.title,
    message: formatted.message,
  };
}
