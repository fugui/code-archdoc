# cpp_order_service AI Coding Agent 上下文包

## 1. 模块边界与核心目录速览
<!-- slot:agent_module_summary -->
- **语言与构建**：C++17 / C，CMake 构建（`CMakeLists.txt`），产出 `order_core` 静态库与 `order_service` 可执行文件。
- **核心路径**：协议 `proto/order.proto`、头文件 `include/order/order_service.h`、实现 `src/order/order_service.cpp`、C 指标 `src/order/c_metrics_helper.c`、入口 `src/main.cpp`、表结构 `sql/schema.sql`、配置 `config/config.yaml`。

## 2. 不可违背的架构红线与构建测试命令
<!-- slot:agent_hard_constraints -->
- 严禁修改 `third_party/` 或手工编辑 `*.pb.cc` / `*.pb.h` 自动生成产物。
- 访问 `order::OrderService` 内部 `orders_` 容器必须加 `order_mutex_` 锁；`MetricBucket` 更新必须持 `pthread_mutex_lock`。
- 严禁将 `config/config.yaml` 中的真实凭据或 `dsn` 口令输出到日志或文档。
- 构建命令：`cmake -B build -DENABLE_METRICS=1 && cmake --build build`。

## 3. 高频改码入口、关键符号与常见坑点
<!-- slot:agent_change_checklist -->
- **关键符号**：`order::OrderService::CreateOrder`、`order::OrderService::Pay`、`order::OrderService::CancelOrder`、`order::OrderService::GetOrderMetrics`（受 `#ifdef ENABLE_METRICS` 条件编译保护）、`metrics_record_latency`、`RegisterHttpRoutes`。
- **常见坑点**：`GetOrderMetrics` 位于条件编译块内，修改签名时需同时保持 `#ifdef ENABLE_METRICS` 保护；`Pay` 契约要求幂等且超时为 `5000ms`。
