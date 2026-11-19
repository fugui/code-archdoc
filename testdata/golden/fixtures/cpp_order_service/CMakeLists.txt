cmake_minimum_required(VERSION 3.16)
project(cpp_order_service VERSION 1.2.0 LANGUAGES C CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

add_compile_definitions(ENABLE_METRICS=1 CROS_ENABLED=1)

find_package(Protobuf REQUIRED)
find_package(glog REQUIRED)
find_package(Threads REQUIRED)

include_directories(${CMAKE_CURRENT_SOURCE_DIR}/include)

add_library(order_core STATIC
    src/order/order_service.cpp
    src/order/c_metrics_helper.c
)

add_executable(order_service
    src/main.cpp
)

target_link_libraries(order_service PRIVATE
    order_core
    protobuf::libprotobuf
    glog::glog
    Threads::Threads
)
