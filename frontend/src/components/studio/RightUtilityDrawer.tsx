import React from 'react';
import { Code2, History, X } from 'lucide-react';
import type { DocumentDetailVO } from '../../types';
import EvidenceSourceTab, { type EvidenceInspectRequest } from './EvidenceSourceTab';
import RevisionHistoryTab from './RevisionHistoryTab';

export type RightDrawerTab = 'evidence' | 'history';

export interface RightUtilityDrawerProps {
  activeTab: RightDrawerTab;
  doc: DocumentDetailVO | null;
  evidenceRequest: EvidenceInspectRequest | null;
  onChangeTab: (tab: RightDrawerTab) => void;
  onClose: () => void;
  onRolledBack: (updatedDoc: DocumentDetailVO) => void;
}

export const RightUtilityDrawer: React.FC<RightUtilityDrawerProps> = ({
  activeTab,
  doc,
  evidenceRequest,
  onChangeTab,
  onClose,
  onRolledBack,
}) => {
  return (
    <aside className="code-archdoc-studio-right">
      <div className="code-archdoc-right-header">
        <div className="code-archdoc-right-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'evidence'}
            className={`code-archdoc-right-tab ${
              activeTab === 'evidence' ? 'code-archdoc-right-tab--active' : ''
            }`}
            onClick={() => onChangeTab('evidence')}
          >
            <Code2 size={14} />
            <span>源码证据</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'history'}
            className={`code-archdoc-right-tab ${
              activeTab === 'history' ? 'code-archdoc-right-tab--active' : ''
            }`}
            onClick={() => onChangeTab('history')}
          >
            <History size={14} />
            <span>修订历史</span>
          </button>
        </div>

        <button
          type="button"
          className="btn btn-text btn-small"
          onClick={onClose}
          title="折叠右侧辅助面板"
          aria-label="折叠右侧面板"
        >
          <X size={15} />
        </button>
      </div>

      <div className="code-archdoc-right-body">
        {activeTab === 'evidence' ? (
          <EvidenceSourceTab request={evidenceRequest} />
        ) : doc ? (
          <RevisionHistoryTab doc={doc} onRolledBack={onRolledBack} />
        ) : null}
      </div>
    </aside>
  );
};

export default RightUtilityDrawer;
