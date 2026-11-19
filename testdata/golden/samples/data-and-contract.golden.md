# cpp_order_service 数据模型与接口契约

## 1. 对外接口契约（RPC / Proto / HTTP 签名）
<!-- slot:service_interfaces -->
核心对外服务由 `proto/order.proto`（包 `order.v1`，CROS 服务名 `order.core.v1`）与 `src/main.cpp` 暴露：

| 协议 | 接口标识 | 请求 / 响应 | 路由与超时策略 |
| :--- | :--- | :--- | :--- |
| CROS RPC | `order.v1.OrderService/CreateOrder` | `CreateOrderRequest` -> `CreateOrderResponse` | `cros.timeout_ms=2000`, `consistent_hash` |
| CROS RPC | `order.v1.OrderService/Pay` | `PayRequest` -> `PayResponse` | `cros.timeout_ms=5000`, `idempotent=true` |
| CROS RPC | `order.v1.OrderService/CancelOrder` | `CancelOrderRequest` -> `CancelOrderResponse` | `cros.timeout_ms=2000` |
| CROS RPC | `order.v1.OrderService/StreamOrderEvents` | `CreateOrderRequest` -> `stream OrderEvent` | `server_push` 服务端流 |
| HTTP | `GET /api/v1/health` | 健康检查探针 | `src/main.cpp` `RegisterHttpRoutes` |
| HTTP | `POST /v2/orders/pay` | 支付触发入口 | 转发至 `order::OrderService::Pay` |

## 2. 核心数据实体与存储/消息模型
<!-- slot:data_storage_models -->
- **关系型存储表 (`sql/schema.sql`)**：
  - `orders`：主键 `order_id`，字段含 `user_id`、`sku_id`、`amount_cents`、`status`、`created_at`，索引 `idx_orders_user_id`。
  - `order_payments`：主键 `tx_id`，字段含 `order_id`、`payment_channel`、`amount_cents`、`paid_at`，索引 `idx_order_payments_order_id`。
- **内存与传输实体 (`include/order/order_service.h`、`proto/order.proto`)**：
  - `order::OrderRecord` 保存内存订单聚合状态；`OrderStatus` 枚举覆盖 `CREATED`/`PAID`/`CANCELED`；`MetricBucket` 用于延迟指标聚合。

## 3. 协议边界、版本兼容性与错误码体系
<!-- slot:protocol_error_codes -->
- **协议演进边界**：`proto/order.proto` 采用 `proto3` 语法，字段编号保持只增不改；服务级 `cros.timeout_ms=3000` 作为默认兜底。
- **统一错误码 (`include/order/error_codes.h`)**：
  - `order::OrderErrorCode::OK` (`ERR_ORDER_OK = 0`)
  - `order::OrderErrorCode::Timeout` (`ERR_ORDER_TIMEOUT = 1001`)
  - `order::OrderErrorCode::PaymentFailed` (`ERR_PAYMENT_FAILED = 1002`)
  - `order::OrderErrorCode::NotFound` (`ERR_ORDER_NOT_FOUND = 1003`)

## 4. 契约演进约定与人工补充说明
<!-- slot:contract_invariants -->
- `Pay` 接口需保持幂等语义（依据 `order_id` 防重入）；新增字段须在 `proto/order.proto` 末尾追加并同步更新 `sql/schema.sql`。
