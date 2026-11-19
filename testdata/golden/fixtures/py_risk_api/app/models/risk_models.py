from sqlalchemy import Column, Float, Integer, String
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column
from django.db import models


class Base(DeclarativeBase):
    pass


class RiskEvent(Base):
    __tablename__ = "risk_events"

    id = Column(Integer, primary_key=True)
    user_id = Column(String(64), nullable=False, index=True)
    score = Column(Float, nullable=False)
    decision: Mapped[str] = mapped_column(String(32), nullable=False)


class BlacklistRule(models.Model):
    rule_code = models.CharField(max_length=64, unique=True)
    enabled = models.BooleanField(default=True)

    class Meta:
        db_table = "risk_blacklist_rules"
