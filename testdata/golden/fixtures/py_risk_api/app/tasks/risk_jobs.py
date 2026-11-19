from celery import Celery, shared_task
from apscheduler.schedulers.background import BackgroundScheduler

celery_app = Celery("risk_jobs")
scheduler = BackgroundScheduler()


@celery_app.task(name="risk.recompute_scores")
def recompute_scores(batch_id: str) -> int:
    return 0


@shared_task
def sync_blacklist_cache() -> None:
    pass


@scheduler.scheduled_job("interval", minutes=5)
def sweep_expired_events() -> None:
    pass
