from fastapi import APIRouter
from flask import Flask

router = APIRouter(prefix="/api/v1/risk")
app = Flask(__name__)


@router.get("/score/{user_id}")
async def get_user_risk_score(user_id: str) -> dict:
    return {"user_id": user_id, "score": 0.12}


@router.post("/evaluate")
def evaluate_transaction(tx_id: str, amount: float) -> dict:
    return {"tx_id": tx_id, "decision": "allow"}


@app.route("/healthz", methods=["GET"])
def health_check():
    return {"status": "ok"}


@app.post("/webhook/alert")
def receive_alert():
    return {"accepted": True}
