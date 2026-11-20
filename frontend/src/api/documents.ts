import type {
  DocumentDetailVO,
  DocumentListVO,
  DocumentRevisionSummaryVO,
  ExportZipPayload,
  ItemsResponse,
  PublishDocumentPayload,
  PublishResultVO,
  ResolveOrphanPayload,
  RevisionDiffVO,
  RollbackDocumentPayload,
  SectionVO,
  SourcePreviewQuery,
  SourcePreviewVO,
  UnpublishResultVO,
  UpdateOwnershipPayload,
  UpdateSectionPayload,
} from '../types';
import { apiDownloadFile, apiRequest } from './client';

export async function listDocuments(
  repoId: number,
  subdirectory = '',
  refName = '',
): Promise<DocumentListVO> {
  const qs = new URLSearchParams({ repo_id: String(repoId) });
  if (subdirectory) qs.set('subdirectory', subdirectory);
  if (refName) qs.set('ref_name', refName);
  return apiRequest<DocumentListVO>(`/documents?${qs.toString()}`);
}

export async function getDocument(docId: number, revisionNo?: number): Promise<DocumentDetailVO> {
  const qs = revisionNo && revisionNo > 0 ? `?revision_no=${revisionNo}` : '';
  return apiRequest<DocumentDetailVO>(`/documents/${docId}${qs}`);
}

export async function listDocumentRevisions(docId: number): Promise<DocumentRevisionSummaryVO[]> {
  const res = await apiRequest<ItemsResponse<DocumentRevisionSummaryVO>>(
    `/documents/${docId}/revisions`,
  );
  return Array.isArray(res.items) ? res.items : [];
}

export async function diffDocumentRevisions(
  docId: number,
  fromRev: number,
  toRev: number,
): Promise<RevisionDiffVO> {
  const qs = new URLSearchParams({
    from_rev: String(fromRev),
    to_rev: String(toRev),
  });
  return apiRequest<RevisionDiffVO>(`/documents/${docId}/revisions/diff?${qs.toString()}`);
}

export async function updateDocumentSection(
  docId: number,
  slotId: string,
  payload: UpdateSectionPayload,
): Promise<DocumentDetailVO> {
  return apiRequest<DocumentDetailVO>(
    `/documents/${docId}/sections/${encodeURIComponent(slotId)}`,
    {
      method: 'PUT',
      bodyData: payload,
    },
  );
}

export async function updateSectionOwnership(
  docId: number,
  slotId: string,
  payload: UpdateOwnershipPayload,
): Promise<SectionVO> {
  return apiRequest<SectionVO>(
    `/documents/${docId}/sections/${encodeURIComponent(slotId)}/ownership`,
    {
      method: 'PATCH',
      bodyData: payload,
    },
  );
}

export async function resolveOrphanSection(
  docId: number,
  slotId: string,
  payload: ResolveOrphanPayload,
): Promise<DocumentDetailVO> {
  return apiRequest<DocumentDetailVO>(
    `/documents/${docId}/sections/${encodeURIComponent(slotId)}/orphan-resolve`,
    {
      method: 'POST',
      bodyData: payload,
    },
  );
}

export async function rollbackDocument(
  docId: number,
  payload: RollbackDocumentPayload,
): Promise<DocumentDetailVO> {
  return apiRequest<DocumentDetailVO>(`/documents/${docId}/rollback`, {
    method: 'POST',
    bodyData: payload,
  });
}

export async function getSourcePreview(
  docId: number,
  query: SourcePreviewQuery,
): Promise<SourcePreviewVO> {
  const qs = new URLSearchParams({ file: query.file });
  if (query.commit) qs.set('commit', query.commit);
  if (query.lineStart && query.lineStart > 0) qs.set('line_start', String(query.lineStart));
  if (query.lineEnd && query.lineEnd > 0) qs.set('line_end', String(query.lineEnd));
  if (query.symbol) qs.set('symbol', query.symbol);
  if (query.scope === 'repo') qs.set('scope', 'repo');
  return apiRequest<SourcePreviewVO>(`/documents/${docId}/source-preview?${qs.toString()}`);
}

export async function publishDocument(
  docId: number,
  payload: PublishDocumentPayload = {},
): Promise<PublishResultVO> {
  return apiRequest<PublishResultVO>(`/documents/${docId}/publish`, {
    method: 'POST',
    bodyData: payload,
  });
}

export async function unpublishDocument(docId: number): Promise<UnpublishResultVO> {
  return apiRequest<UnpublishResultVO>(`/documents/${docId}/unpublish`, {
    method: 'POST',
  });
}

export async function exportDocumentMarkdown(
  docId: number,
  fallbackFilename = `archdoc-${docId}.md`,
): Promise<string> {
  return apiDownloadFile(`/documents/${docId}/export`, fallbackFilename, {
    method: 'GET',
  });
}

export async function exportDocumentsZip(
  payload: ExportZipPayload,
  fallbackFilename = 'archdoc-export.zip',
): Promise<string> {
  return apiDownloadFile('/exports/zip', fallbackFilename, {
    method: 'POST',
    bodyData: payload,
  });
}
