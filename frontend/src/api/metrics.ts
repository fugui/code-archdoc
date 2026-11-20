import type {
  MetricsOverviewQuery,
  MetricsOverviewVO,
  RevisionsCompareQuery,
  RevisionsCompareVO,
  SlotHeatmapQuery,
  SlotHeatmapVO,
} from '../types';
import { apiRequest } from './client';

export async function getMetricsOverview(
  query?: MetricsOverviewQuery,
): Promise<MetricsOverviewVO> {
  const params = new URLSearchParams();
  if (query?.days && query.days > 0) {
    params.set('days', String(query.days));
  }
  if (query?.docType?.trim()) {
    params.set('doc_type', query.docType.trim());
  }
  if (query?.docTypeRevisionId && query.docTypeRevisionId > 0) {
    params.set('doc_type_revision_id', String(query.docTypeRevisionId));
  }
  const qs = params.toString();
  return apiRequest<MetricsOverviewVO>(`/metrics/overview${qs ? `?${qs}` : ''}`);
}

export async function compareMetricsRevisions(
  query: RevisionsCompareQuery,
): Promise<RevisionsCompareVO> {
  const params = new URLSearchParams();
  params.set('doc_type', query.docType);
  if (query.days && query.days > 0) {
    params.set('days', String(query.days));
  }
  return apiRequest<RevisionsCompareVO>(`/metrics/revisions-compare?${params.toString()}`);
}

export async function getMetricsSlotHeatmap(
  query: SlotHeatmapQuery,
): Promise<SlotHeatmapVO> {
  const params = new URLSearchParams();
  params.set('doc_type', query.docType);
  if (query.docTypeRevisionId && query.docTypeRevisionId > 0) {
    params.set('doc_type_revision_id', String(query.docTypeRevisionId));
  }
  if (query.days && query.days > 0) {
    params.set('days', String(query.days));
  }
  return apiRequest<SlotHeatmapVO>(`/metrics/slot-heatmap?${params.toString()}`);
}
