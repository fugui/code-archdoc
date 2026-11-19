package com.acme.billing.mapper;

import com.acme.billing.model.InvoiceEntity;
import org.apache.ibatis.annotations.Mapper;
import org.apache.ibatis.annotations.Select;

@Mapper
public interface InvoiceMapper {

    @Select("SELECT * FROM billing_ledger WHERE invoice_no = #{invoiceNo}")
    InvoiceEntity findByInvoiceNo(String invoiceNo);
}
