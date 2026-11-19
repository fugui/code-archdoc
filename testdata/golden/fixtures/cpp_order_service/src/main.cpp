#include "order/order_service.h"

#include <iostream>
#include <string>

struct SimpleRouter {
    void Get(const char* path, const char* handler) { (void)path; (void)handler; }
    void Post(const char* path, const char* handler) { (void)path; (void)handler; }
};

void RegisterHttpRoutes(SimpleRouter& router) {
    router.Get("/api/v1/health", "HealthHandler");
    router.Post("/v2/orders/pay", "order::OrderService::Pay");
}

int main(int argc, char** argv) {
    (void)argc;
    (void)argv;
    order::OrderService svc;
    svc.StartBackgroundWorker();

    SimpleRouter router;
    RegisterHttpRoutes(router);

    std::string order_id;
    svc.CreateOrder("u1001", "sku_42", 9900, &order_id);
    std::cout << "order_service started, sample=" << order_id << std::endl;
    return 0;
}
