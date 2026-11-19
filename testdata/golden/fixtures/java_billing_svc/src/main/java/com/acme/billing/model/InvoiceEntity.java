package com.acme.billing.model;

import jakarta.persistence.*;

@Entity
@Table(name = "billing_invoices")
public class InvoiceEntity {

    @Id
    @Column(name = "invoice_id", nullable = false)
    private Long id;

    @Column(name = "invoice_no", nullable = false, unique = true)
    private String invoiceNo;

    @Column(name = "amount_cents", nullable = false)
    private Long amountCents;

    private String status;

    public Long getId() {
        return id;
    }

    public String getInvoiceNo() {
        return invoiceNo;
    }
}
