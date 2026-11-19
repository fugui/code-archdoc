#pragma once

#define ERR_ORDER_OK 0
#define ERR_ORDER_TIMEOUT 1001
#define ERR_PAYMENT_FAILED 1002
#define ERR_ORDER_NOT_FOUND 1003

namespace order {

enum class OrderErrorCode {
    OK = 0,
    Timeout = 1001,
    PaymentFailed = 1002,
    NotFound = 1003
};

} // namespace order
