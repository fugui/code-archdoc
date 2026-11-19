# cpp_order_service 配置、部署与研发上手

## 1. 脱敏配置项全量清单与环境变量映射
<!-- slot:config_inventory -->
核心运行配置位于 `config/config.yaml`（敏感凭据已强制脱敏，严禁明文入库）：

| 配置键路径 | 类型 | 安全默认值 | 敏感标记 |
| :--- | :--- | :--- | :---: |
| `server.host` | string | `127.0.0.1` | 否 |
| `server.port` | int | `9080` | 否 |
| `server.worker_threads` | int | `4` | 否 |
| `redis.pool_size` | int | `32` | 否 |
| `order.timeout_ms` | int | `3000` | 否 |
| `metrics.shm_name` | string | `/order_pay_ring` | 否 |
| `database.db_password` | string | `[REDACTED]` | 是 |
| `database.dsn` | string | `postgres://***:***@10.0.0.12:5432/orders` | 是 |
| `payment.api_key` | string | `[REDACTED]` | 是 |

## 2. 部署拓扑、运行依赖与端口/资源清单
<!-- slot:deploy_topology -->
- **进程与端口**：单实例二进制 `order_service` 默认监听 `9080` 端口，依赖 PostgreSQL（`orders` / `order_payments`）与 Redis 连接池（`redis.pool_size=32`）。
- **操作系统资源**：需支持 POSIX 共享内存 `/order_pay_ring`（`shm_open` + `mmap`）及 Linux `epoll_wait` / `timerfd_create` 系统调用，支持 `SIGTERM` 信号处理（`sigaction`）。

## 3. 本地构建、编译开关、单测与调试指南
<!-- slot:local_build_dev -->
- **构建系统 (`CMakeLists.txt`、`compile_commands.json`)**：需要 CMake >= 3.16 与支持 C++17（`-std=c++17`）的编译器，依赖 `Protobuf`、`glog`、`Threads`。
- **关键编译开关**：`ENABLE_METRICS=1` 开启 `order::OrderService::GetOrderMetrics` 条件编译分支；`CROS_ENABLED=1` 与 `-DCROS_PROTOCOL_V2` 启用 CROS RPC 集成。
- **构建产物**：静态库 `order_core` 与可执行文件 `order_service`。

## 4. 关键日志、错误码与高频故障排障手册
<!-- slot:troubleshooting_logs -->
- **`ERR_ORDER_TIMEOUT` (`1001`)**：检查 `CreateOrder` 入参合法性及 `order.timeout_ms` 配置。
- **`ERR_PAYMENT_FAILED` (`1002`)**：核对 `order::OrderService::Pay` 的支付渠道与金额是否与 `OrderRecord` 一致。
- **`ERR_ORDER_NOT_FOUND` (`1003`)**：确认目标 `order_id` 是否已创建或已被清理。
- **指标接口缺失**：检查编译参数是否启用 `-DENABLE_METRICS=1`。
