package com.acme.billing.job;

import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;

@Component
public class BillingReconcileJob {

    @Scheduled(cron = "0 0 2 * * *")
    public void reconcileDailyInvoices() {
        // reconcile daily billing ledger
    }

    @Scheduled(fixedDelay = 60000)
    public void retryFailedSettlements() {
        // retry failed settlements
    }
}
