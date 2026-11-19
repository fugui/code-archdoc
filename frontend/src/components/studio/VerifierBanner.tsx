import React, { useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  ShieldAlert,
} from 'lucide-react';
import type { VerifierReport } from '../../types';

export interface VerifierBannerProps {
  report: VerifierReport;
  onJumpToSlot?: (slotId: string) => void;
}

export const VerifierBanner: React.FC<VerifierBannerProps> = ({ report, onJumpToSlot }) => {
  const [expanded, setExpanded] = useState<boolean>(!report.can_approve);

  const totalFindings =
    report.secret_findings.length +
    report.unverified_refs.length +
    report.mermaid_issues.length +
    report.missing_required_slots.length;

  const variant = !report.can_approve
    ? 'danger'
    : report.warning_count > 0 || report.error_count > 0
      ? 'warning'
      : 'success';

  const coveragePct = Math.round((report.slot_coverage_rate || 0) * 100);

  return (
    <section className={`code-archdoc-verifier code-archdoc-verifier--${variant}`}>
      <button
        type="button"
        className="code-archdoc-verifier-header"
        onClick={() => setExpanded((prev) => !prev)}
        aria-expanded={expanded}
      >
        <div className="code-archdoc-verifier-summary">
          {variant === 'danger' ? (
            <ShieldAlert size={16} />
          ) : variant === 'warning' ? (
            <AlertTriangle size={16} />
          ) : (
            <CheckCircle2 size={16} />
          )}
          <span className="code-archdoc-verifier-title">
            {variant === 'danger'
              ? 'Verifier 硬门禁阻断（当前修订禁止审批发布）'
              : variant === 'warning'
                ? 'Verifier 校验通过但存在告警项（可审批发布）'
                : 'Verifier 四重门禁全部通过'}
          </span>
          <span className="code-badge code-badge--muted">必填槽位覆盖 {coveragePct}%</span>
          {report.blocker_count > 0 && (
            <span className="code-badge code-badge--danger">阻断 {report.blocker_count}</span>
          )}
          {report.error_count > 0 && (
            <span className="code-badge code-badge--danger">错误 {report.error_count}</span>
          )}
          {report.warning_count > 0 && (
            <span className="code-badge code-badge--warning">告警 {report.warning_count}</span>
          )}
        </div>

        {totalFindings > 0 && (
          <span className="code-archdoc-verifier-toggle">
            {expanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
            <span>{expanded ? '收起明细' : `查看 ${totalFindings} 项明细`}</span>
          </span>
        )}
      </button>

      {expanded && totalFindings > 0 && (
        <div className="code-archdoc-verifier-body">
          {/* 1. 明文凭据/私钥阻断 */}
          {report.secret_findings.length > 0 && (
            <div className="code-archdoc-verifier-group">
              <div className="code-archdoc-verifier-group-title code-archdoc-text-danger">
                明文凭据 / 敏感密钥阻断 ({report.secret_findings.length})
              </div>
              <ul className="code-archdoc-verifier-list">
                {report.secret_findings.map((item, idx) => (
                  <li key={`${item.slot_id}-${item.rule_id}-${idx}`}>
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => onJumpToSlot?.(item.slot_id)}
                    >
                      [{item.slot_id}]
                    </button>
                    <span>
                      规则 <code className="code-archdoc-mono">{item.rule_id}</code>（第 {item.line_offset} 行）
                      命中脱敏片段 <code className="code-archdoc-mono">{item.matched_mask}</code>
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* 2. 缺失必填槽位 */}
          {report.missing_required_slots.length > 0 && (
            <div className="code-archdoc-verifier-group">
              <div className="code-archdoc-verifier-group-title code-archdoc-text-danger">
                缺失必填章节槽位 ({report.missing_required_slots.length})
              </div>
              <div className="flex-wrap gap-xs">
                {report.missing_required_slots.map((slotId) => (
                  <span key={slotId} className="code-badge code-badge--danger">
                    {slotId}
                  </span>
                ))}
              </div>
            </div>
          )}

          {/* 3. 未验证符号 / 文件引用 */}
          {report.unverified_refs.length > 0 && (
            <div className="code-archdoc-verifier-group">
              <div className="code-archdoc-verifier-group-title code-archdoc-text-warning">
                未验证符号 / 路径引用 ({report.unverified_refs.length})
              </div>
              <ul className="code-archdoc-verifier-list">
                {report.unverified_refs.map((ref, idx) => (
                  <li key={`${ref.slot_id}-${ref.value}-${idx}`}>
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => onJumpToSlot?.(ref.slot_id)}
                    >
                      [{ref.slot_id}]
                    </button>
                    <span className="code-badge code-badge--muted">{ref.ref_kind}</span>
                    <code className="code-archdoc-mono">{ref.value}</code>
                    <span className="text-muted">（未在当前快照 Code IR 中找到精确匹配）</span>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* 4. Mermaid 语法 / 复杂度告警 */}
          {report.mermaid_issues.length > 0 && (
            <div className="code-archdoc-verifier-group">
              <div className="code-archdoc-verifier-group-title code-archdoc-text-warning">
                Mermaid 图表校验告警 ({report.mermaid_issues.length})
              </div>
              <ul className="code-archdoc-verifier-list">
                {report.mermaid_issues.map((issue, idx) => (
                  <li key={`${issue.slot_id}-${issue.block_index}-${idx}`}>
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => onJumpToSlot?.(issue.slot_id)}
                    >
                      [{issue.slot_id}]
                    </button>
                    <span className="code-badge code-badge--muted">{issue.diagram_type || 'mermaid'}</span>
                    <span>{issue.message}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </section>
  );
};

export default VerifierBanner;
