package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
)

// ErrInvalidMetricsParam 表示质量度量查询参数非法（HTTP 400 INVALID_PARAM）。
var ErrInvalidMetricsParam = errors.New("质量度量查询参数非法")

// MetricsOverviewVO 对应 GET /api/metrics/overview 响应（01-http-api-contract.md §3.9）。
type MetricsOverviewVO struct {
	WindowDays            int     `json:"window_days"`
	AvgEditDiffRatio      float64 `json:"avg_edit_diff_ratio"`
	ChatAcceptRate        float64 `json:"chat_accept_rate"`
	VerifierFirstPassRate float64 `json:"verifier_first_pass_rate"`
	VerifierBlockRate     float64 `json:"verifier_block_rate"`
	AvgSlotCoverageRate   float64 `json:"avg_slot_coverage_rate"`
	AvgTokensPerDoc       int64   `json:"avg_tokens_per_doc"`
	AvgCacheHitRatio      float64 `json:"avg_cache_hit_ratio"`
	SampleDocCount        int64   `json:"sample_doc_count"`
}

// RevisionMetricPointVO 对应 GET /api/metrics/revisions-compare 单个修订对比数据点。
type RevisionMetricPointVO struct {
	DocTypeRevisionID   uint      `json:"doc_type_revision_id"`
	RevisionNo          int       `json:"revision_no"`
	CreatedAt           time.Time `json:"created_at"`
	AvgEditDiffRatio    float64   `json:"avg_edit_diff_ratio"`
	ChatAcceptRate      float64   `json:"chat_accept_rate"`
	VerifierBlockRate   float64   `json:"verifier_block_rate"`
	AvgSlotCoverageRate float64   `json:"avg_slot_coverage_rate"`
	AvgTokensPerDoc     int64     `json:"avg_tokens_per_doc"`
	AvgCacheHitRatio    float64   `json:"avg_cache_hit_ratio"`
	SampleCount         int64     `json:"sample_count"`
}

// RevisionsCompareVO 对应 GET /api/metrics/revisions-compare 响应。
type RevisionsCompareVO struct {
	DocTypeSlug string                  `json:"doc_type_slug"`
	Revisions   []RevisionMetricPointVO `json:"revisions"`
}

// SlotHeatmapItemVO 对应 GET /api/metrics/slot-heatmap 单个槽位热力项。
type SlotHeatmapItemVO struct {
	SlotID              string  `json:"slot_id"`
	Title               string  `json:"title"`
	AvgEditDiffRatio    float64 `json:"avg_edit_diff_ratio"`
	VerifierWarningRate float64 `json:"verifier_warning_rate"`
	SampleCount         int64   `json:"sample_count"`
}

// SlotHeatmapVO 对应 GET /api/metrics/slot-heatmap 响应。
type SlotHeatmapVO struct {
	DocTypeSlug string              `json:"doc_type_slug"`
	Slots       []SlotHeatmapItemVO `json:"slots"`
}

// QualityMetricsService 聚合草稿→定稿修改率、Chat 采纳率、Verifier 拦截率、槽位完备度与 Token/KV Cache 指标（F-26 W2）。
type QualityMetricsService struct {
	govRepo     GovernanceRepo
	docTypeRepo DocTypeRepo
}

// NewQualityMetricsService 构造在线质量度量服务。
func NewQualityMetricsService(govRepo GovernanceRepo, docTypeRepo DocTypeRepo) *QualityMetricsService {
	return &QualityMetricsService{
		govRepo:     govRepo,
		docTypeRepo: docTypeRepo,
	}
}

// GetOverview 按时间窗口、可选 DocType 与 DocTypeRevision 聚合全局质量与成本 KPI。
func (s *QualityMetricsService) GetOverview(
	ctx context.Context,
	days int,
	docTypeSlug string,
	docTypeRevID *uint,
) (*MetricsOverviewVO, error) {
	windowDays := clampDays(days, 30)
	slug := strings.TrimSpace(docTypeSlug)
	if slug != "" {
		if err := doctype.ValidateSlug(slug); err != nil {
			return nil, fmt.Errorf("%w: 非法 doc_type", ErrInvalidMetricsParam)
		}
	}

	filter := models.MetricsFilter{
		Days:              windowDays,
		DocTypeSlug:       slug,
		DocTypeRevisionID: docTypeRevID,
	}
	metrics, err := s.govRepo.ListQualityMetrics(ctx, filter)
	if err != nil {
		return nil, err
	}
	usageLogs, err := s.govRepo.ListLLMUsageLogs(ctx, filter)
	if err != nil {
		return nil, err
	}

	agg := aggregateMetricsBucket(metrics, usageLogs)
	return &MetricsOverviewVO{
		WindowDays:            windowDays,
		AvgEditDiffRatio:      agg.avgEditDiffRatio,
		ChatAcceptRate:        agg.chatAcceptRate,
		VerifierFirstPassRate: agg.verifierFirstPassRate,
		VerifierBlockRate:     agg.verifierBlockRate,
		AvgSlotCoverageRate:   agg.avgSlotCoverageRate,
		AvgTokensPerDoc:       agg.avgTokensPerDoc,
		AvgCacheHitRatio:      agg.avgCacheHitRatio,
		SampleDocCount:        agg.sampleDocCount,
	}, nil
}

// CompareRevisions 按指定 DocType 的 DocTypeRevision 纵向对比质量与成本演进（revision_no ASC）。
func (s *QualityMetricsService) CompareRevisions(
	ctx context.Context,
	docTypeSlug string,
	days int,
) (*RevisionsCompareVO, error) {
	slug := strings.TrimSpace(docTypeSlug)
	if slug == "" {
		return nil, fmt.Errorf("%w: doc_type 参数必填", ErrInvalidMetricsParam)
	}
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, fmt.Errorf("%w: 非法 doc_type", ErrInvalidMetricsParam)
	}

	windowDays := clampDays(days, 90)
	revs, err := s.docTypeRepo.ListRevisions(ctx, slug)
	if err != nil {
		return nil, err
	}
	// ListRevisions 返回 revision_no DESC，反转/排序为 ASC 以展示时间演进趋势
	sort.Slice(revs, func(i, j int) bool {
		return revs[i].RevisionNo < revs[j].RevisionNo
	})

	filter := models.MetricsFilter{
		Days:        windowDays,
		DocTypeSlug: slug,
	}
	metrics, err := s.govRepo.ListQualityMetrics(ctx, filter)
	if err != nil {
		return nil, err
	}
	usageLogs, err := s.govRepo.ListLLMUsageLogs(ctx, filter)
	if err != nil {
		return nil, err
	}

	metricsByRev := make(map[uint][]models.QualityMetric)
	for _, m := range metrics {
		metricsByRev[m.DocTypeRevisionID] = append(metricsByRev[m.DocTypeRevisionID], m)
	}
	usageByRev := make(map[uint][]models.LLMUsageLog)
	for _, u := range usageLogs {
		if u.DocTypeRevisionID != nil && *u.DocTypeRevisionID > 0 {
			usageByRev[*u.DocTypeRevisionID] = append(usageByRev[*u.DocTypeRevisionID], u)
		}
	}

	points := make([]RevisionMetricPointVO, 0, len(revs))
	for _, rev := range revs {
		agg := aggregateMetricsBucket(metricsByRev[rev.ID], usageByRev[rev.ID])
		points = append(points, RevisionMetricPointVO{
			DocTypeRevisionID:   rev.ID,
			RevisionNo:          rev.RevisionNo,
			CreatedAt:           rev.CreatedAt,
			AvgEditDiffRatio:    agg.avgEditDiffRatio,
			ChatAcceptRate:      agg.chatAcceptRate,
			VerifierBlockRate:   agg.verifierBlockRate,
			AvgSlotCoverageRate: agg.avgSlotCoverageRate,
			AvgTokensPerDoc:     agg.avgTokensPerDoc,
			AvgCacheHitRatio:    agg.avgCacheHitRatio,
			SampleCount:         agg.sampleDocCount,
		})
	}

	return &RevisionsCompareVO{
		DocTypeSlug: slug,
		Revisions:   points,
	}, nil
}

// GetSlotHeatmap 按 slot_id 聚合历史草稿→定稿平均修改率与 Verifier 告警率，辅助定位薄弱槽位 Prompt。
func (s *QualityMetricsService) GetSlotHeatmap(
	ctx context.Context,
	docTypeSlug string,
	docTypeRevID *uint,
	days int,
) (*SlotHeatmapVO, error) {
	slug := strings.TrimSpace(docTypeSlug)
	if slug == "" {
		return nil, fmt.Errorf("%w: doc_type 参数必填", ErrInvalidMetricsParam)
	}
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, fmt.Errorf("%w: 非法 doc_type", ErrInvalidMetricsParam)
	}

	windowDays := clampDays(days, 30)
	filter := models.MetricsFilter{
		Days:              windowDays,
		DocTypeSlug:       slug,
		DocTypeRevisionID: docTypeRevID,
	}

	metrics, err := s.govRepo.ListQualityMetrics(ctx, filter)
	if err != nil {
		return nil, err
	}

	// 从 DocType 修订快照中建立 slot_id -> title 字典（最新修订优先）
	titleBySlot := make(map[string]string)
	if revs, rErr := s.docTypeRepo.ListRevisions(ctx, slug); rErr == nil {
		// ListRevisions 按 revision_no DESC 排列，从旧到新覆写使新标题优先
		for i := len(revs) - 1; i >= 0; i-- {
			var slotDefs []models.SlotDefinition
			if json.Unmarshal(revs[i].SlotsSnapshotJSON, &slotDefs) == nil {
				for _, sd := range slotDefs {
					if sd.SlotID != "" && sd.Title != "" {
						titleBySlot[sd.SlotID] = sd.Title
					}
				}
			}
		}
	}

	type slotAcc struct {
		ratioSum     float64
		ratioCount   int64
		warningHits  int64
		warningTotal int64
	}
	accs := make(map[string]*slotAcc)
	getAcc := func(sid string) *slotAcc {
		a, ok := accs[sid]
		if !ok {
			a = &slotAcc{}
			accs[sid] = a
		}
		return a
	}

	for _, m := range metrics {
		if m.MetricEvent != models.MetricEventDocPublished {
			continue
		}
		var ratios map[string]float64
		if len(m.SlotDiffRatiosJSON) > 2 && json.Unmarshal(m.SlotDiffRatiosJSON, &ratios) == nil {
			for sid, ratio := range ratios {
				if sid == "" {
					continue
				}
				acc := getAcc(sid)
				acc.ratioSum += ratio
				acc.ratioCount++
			}
		}
	}

	since := time.Now().AddDate(0, 0, -windowDays)
	if secRows, sErr := s.govRepo.ListSectionVerifierSummaries(ctx, slug, docTypeRevID, since); sErr == nil {
		for _, row := range secRows {
			if row.SlotID == "" {
				continue
			}
			if row.Title != "" && titleBySlot[row.SlotID] == "" {
				titleBySlot[row.SlotID] = row.Title
			}
			acc := getAcc(row.SlotID)
			acc.warningTotal++
			var vsum models.SectionVerifierSummary
			if len(row.VerifierSummaryJSON) > 2 && json.Unmarshal(row.VerifierSummaryJSON, &vsum) == nil {
				if vsum.UnverifiedCount > 0 || vsum.MermaidWarnings > 0 || vsum.MermaidErrors > 0 || vsum.SecretBlockers > 0 || vsum.HasBlocker {
					acc.warningHits++
				}
			}
		}
	}

	items := make([]SlotHeatmapItemVO, 0, len(accs))
	for sid, acc := range accs {
		avgRatio := 0.0
		if acc.ratioCount > 0 {
			avgRatio = roundMetric4(acc.ratioSum / float64(acc.ratioCount))
		}
		warnRate := 0.0
		if acc.warningTotal > 0 {
			warnRate = roundMetric4(float64(acc.warningHits) / float64(acc.warningTotal))
		}
		sampleCount := acc.ratioCount
		if sampleCount == 0 {
			sampleCount = acc.warningTotal
		}
		title := titleBySlot[sid]
		if title == "" {
			title = sid
		}
		items = append(items, SlotHeatmapItemVO{
			SlotID:              sid,
			Title:               title,
			AvgEditDiffRatio:    avgRatio,
			VerifierWarningRate: warnRate,
			SampleCount:         sampleCount,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].AvgEditDiffRatio != items[j].AvgEditDiffRatio {
			return items[i].AvgEditDiffRatio > items[j].AvgEditDiffRatio
		}
		return items[i].SlotID < items[j].SlotID
	})

	return &SlotHeatmapVO{
		DocTypeSlug: slug,
		Slots:       items,
	}, nil
}

type metricsBucketSummary struct {
	avgEditDiffRatio      float64
	chatAcceptRate        float64
	verifierFirstPassRate float64
	verifierBlockRate     float64
	avgSlotCoverageRate   float64
	avgTokensPerDoc       int64
	avgCacheHitRatio      float64
	sampleDocCount        int64
}

func aggregateMetricsBucket(metrics []models.QualityMetric, usageLogs []models.LLMUsageLog) metricsBucketSummary {
	var (
		editSum, editCount           float64
		chatAccepted, chatTotal      float64
		verifyBlocked, verifyTotal   float64
		verifyCovSum, verifyCovCount float64
		pubCovSum, pubCovCount       float64
	)
	docIDSet := make(map[uint]bool)

	for _, m := range metrics {
		if m.DocumentID > 0 {
			docIDSet[m.DocumentID] = true
		}
		switch m.MetricEvent {
		case models.MetricEventDocPublished:
			editSum += m.EditDiffRatio
			editCount++
			if m.SlotCoverageRate > 0 {
				pubCovSum += m.SlotCoverageRate
				pubCovCount++
			}
		case models.MetricEventChatProposalResolved:
			if m.ChatAccepted != nil {
				chatTotal++
				if *m.ChatAccepted {
					chatAccepted++
				}
			}
		case models.MetricEventAIDraftVerified:
			verifyTotal++
			if m.VerifierBlocked {
				verifyBlocked++
			}
			verifyCovSum += m.SlotCoverageRate
			verifyCovCount++
		}
	}

	var (
		totalPromptTokens int64
		totalCachedTokens int64
		totalAllTokens    int64
	)
	usageDocIDs := make(map[uint]bool)
	usageTaskIDs := make(map[uint]bool)
	for _, u := range usageLogs {
		totalPromptTokens += int64(u.PromptTokens)
		totalCachedTokens += int64(u.CachedPromptTokens)
		totalAllTokens += int64(u.PromptTokens + u.CompletionTokens)
		if u.DocumentID != nil && *u.DocumentID > 0 {
			usageDocIDs[*u.DocumentID] = true
		}
		if u.TaskID != nil && *u.TaskID > 0 {
			usageTaskIDs[*u.TaskID] = true
		}
	}

	out := metricsBucketSummary{
		sampleDocCount: int64(len(docIDSet)),
	}
	if editCount > 0 {
		out.avgEditDiffRatio = roundMetric4(editSum / editCount)
	}
	if chatTotal > 0 {
		out.chatAcceptRate = roundMetric4(chatAccepted / chatTotal)
	}
	if verifyTotal > 0 {
		out.verifierBlockRate = roundMetric4(verifyBlocked / verifyTotal)
		out.verifierFirstPassRate = roundMetric4((verifyTotal - verifyBlocked) / verifyTotal)
	}
	if verifyCovCount > 0 {
		out.avgSlotCoverageRate = roundMetric4(verifyCovSum / verifyCovCount)
	} else if pubCovCount > 0 {
		out.avgSlotCoverageRate = roundMetric4(pubCovSum / pubCovCount)
	}

	if totalPromptTokens > 0 {
		out.avgCacheHitRatio = roundMetric4(float64(totalCachedTokens) / float64(totalPromptTokens))
	}

	docDenom := int64(len(usageDocIDs))
	if docDenom == 0 {
		docDenom = int64(len(usageTaskIDs))
	}
	if docDenom == 0 {
		docDenom = out.sampleDocCount
	}
	if docDenom > 0 && totalAllTokens > 0 {
		out.avgTokensPerDoc = int64(math.Round(float64(totalAllTokens) / float64(docDenom)))
	}
	return out
}

func clampDays(days, defaultDays int) int {
	if days <= 0 {
		return defaultDays
	}
	if days > 365 {
		return 365
	}
	return days
}

func roundMetric4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
