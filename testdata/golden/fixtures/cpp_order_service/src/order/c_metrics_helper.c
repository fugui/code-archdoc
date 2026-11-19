#include <pthread.h>
#include <signal.h>

struct MetricBucket {
    int count;
    long total_latency_us;
};

static pthread_mutex_t g_metrics_lock = PTHREAD_MUTEX_INITIALIZER;

static void metrics_signal_handler(int sig) {
    (void)sig;
}

void metrics_init(struct MetricBucket* bucket) {
    if (bucket == 0) {
        return;
    }
    pthread_mutex_lock(&g_metrics_lock);
    bucket->count = 0;
    bucket->total_latency_us = 0;
    pthread_mutex_unlock(&g_metrics_lock);
    signal(SIGUSR1, metrics_signal_handler);
}

void metrics_record_latency(struct MetricBucket* bucket, long latency_us) {
    if (bucket == 0) {
        return;
    }
    pthread_mutex_lock(&g_metrics_lock);
    bucket->count += 1;
    bucket->total_latency_us += latency_us;
    pthread_mutex_unlock(&g_metrics_lock);
}
