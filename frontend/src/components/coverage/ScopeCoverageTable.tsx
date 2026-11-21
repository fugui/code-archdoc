import React from 'react';
import type { DocTypeSummaryVO, RepoScopeCoverageVO } from '../../types';
import { shortSha } from '../../utils/urlQuery';
import ArchElementBadge from './ArchElementBadge';
import CoverageActionButtons from './CoverageActionButtons';
import ScopeDocCoverageCell from './ScopeDocCoverageCell';
import ScopeDocExpandedGrid from './ScopeDocExpandedGrid';
import ScopeIdentityCell from './ScopeIdentityCell';

export interface ScopeCoverageTableProps {
  rows: RepoScopeCoverageVO[];
  docTypes: DocTypeSummaryVO[];
  focusedDocTypeSlug?: string;
  expandedRowKeys: Set<string>;
  showArchColumn?: boolean;
  showSelection?: boolean;
  selectedRepoId?: number | null;
  selectedSubdirs?: string[];
  discoveringRepoId?: number | null;
  exportingKey?: string | null;
  compact?: boolean;
  subdirCountsByRepo?: Map<number, number>;
  expandedSubdirRepos?: Set<number>;
  onToggleSubdirRepo?: (repoId: number) => void;
  onToggleExpandRow: (rowKey: string) => void;
  onToggleSelectSubdir?: (row: RepoScopeCoverageVO) => void;
  onDiscoverModules?: (row: RepoScopeCoverageVO) => void;
  onOpenModuleGovernance?: (row: RepoScopeCoverageVO) => void;
  onOpenActiveTask: (row: RepoScopeCoverageVO) => void;
  onOpenStudio: (repoId: number, docId?: number, subdirectory?: string) => void;
  onTriggerGenerate: (row: RepoScopeCoverageVO, docTypeSlug?: string) => void;
  onBatchGenerate?: (row: RepoScopeCoverageVO) => void;
  onExportZip: (row: RepoScopeCoverageVO) => void;
}

export const ScopeCoverageTable: React.FC<ScopeCoverageTableProps> = ({
  rows,
  docTypes,
  focusedDocTypeSlug,
  expandedRowKeys,
  showArchColumn = true,
  showSelection = false,
  selectedRepoId = null,
  selectedSubdirs = [],
  discoveringRepoId = null,
  exportingKey = null,
  compact = false,
  subdirCountsByRepo,
  expandedSubdirRepos,
  onToggleSubdirRepo,
  onToggleExpandRow,
  onToggleSelectSubdir,
  onDiscoverModules,
  onOpenModuleGovernance,
  onOpenActiveTask,
  onOpenStudio,
  onTriggerGenerate,
  onBatchGenerate,
  onExportZip,
}) => {
  const colSpan = showArchColumn ? 5 : 4;

  return (
    <table className={`code-table ${compact ? 'code-archdoc-table--compact' : ''}`}>
      <thead>
        <tr>
          <th>仓库 / 子目录范围</th>
          {showArchColumn && <th>所属架构元素</th>}
          <th>负责人与提交</th>
          <th>文档覆盖（{docTypes.length} 类）</th>
          <th className="code-archdoc-coverage-actions-col">操作</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row, idx) => {
          const rowKey = `${row.repo_id}:${row.subdirectory}`;
          const expanded = expandedRowKeys.has(rowKey);
          const approvedCount = Object.values(row.doc_statuses ?? {}).filter(
            (d) => d.status === 'approved',
          ).length;
          const firstDoc = Object.values(row.doc_statuses ?? {})[0];
          const isRowChecked =
            showSelection &&
            row.scope_type === 'subdir' &&
            selectedRepoId === row.repo_id &&
            selectedSubdirs.includes(row.subdirectory);

          return (
            <React.Fragment key={rowKey}>
              <tr className={expanded ? 'code-archdoc-coverage-row--expanded' : ''}>
                <td>
                  <ScopeIdentityCell
                    row={row}
                    showSelection={showSelection}
                    isSelected={isRowChecked}
                    isDiscovering={discoveringRepoId === row.repo_id}
                    subdirCount={
                      row.scope_type === 'repo' ? subdirCountsByRepo?.get(row.repo_id) : undefined
                    }
                    subdirsExpanded={
                      row.scope_type === 'repo'
                        ? Boolean(expandedSubdirRepos?.has(row.repo_id))
                        : false
                    }
                    onToggleSubdirs={
                      row.scope_type === 'repo' && onToggleSubdirRepo
                        ? () => onToggleSubdirRepo(row.repo_id)
                        : undefined
                    }
                    onToggleSelect={
                      onToggleSelectSubdir ? () => onToggleSelectSubdir(row) : undefined
                    }
                    onDiscoverModules={
                      onDiscoverModules ? () => onDiscoverModules(row) : undefined
                    }
                    onOpenActiveTask={() => onOpenActiveTask(row)}
                  />
                </td>

                {showArchColumn && (
                  <td>
                    <ArchElementBadge row={row} />
                  </td>
                )}

                <td>
                  <div className="code-archdoc-owner-name">{row.owner_name || '-'}</div>
                  <div className="code-archdoc-coverage-meta-row">
                    <span title={row.latest_commit_sha || '暂无快照'}>
                      Commit:{' '}
                      <code className="code-archdoc-mono">{shortSha(row.latest_commit_sha)}</code>
                    </span>
                  </div>
                </td>

                <td>
                  <ScopeDocCoverageCell
                    row={row}
                    docTypes={docTypes}
                    focusedDocTypeSlug={focusedDocTypeSlug}
                    expanded={expanded}
                    onToggleExpand={() => onToggleExpandRow(rowKey)}
                    onOpenStudio={(docId) =>
                      onOpenStudio(row.repo_id, docId, row.subdirectory)
                    }
                    onTriggerGenerate={(slug) => onTriggerGenerate(row, slug)}
                  />
                </td>

                <td className="code-archdoc-coverage-actions-col">
                  <CoverageActionButtons
                    row={row}
                    exporting={exportingKey === rowKey}
                    approvedCount={approvedCount}
                    tooltipPlacement={idx === 0 ? 'bottom' : 'top'}
                    onOpenStudio={() =>
                      onOpenStudio(row.repo_id, firstDoc?.document_id, row.subdirectory)
                    }
                    onTriggerGenerate={() => onTriggerGenerate(row)}
                    onBatchGenerate={onBatchGenerate ? () => onBatchGenerate(row) : undefined}
                    onOpenModuleGovernance={
                      onOpenModuleGovernance ? () => onOpenModuleGovernance(row) : undefined
                    }
                    onExportZip={() => onExportZip(row)}
                  />
                </td>
              </tr>

              {expanded && (
                <tr className="code-archdoc-coverage-expanded-row">
                  <td colSpan={colSpan} className="code-archdoc-coverage-expanded-cell">
                    <ScopeDocExpandedGrid
                      row={row}
                      docTypes={docTypes}
                      focusedDocTypeSlug={focusedDocTypeSlug}
                      onOpenStudio={(docId) =>
                        onOpenStudio(row.repo_id, docId, row.subdirectory)
                      }
                      onTriggerGenerate={(slug) => onTriggerGenerate(row, slug)}
                    />
                  </td>
                </tr>
              )}
            </React.Fragment>
          );
        })}
      </tbody>
    </table>
  );
};

export default ScopeCoverageTable;
