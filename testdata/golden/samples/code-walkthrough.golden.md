# cpp_order_service 代码导读与改码指南

## 1. 仓库目录职责与核心模块地图
<!-- slot:directory_map -->
- `CMakeLists.txt` / `compile_commands.json`：定义 `order_core` 与 `order_service` 构建目标与编译宏开关。
- `proto/order.proto`：CROS RPC 服务契约（`order.v1.OrderService`）与消息协议定义。
- `include/order/`：对外头文件与宏定义（`export.h`、`error_codes.h`、`order_service.h`）。
- `src/order/`：核心服务实现（`order_service.cpp`）与 C 语言延迟统计辅助模块（`c_metrics_helper.c`）。
- `src/main.cpp`：服务启动入口与 HTTP 路由注册（`RegisterHttpRoutes`、`main`）。
- `sql/schema.sql` & `config/config.yaml`：数据库表结构与服务运行配置。

## 2. 核心请求逐跳源码导航
<!-- slot:call_chain_guide -->
1. **服务启动 (`src/main.cpp`)**：`main` 构造 `order::OrderService`，调用 `order::OrderService::StartBackgroundWorker` 初始化后台 `epoll_wait` / `timerfd_create` / `pthread_create` 线程，再通过 `RegisterHttpRoutes` 注册 `/api/v1/health` 与 `/v2/orders/pay`。
2. **创建订单 (`src/order/order_service.cpp`)**：`order::OrderService::CreateOrder` 校验参数后获取 `order_mutex_`（`std::lock_guard`），写入 `orders_` 并递增 `active_orders_`。
3. **支付与共享内存上报 (`src/order/order_service.cpp`)**：`order::OrderService::Pay` 获取 `std::unique_lock` 校验订单金额与渠道，通过 `shm_open` + `mmap` 写入 `/order_pay_ring` 后置状态为已支付。
4. **指标读取 (`src/order/order_service.cpp`)**：在 `#ifdef ENABLE_METRICS` 下，`order::OrderService::GetOrderMetrics` 持 `std::shared_lock` 返回活跃订单计数。

## 3. 典型二次开发修改清单（新增接口/字段/配置）
<!-- slot:modification_recipes -->
- **新增 CROS RPC 接口**：① 在 `proto/order.proto` 增加请求/响应消息及 `rpc` 定义与 `cros.*` option；② 在 `include/order/order_service.h` 声明成员函数；③ 在 `src/order/order_service.cpp` 实现并确保对共享状态加锁；④ 必要时在 `src/main.cpp` `RegisterHttpRoutes` 注册调试路由。
- **新增错误码**：同步修改 `include/order/error_codes.h` 中的 `ERR_*` 宏与 `order::OrderErrorCode` 枚举。
- **新增配置项**：在 `config/config.yaml` 声明安全默认值，敏感凭据键名必须避免写入明文默认值。
