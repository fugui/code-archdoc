#pragma once

#include <atomic>
#include <cstdint>
#include <mutex>
#include <shared_mutex>
#include <string>
#include <unordered_map>

#include "order/error_codes.h"
#include "order/export.h"

namespace order {

// 订单内存聚合实体
struct OrderRecord {
    std::string order_id;
    std::string user_id;
    int64_t amount_cents;
    int status;
};

// 核心订单交易服务（带导出宏修饰）
API_EXPORT class OrderService {
public:
    OrderService();
    ~OrderService();

    CROS_RPC_METHOD OrderErrorCode CreateOrder(
        const std::string& user_id,
        const std::string& sku_id,
        int64_t amount_cents,
        std::string* out_order_id);

    CROS_RPC_METHOD OrderErrorCode Pay(
        const std::string& order_id,
        const std::string& channel,
        int64_t amount_cents);

    OrderErrorCode CancelOrder(
        const std::string& order_id,
        const std::string& reason);

#ifdef ENABLE_METRICS
    int64_t GetOrderMetrics() const;
#endif

    void StartBackgroundWorker();

private:
    mutable std::mutex order_mutex_;
    mutable std::shared_mutex cache_mutex_;
    std::atomic<int64_t> active_orders_{0};
    std::unordered_map<std::string, OrderRecord> orders_;
};

} // namespace order
