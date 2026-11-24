import React, { useCallback, useEffect, useState } from 'react';
import { AlertTriangle, History, Loader2, RefreshCw, RotateCcw } from 'lucide-react';
import { getRefDocDiff, listRefDocVersions } from '../../api/refDocs';
import type { RefDocDiffResponse, RefDocVersionsResponse } from '../../types';
import { formatApiError } from '../../utils/errorMessages';
import { classifyRefDocDiffLine } from '../../utils/refDocStudioUtils';

export interface RefDocVersionHistoryPanelProps {
  docId: number;
  currentVersion: number;
  canEdit: boolean;
  restoring: boolean;
  onRestoreVersion: (versionNo: number) => Promise<void>;
}

function formatByteSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${bytes} B`;
}

export const RefDocVersionHistoryPanel: React.FC<RefDocVersionHistoryPanelProps> = ({
  docId,
  currentVersion,
  canEdit,
  restoring,
  onRestoreVersion,
}) => {
  const [loadingVersions, setLoadingVersions] = useState<boolean>(false);
  const [versionsData, setVersionsData] = useState<RefDocVersionsResponse | null>(null);
  const [versionsError, setVersionsError] = useState<string>('');

  const [fromVersion, setFromVersion] = useState<number>(1);
  const [toVersion, setToVersion] = useState<number>(currentVersion);
  const [loadingDiff, setLoadingDiff] = useState<boolean>(false);
  const [diffData, setDiffData] = useState<RefDocDiffResponse | null>(null);
  const [diffError, setDiffError] = useState<string>('');

  const loadDiff = useCallback(
    async (fromV: number, toV: number) => {
      if (fromV <= 0 || toV <= 0) return;
      setLoadingDiff(true);
      setDiffError('');
      try {
        const res = await getRefDocDiff(docId, fromV, toV);
        setDiffData(res);
      } catch (err: unknown) {
        const f = formatApiError(err, '加载版本差异失败');
        setDiffError(`${f.title}：${f.message}`);
        setDiffData(null);
      } finally {
        setLoadingDiff(false);
      }
    },
    [docId],
  );

  const loadVersions = useCallback(async () => {
    setLoadingVersions(true);
    setVersionsError('');
    try {
      const res = await listRefDocVersions(docId);
      setVersionsData(res);
      const latest = res.current_version;
      const prev = Math.max(1, latest - 1);
      setFromVersion(prev);
      setToVersion(latest);
      if (latest > 1) {
        await loadDiff(prev, latest);
      } else {
        setDiffData(null);
      }
    } catch (err: unknown) {
      const f = formatApiError(err, '加载版本清单失败');
      setVersionsError(`${f.title}：${f.message}`);
    } finally {
      setLoadingVersions(false);
    }
  }, [docId, loadDiff]);

  useEffect(() => {
    void loadVersions();
  }, [loadVersions, currentVersion]);

  const versions = versionsData?.versions ?? [];

  return (
    <div className="code-archdoc-refstudio-pane">
      <div className="code-archdoc-refstudio-pane-head">
        <span className="flex-center gap-xs">
          <History size={14} />
          <span>不可变版本快照（v{versionsData?.current_version ?? currentVersion}）</span>
        </span>
        <button
          type="button"
          className="btn btn-text btn-small"
          disabled={loadingVersions}
          onClick={() => void loadVersions()}
        >
          <RefreshCw size={12} />
          <span>刷新</span>
        </button>
      </div>

      {versionsError && (
        <div className="code-archdoc-alert code-archdoc-alert--danger">
          <AlertTriangle size={15} />
          <div className="code-archdoc-alert-body">{versionsError}</div>
        </div>
      )}

      {loadingVersions ? (
        <div className="code-archdoc-loading">
          <Loader2 size={16} className="code-archdoc-spin" />
          <span>正在加载版本列表...</span>
        </div>
      ) : (
        <div className="code-archdoc-timeline">
          {versions.map((ver) => (
            <div
              key={ver.version_no}
              className={`code-archdoc-timeline-item ${
                ver.is_current ? 'code-archdoc-timeline-item--current' : ''
              }`}
            >
              <div className="code-archdoc-timeline-head">
                <div className="flex-center gap-xs">
                  <span className="code-archdoc-mono">v{ver.version_no}</span>
                  {ver.is_current && <span className="code-badge code-badge--primary">当前版本</span>}
                  {!ver.file_exists && (
                    <span className="code-badge code-badge--danger">磁盘文件缺失</span>
                  )}
                </div>
                <span className="text-muted">
                  {ver.file_exists ? formatByteSize(ver.byte_size) : '不可读'}
                </span>
              </div>

              <div className="code-archdoc-timeline-foot">
                <button
                  type="button"
                  className="btn btn-text btn-small"
                  disabled={!ver.file_exists || ver.version_no === currentVersion}
                  onClick={() => {
                    setFromVersion(ver.version_no);
                    setToVersion(currentVersion);
                    void loadDiff(ver.version_no, currentVersion);
                  }}
                >
                  对比至当前 v{currentVersion}
                </button>

                {canEdit && !ver.is_current && ver.file_exists && (
                  <button
                    type="button"
                    className="btn btn-secondary btn-small"
                    disabled={restoring}
                    onClick={() => void onRestoreVersion(ver.version_no)}
                    title="不可变前滚：不覆盖历史文件，直接以该版本正文生成最新版本号"
                  >
                    {restoring ? (
                      <Loader2 size={12} className="code-archdoc-spin" />
                    ) : (
                      <RotateCcw size={12} />
                    )}
                    <span>恢复此版本为新版</span>
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* 双版本 Unified Diff 对比区 */}
      {versions.length >= 2 && (
        <div className="code-archdoc-refstudio-diff-section">
          <div className="code-archdoc-refstudio-diff-controls">
            <label className="code-archdoc-refstudio-diff-select-label">
              <span>基准</span>
              <select
                className="code-select"
                value={fromVersion}
                onChange={(e) => {
                  const nextFrom = Number(e.target.value);
                  setFromVersion(nextFrom);
                  void loadDiff(nextFrom, toVersion);
                }}
              >
                {versions.map((v) => (
                  <option key={v.version_no} value={v.version_no} disabled={!v.file_exists}>
                    v{v.version_no}
                    {!v.file_exists ? ' (缺失)' : ''}
                  </option>
                ))}
              </select>
            </label>

            <span className="text-muted">→</span>

            <label className="code-archdoc-refstudio-diff-select-label">
              <span>目标</span>
              <select
                className="code-select"
                value={toVersion}
                onChange={(e) => {
                  const nextTo = Number(e.target.value);
                  setToVersion(nextTo);
                  void loadDiff(fromVersion, nextTo);
                }}
              >
                {versions.map((v) => (
                  <option key={v.version_no} value={v.version_no} disabled={!v.file_exists}>
                    v{v.version_no}
                    {!v.file_exists ? ' (缺失)' : ''}
                  </option>
                ))}
              </select>
            </label>
          </div>

          {diffError && (
            <div className="code-archdoc-alert code-archdoc-alert--warning">
              <AlertTriangle size={14} />
              <div className="code-archdoc-alert-body">{diffError}</div>
            </div>
          )}

          {loadingDiff ? (
            <div className="code-archdoc-loading">
              <Loader2 size={15} className="code-archdoc-spin" />
              <span>正在计算行级差异...</span>
            </div>
          ) : diffData ? (
            <div className="code-archdoc-diff-card">
              <div className="code-archdoc-diff-card-head">
                <span>
                  v{diffData.from_version} → v{diffData.to_version}
                </span>
                <span className="code-archdoc-mono">
                  +{diffData.added_lines} / -{diffData.removed_lines}
                </span>
              </div>
              <div className="code-archdoc-diff-lines">
                {diffData.unified_diff ? (
                  diffData.unified_diff.split('\n').map((line, idx) => (
                    <div key={idx} className={classifyRefDocDiffLine(line)}>
                      {line || ' '}
                    </div>
                  ))
                ) : (
                  <div className="code-archdoc-diff-line">（两版本正文完全一致，无行级差异）</div>
                )}
              </div>
            </div>
          ) : null}
        </div>
      )}
    </div>
  );
};

export default RefDocVersionHistoryPanel;
