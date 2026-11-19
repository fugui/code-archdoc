package com.acme.billing.controller;

import com.acme.billing.model.InvoiceEntity;
import org.springframework.web.bind.annotation.*;

@RestController
@RequestMapping("/api/v1/billing")
public class BillingController {

    @GetMapping("/invoices/{id}")
    public InvoiceEntity getInvoice(@PathVariable("id") Long id) {
        return null;
    }

    @PostMapping("/invoices")
    public InvoiceEntity createInvoice(@RequestBody CreateInvoiceRequest req) {
        return null;
    }

    @PutMapping("/invoices/{id}/settle")
    public void settleInvoice(@PathVariable("id") Long id) {
    }

    @DeleteMapping("/invoices/{id}")
    public void cancelInvoice(@PathVariable("id") Long id) {
    }

    @RequestMapping(value = "/summary", method = RequestMethod.GET)
    public BillingSummary getSummary(@RequestParam("month") String month) {
        return null;
    }
}
